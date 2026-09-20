package react

import (
	"fmt"
	"strings"
)

// This file is half of the harness: what we send. parse.go is the other half,
// what we read back.
//
// A language model can only do one thing — continue text. It cannot call a
// function, look anything up, or find out what day it is. An "agent" is
// therefore a loop around that one ability, built from three pieces:
//
//  1. a prompt that tells the model what tools exist and what format to use
//     when it wants one (this file);
//  2. a parser that reads the model's reply and works out which tool it asked
//     for (parse.go);
//  3. a loop that runs the tool, appends the result to the conversation, and
//     asks again (agent.go).
//
// Nothing here is provider magic. The model emits text like "Action:
// calculator"; we, not the API, decide that means "call the calculator". That
// is what makes this a low-level harness: every mechanism is visible in this
// package, and you can read the exact bytes of it in the UI's trace panel.

// systemPromptTmpl is the contract. It does three jobs, and every line of it
// earns its place by having been needed at some point:
//
//   - it lists the tools, so the model knows what it can ask for;
//   - it fixes the reply format, so the parser has something to match against;
//   - it forbids the model from writing Observations, because an Observation is
//     a fact from the real world and only the loop is allowed to produce one.
//
// The Thought line is not decoration. Asking the model to say what it is doing
// before it acts is the "Re" in ReAct: it produces visibly better tool choices,
// and it is what fills the reasoning column in the UI.
const systemPromptTmpl = `You are a ReAct agent. You answer a question by thinking step by step and using tools.

Available tools:
%s
Every reply you make begins with a Thought line. Reply in exactly this format,
one block at a time:

Thought: what you know so far and what you need next
Action: the name of one tool, exactly as written above
Action Input: the input to that tool, on a single line

Then STOP and wait. The system runs the tool and replies with:

Observation: the tool's result

Repeat Thought/Action/Action Input as many times as you need. When you can
answer, finish with exactly:

Thought: I now know the final answer
Final Answer: the answer, in prose, for the user

Rules:
- Begin every reply with "Thought:" — including the reply that answers. A reply that starts with "Action:" is malformed.
- Never write an Observation yourself; only the system writes those.
- Never invent tool output. If a tool fails, say so in your next Thought and try a different input or a different tool.
- One Action per block. Never emit both an Action and a Final Answer.
- Prefer a tool over your own memory for arithmetic, for the current date, and for facts you are not certain about.
- If the question needs no tool at all, reply with a single Thought and the Final Answer.`

// buildSystemPrompt renders the template with the tool catalogue. The catalogue
// is the whole of the model's knowledge about the tools: there is no schema and
// no registration step on the provider's side, just these lines of prose. That
// is why a tool's description matters so much — see Toolbox.describe.
func buildSystemPrompt(tb *Toolbox) string {
	return fmt.Sprintf(systemPromptTmpl, tb.describe())
}

// Turn is one message in the conversation we send. The agent speaks as
// TurnUser; the model's own previous blocks are sent back as TurnModel.
//
// Sending its replies back in the model role — rather than pasting the whole
// exchange into one long user message — matters because a chat model is trained
// on alternating turns. Measured on a weaker model, the concatenated form
// dropped the Thought line on 13% of steps against 3% for real turns.
type Turn struct {
	Role string
	Text string
}

const (
	TurnUser  = "user"
	TurnModel = "model"
)

// padEntry is one completed round of the loop: what the model said, and what we
// said back. The agent keeps a slice of these and rebuilds the whole
// conversation from it on every call — see buildTurns.
type padEntry struct {
	Reply       string // the model's block, rebuilt canonically by replyBlock
	Observation string // what the tool returned
}

// formatReminder is appended to every user turn. It looks redundant next to the
// system prompt, and is not: with the rounds split into real turns, a bare
// "Question: …" reads as ordinary chat and the model answers tersely. Removing
// this line once produced a run of eight steps that emitted no Thought at all
// and then hit the step limit.
//
// It sits at the end of a user turn, which is the last thing the model reads
// before replying — and because user turns are only ever appended, never
// edited, adding it does not disturb the prefix that a provider's prompt cache
// matches on.
const formatReminder = "\n\n(Reply with Thought:, then either Action/Action Input or Final Answer.)"

// buildTurns rebuilds the conversation from the question and the scratchpad.
//
// The loop is stateless: this runs from scratch on every step, and the agent
// holds no conversation handle, no session id, nothing but []padEntry. A run is
// therefore fully described by what it sends — which is why the request can be
// dumped in the UI and read top to bottom.
//
// The cost is that everything is re-sent every step, so a run's token bill
// grows with the square of its length. truncate bounds the worst of it.
func buildTurns(question string, pad []padEntry) []Turn {
	turns := make([]Turn, 0, 1+2*len(pad))
	turns = append(turns, Turn{
		Role: TurnUser,
		Text: "Question: " + strings.TrimSpace(question) + formatReminder,
	})
	for _, e := range pad {
		turns = append(turns,
			Turn{Role: TurnModel, Text: e.Reply},
			Turn{Role: TurnUser, Text: "Observation: " + e.Observation + formatReminder},
		)
	}
	return turns
}

// replyBlock renders a parsed step back into the block the model will see as
// its own previous turn.
//
// Note that it is rebuilt from the parse rather than echoed verbatim. If the
// model wrapped its reply in code fences, rambled before the Thought, or wrote
// an Observation it was not entitled to write, none of that is replayed to it.
// Feeding a model its own malformed output teaches it that the malformed output
// was acceptable.
func replyBlock(s step) string {
	var b strings.Builder
	if s.Thought != "" {
		fmt.Fprintf(&b, "Thought: %s\n", s.Thought)
	}
	fmt.Fprintf(&b, "Action: %s\nAction Input: %s", s.Tool, s.Input)
	return b.String()
}

// FormatTurns renders a conversation as a readable transcript, for the trace
// event and for -v. It is a view of the request, not the request itself: the
// bytes that actually went out are captured in Wire.
func FormatTurns(turns []Turn) string {
	var b strings.Builder
	for i, t := range turns {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "[%s]\n%s", t.Role, t.Text)
	}
	return b.String()
}

// turnsBytes is the size of the conversation, so the UI can show it growing.
func turnsBytes(turns []Turn) int {
	n := 0
	for _, t := range turns {
		n += len(t.Text)
	}
	return n
}

// truncate bounds a single observation. Tool output is re-sent on every later
// step, so one chatty tool would otherwise grow the request quadratically and
// crowd out the reasoning.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("… [truncated, %d bytes total]", len(s))
}
