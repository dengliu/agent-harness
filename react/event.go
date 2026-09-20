package react

import "time"

// EventType names a single observable moment in a ReAct run. The agent emits
// events as it goes so that a caller — the CLI printer or the SSE handler that
// feeds the web UI — can render the reasoning trace while it is still running.
type EventType string

const (
	EventStart       EventType = "start"       // run began
	EventTrace       EventType = "trace"       // raw model reply for this step, before parsing
	EventThought     EventType = "thought"     // model reasoned about what to do next
	EventAction      EventType = "action"      // model chose a tool and an input
	EventObservation EventType = "observation" // tool returned (or failed)
	EventFinal       EventType = "final"       // model produced the answer
	EventError       EventType = "error"       // the run stopped early
)

// Event is the union of everything the agent reports. Fields not relevant to a
// given Type are left zero, which keeps the JSON sent to the browser small.
type Event struct {
	Type    EventType `json:"type"`
	Step    int       `json:"step,omitempty"`
	Text    string    `json:"text,omitempty"`  // thought, final answer, or error message
	Tool    string    `json:"tool,omitempty"`  // action: tool name
	Input   string    `json:"input,omitempty"` // action: tool input
	Ok      bool      `json:"ok,omitempty"`    // observation: tool succeeded
	Elapsed int64     `json:"elapsedMs,omitempty"`
	// System is the system instruction. It is identical on every turn, so it
	// rides along once, on the start event, rather than with each trace.
	System string `json:"system,omitempty"`
	// Prompt is the user half of the request that produced a trace event: the
	// question plus the scratchpad as it stood at that step.
	Prompt string `json:"prompt,omitempty"`
	// PromptBytes is the size of the whole request, system included, so the
	// scratchpad can be watched growing from step to step.
	PromptBytes int `json:"promptBytes,omitempty"`
	// Wire is the raw HTTP exchange behind a trace event, when the model was
	// reached over HTTP. A scripted or offline Model leaves it nil.
	Wire *Wire `json:"wire,omitempty"`
}

// Emitter receives events as they happen. It must not block for long: the agent
// calls it inline.
type Emitter func(Event)

func (e Emitter) emit(ev Event) {
	if e != nil {
		e(ev)
	}
}

func since(t time.Time) int64 { return time.Since(t).Milliseconds() }
