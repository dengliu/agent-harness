package react

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Tool is one action the agent can take.
//
// Run receives whatever the model wrote after "Action Input:", after
// parseActionInput has normalised it — a plain string, because that is all the
// text protocol can carry. There is no schema and no type checking: the tool
// validates its own input, and says so in words when it cannot use it.
//
// A tool should return an error only for genuine failures. The agent turns the
// error into an Observation and lets the model recover, so the error text
// becomes part of the next prompt: write it for the model to read, not for a
// log file.
type Tool interface {
	Name() string
	Description() string
	Run(ctx context.Context, input string) (string, error)
}

// Toolbox is the set of tools available to an agent, keyed by name.
type Toolbox struct {
	tools map[string]Tool
}

func NewToolbox(tools ...Tool) *Toolbox {
	tb := &Toolbox{tools: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		tb.Add(t)
	}
	return tb
}

func (tb *Toolbox) Add(t Tool) { tb.tools[t.Name()] = t }

func (tb *Toolbox) Get(name string) (Tool, bool) {
	t, ok := tb.tools[strings.TrimSpace(name)]
	return t, ok
}

// List returns the tools in a stable order so the prompt is byte-identical
// between runs, which keeps the provider's prompt caching effective.
func (tb *Toolbox) List() []Tool {
	out := make([]Tool, 0, len(tb.tools))
	for _, t := range tb.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

func (tb *Toolbox) Names() []string {
	var names []string
	for _, t := range tb.List() {
		names = append(names, t.Name())
	}
	return names
}

// describe renders the tool catalogue that goes into the system prompt.
//
// This is the entire interface between the model and the toolbox: three lines
// of prose. Nothing is registered with the provider, nothing is validated for
// us. Whether the model picks the right tool, and gives it something usable,
// depends on how well these sentences are written — which is why the
// descriptions below name the argument format and give an example.
func (tb *Toolbox) describe() string {
	var b strings.Builder
	for _, t := range tb.List() {
		fmt.Fprintf(&b, "- %s: %s\n", t.Name(), t.Description())
	}
	return b.String()
}

// funcTool adapts a plain function into a Tool.
type funcTool struct {
	name string
	desc string
	fn   func(context.Context, string) (string, error)
}

func (f funcTool) Name() string        { return f.name }
func (f funcTool) Description() string { return f.desc }
func (f funcTool) Run(ctx context.Context, in string) (string, error) {
	return f.fn(ctx, in)
}

// NewTool wraps fn as a Tool. Adding a tool to the demo is this call plus a
// line in DefaultToolbox — the harness needs nothing else, because the model
// learns about it from Description alone.
func NewTool(name, desc string, fn func(context.Context, string) (string, error)) Tool {
	return funcTool{name: name, desc: desc, fn: fn}
}
