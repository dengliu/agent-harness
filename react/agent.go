// Package react implements a small ReAct agent: a loop in which a language
// model alternates between reasoning in prose and calling tools, using each
// tool result to decide what to do next.
//
//	Thought → Action → Observation → Thought → … → Final Answer
//
// The tool protocol is our own, not the provider's. Tools are described to the
// model in the system prompt, it asks for them in text, and this package parses
// that text and runs them. Nothing about tool use is delegated to the API,
// which is the point: every moving part of an agent harness is visible here.
//
// Where to read:
//
//	prompt.go  what we send — the system prompt, the conversation, the scratchpad
//	parse.go   what we read back — turning the model's prose into a decision
//	agent.go   the loop that joins them (this file)
//	tool.go    the Tool interface and the registry the catalogue is built from
//	openai.go  the provider binding, behind a one-method Model interface
//	wire.go    a transport that keeps the raw HTTP exchange for the UI
package react

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Config tunes a single agent.
type Config struct {
	MaxSteps       int           // give up after this many Thought/Action rounds
	ToolTimeout    time.Duration // per tool call
	MaxObservation int           // bytes of tool output fed back to the model
}

func (c Config) withDefaults() Config {
	if c.MaxSteps <= 0 {
		c.MaxSteps = 8
	}
	if c.ToolTimeout <= 0 {
		c.ToolTimeout = 20 * time.Second
	}
	if c.MaxObservation <= 0 {
		c.MaxObservation = 2000
	}
	return c
}

// Agent runs the ReAct loop against a Model and a Toolbox.
type Agent struct {
	model Model
	tools *Toolbox
	cfg   Config
}

func New(model Model, tools *Toolbox, cfg Config) *Agent {
	return &Agent{model: model, tools: tools, cfg: cfg.withDefaults()}
}

func (a *Agent) Tools() *Toolbox { return a.tools }

// ErrMaxSteps is returned when the agent used its whole step budget without
// reaching a Final Answer.
var ErrMaxSteps = errors.New("reached the step limit without a final answer")

// Run answers question, reporting progress through emit (which may be nil).
//
// This is the harness in full. Read it as five steps repeated until the model
// stops asking for tools:
//
//  1. rebuild the whole conversation from the scratchpad   (buildTurns)
//  2. ask the model to continue it                         (Model.Generate)
//  3. read its reply and work out what it wants            (parseStep)
//  4. run the tool it named                                (runTool)
//  5. append the round and go again
//
// Everything that looks like an "agent framework" is these five lines of
// plumbing plus the prompt in prompt.go. The provider does none of it.
func (a *Agent) Run(ctx context.Context, question string, emit Emitter) (string, error) {
	start := time.Now()
	system := buildSystemPrompt(a.tools)
	emit.emit(Event{Type: EventStart, Text: question, System: system})

	// pad is the entire state of the run: one entry per completed round. There
	// is no conversation handle on the provider's side to go out of sync with.
	var pad []padEntry

	for i := 1; i <= a.cfg.MaxSteps; i++ {
		// (1) and (2). The conversation is rebuilt from scratch every step, so
		// what goes out is always a complete, readable record of the run.
		turns := buildTurns(question, pad)
		genStart := time.Now()
		genCtx, wire := withWire(ctx)
		raw, err := a.model.Generate(genCtx, system, turns, []string{"\nObservation:"})
		if err != nil {
			return a.fail(emit, start, err)
		}

		// Report the exchange before interpreting it. Everything the agent
		// emits after this is an *interpretation* of that text, so surfacing
		// the text itself is what lets someone watching the UI check the
		// interpretation — and see a malformed block for what it is.
		trace := Event{
			Type:        EventTrace,
			Step:        i,
			Prompt:      FormatTurns(turns),
			Text:        strings.TrimSpace(raw),
			Elapsed:     since(genStart),
			PromptBytes: len(system) + turnsBytes(turns),
		}
		if wire.Request != "" {
			trace.Wire = wire
		}
		emit.emit(trace)

		// (3) Read the reply.
		s, err := parseStep(raw)
		if err != nil {
			// The model produced something the protocol does not cover. Rather
			// than fail the run, put the problem in front of it as if it were
			// an observation and let it correct itself; only running out of
			// steps ends things. Its own text goes back with the correction,
			// because there is no parsed block to rebuild.
			pad = append(pad, padEntry{
				Reply:       strings.TrimSpace(raw),
				Observation: "Your last reply was not in the required format (" + err.Error() + "). Reply with either Action/Action Input or a Final Answer.",
			})
			emit.emit(Event{Type: EventObservation, Step: i, Text: "malformed model reply; asking it to retry", Ok: false})
			continue
		}

		if s.Thought != "" {
			emit.emit(Event{Type: EventThought, Step: i, Text: s.Thought})
		}

		// The model says it is done. Its text is the answer; the loop ends.
		if s.Final != "" {
			emit.emit(Event{Type: EventFinal, Step: i, Text: s.Final, Elapsed: since(start)})
			return s.Final, nil
		}

		// (4) Run the tool it named.
		emit.emit(Event{Type: EventAction, Step: i, Tool: s.Tool, Input: s.Input})
		toolStart := time.Now()
		obs, ok := a.runTool(ctx, s.Tool, s.Input)
		emit.emit(Event{Type: EventObservation, Step: i, Tool: s.Tool, Text: obs, Ok: ok, Elapsed: since(toolStart)})

		// (5) Append the round. Note that the Observation stored here is the
		// one the *tool* produced — the model has no way to write into this
		// slice, which is what makes the trace trustworthy.
		pad = append(pad, padEntry{
			Reply:       replyBlock(s),
			Observation: truncate(obs, a.cfg.MaxObservation),
		})
	}
	return a.fail(emit, start, ErrMaxSteps)
}

// runTool executes one tool call.
//
// Every failure here returns text rather than an error, and the bool only tells
// the UI whether to colour it red. A missing tool, a bad expression, an HTTP
// timeout — each becomes the next Observation, so the model reads it and tries
// something else. That single decision is most of what makes the loop robust:
// the alternative, failing the run, means any model slip ends the conversation.
func (a *Agent) runTool(ctx context.Context, name, input string) (string, bool) {
	tool, found := a.tools.Get(name)
	if !found {
		// Listing the real names turns a dead end into a recoverable mistake.
		return fmt.Sprintf("There is no tool called %q. Available tools: %v.", name, a.tools.Names()), false
	}
	ctx, cancel := context.WithTimeout(ctx, a.cfg.ToolTimeout)
	defer cancel()

	out, err := tool.Run(ctx, input)
	if err != nil {
		return fmt.Sprintf("%s failed: %v", name, err), false
	}
	if out == "" {
		return fmt.Sprintf("%s returned nothing.", name), true
	}
	return out, true
}

func (a *Agent) fail(emit Emitter, start time.Time, err error) (string, error) {
	emit.emit(Event{Type: EventError, Text: err.Error(), Elapsed: since(start)})
	return "", err
}
