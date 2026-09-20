package react

import (
	"context"
	"strings"
	"testing"
)

// The whole loop is testable without an API key because Model is one method.
// scriptedModel replays canned replies and records every conversation it was
// sent, so a test can assert both what the agent did and what it would have
// shown the model next.
type scriptedModel struct {
	replies  []string
	requests [][]Turn
}

func (m *scriptedModel) Generate(_ context.Context, _ string, turns []Turn, _ []string) (string, error) {
	m.requests = append(m.requests, turns)
	if len(m.replies) == 0 {
		return "", context.Canceled
	}
	r := m.replies[0]
	m.replies = m.replies[1:]
	return r, nil
}

func (m *scriptedModel) transcript(n int) string {
	if n >= len(m.requests) {
		return ""
	}
	return FormatTurns(m.requests[n])
}

func testAgent(replies ...string) (*Agent, *scriptedModel) {
	m := &scriptedModel{replies: replies}
	return New(m, NewToolbox(Calculator(), Clock()), Config{MaxSteps: 5}), m
}

// The happy path: the model asks for a tool, the loop runs it, and the result
// is in front of the model on the next call.
func TestRunUsesToolThenAnswers(t *testing.T) {
	a, m := testAgent(
		"Thought: I should compute this.\nAction: calculator\nAction Input: 6 * 7\n",
		"Thought: I now know the final answer\nFinal Answer: It is 42.",
	)
	got, err := a.Run(context.Background(), "What is 6 times 7?", nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got != "It is 42." {
		t.Errorf("answer = %q, want %q", got, "It is 42.")
	}
	if len(m.requests) != 2 {
		t.Fatalf("model called %d times, want 2", len(m.requests))
	}
	if !strings.Contains(m.transcript(1), "Observation: 42") {
		t.Errorf("second request lacks the observation:\n%s", m.transcript(1))
	}
}

// The conversation is a real exchange of turns, not one concatenated block: the
// agent speaks as the user and the model's own blocks come back in the model
// role. Sending its words back as the user's measurably increases format drift.
func TestConversationAlternatesRoles(t *testing.T) {
	a, m := testAgent(
		"Thought: first\nAction: calculator\nAction Input: 1+1\n",
		"Thought: second\nAction: calculator\nAction Input: 2+2\n",
		"Final Answer: done",
	)
	if _, err := a.Run(context.Background(), "add things", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}

	third := m.requests[2]
	want := []struct{ role, contains string }{
		{TurnUser, "Question: add things"},
		{TurnModel, "Action: calculator"},
		{TurnUser, "Observation: 2"},
		{TurnModel, "Action Input: 2+2"},
		{TurnUser, "Observation: 4"},
	}
	if len(third) != len(want) {
		t.Fatalf("third request has %d turns, want %d:\n%s", len(third), len(want), m.transcript(2))
	}
	for i, w := range want {
		if third[i].Role != w.role {
			t.Errorf("turn %d role = %q, want %q", i, third[i].Role, w.role)
		}
		if !strings.Contains(third[i].Text, w.contains) {
			t.Errorf("turn %d = %q, want it to contain %q", i, third[i].Text, w.contains)
		}
	}
}

// A model turn is the block rebuilt from the parse, never the raw reply. If the
// model invents an observation, that invention must not come back to it looking
// like something that happened.
func TestModelTurnIsCanonical(t *testing.T) {
	a, m := testAgent(
		"```\nThought: fenced\nAction: calculator\nAction Input: 7*6\nObservation: 999\n```",
		"Final Answer: 42",
	)
	if _, err := a.Run(context.Background(), "q", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reply := m.requests[1][1]
	if reply.Role != TurnModel {
		t.Fatalf("turn 1 role = %q, want %q", reply.Role, TurnModel)
	}
	if want := "Thought: fenced\nAction: calculator\nAction Input: 7*6"; reply.Text != want {
		t.Errorf("model turn = %q, want %q", reply.Text, want)
	}
	if strings.Contains(m.transcript(1), "999") {
		t.Errorf("the hallucinated observation was replayed:\n%s", m.transcript(1))
	}
}

func TestEventsDescribeTheTrace(t *testing.T) {
	a, _ := testAgent(
		"Thought: compute\nAction: calculator\nAction Input: 1+1\n",
		"Final Answer: 2",
	)
	var types []EventType
	var events []Event
	if _, err := a.Run(context.Background(), "1+1?", func(e Event) {
		types = append(types, e.Type)
		events = append(events, e)
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := []EventType{
		EventStart,
		EventTrace, EventThought, EventAction, EventObservation,
		EventTrace, EventFinal,
	}
	if len(types) != len(want) {
		t.Fatalf("events = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events = %v, want %v", types, want)
		}
	}
	trace := events[1]
	if !strings.Contains(trace.Text, "Action: calculator") {
		t.Errorf("trace text = %q, want the raw reply", trace.Text)
	}
	if !strings.Contains(trace.Prompt, "Question: 1+1?") {
		t.Errorf("trace prompt = %q, want the question", trace.Prompt)
	}
	if !strings.Contains(events[0].System, "You are a ReAct agent") {
		t.Errorf("start event should carry the system prompt, got %q", events[0].System)
	}
	// The scratchpad grows, so the second call's request is the larger one.
	if second := events[5]; second.PromptBytes <= trace.PromptBytes {
		t.Errorf("request did not grow: %d then %d bytes", trace.PromptBytes, second.PromptBytes)
	}
}

// Recovery: none of these end the run. Each puts an explanation in front of the
// model and gives it another turn.
func TestUnknownToolBecomesAnObservation(t *testing.T) {
	a, m := testAgent(
		"Thought: search\nAction: google\nAction Input: cats\n",
		"Final Answer: no google here",
	)
	if _, err := a.Run(context.Background(), "q", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(m.transcript(1), `no tool called "google"`) {
		t.Errorf("model was not told the tool is missing:\n%s", m.transcript(1))
	}
}

func TestToolErrorBecomesAnObservation(t *testing.T) {
	a, m := testAgent(
		"Thought: compute\nAction: calculator\nAction Input: 1 +* 2\n",
		"Final Answer: recovered",
	)
	if _, err := a.Run(context.Background(), "q", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(m.transcript(1), "calculator failed:") {
		t.Errorf("model was not told the tool failed:\n%s", m.transcript(1))
	}
}

func TestMalformedReplyIsEchoedWithTheCorrection(t *testing.T) {
	a, m := testAgent(
		"Thought: I am thinking about it and will stop there",
		"Final Answer: recovered",
	)
	if _, err := a.Run(context.Background(), "q", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	second := m.requests[1]
	if len(second) < 3 {
		t.Fatalf("expected the malformed round in the transcript:\n%s", m.transcript(1))
	}
	if second[1].Role != TurnModel || !strings.Contains(second[1].Text, "I am thinking about it") {
		t.Errorf("model turn = %+v, want its own text in the model role", second[1])
	}
	if !strings.Contains(second[2].Text, "not in the required format") {
		t.Errorf("user turn = %q, want the correction", second[2].Text)
	}
}

func TestStepLimit(t *testing.T) {
	loop := "Thought: again\nAction: calculator\nAction Input: 1+1\n"
	a, _ := testAgent(loop, loop, loop, loop, loop)
	if _, err := a.Run(context.Background(), "q", nil); err != ErrMaxSteps {
		t.Fatalf("err = %v, want ErrMaxSteps", err)
	}
}

// Each row is a way a real model drifted from the format during development.
// The parser repairs what it can, because a rejected reply costs a round trip.
func TestParseStep(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		tool, input   string
		final         string
		wantThoughtIs string
	}{
		{
			name: "plain action", raw: "Thought: need math\nAction: calculator\nAction Input: 2+2",
			tool: "calculator", input: "2+2", wantThoughtIs: "need math",
		},
		{
			name: "json object input", raw: "Thought: t\nAction: wikipedia\nAction Input: {\"query\": \"Ada Lovelace\"}",
			tool: "wikipedia", input: "Ada Lovelace", wantThoughtIs: "t",
		},
		{
			name: "quoted input", raw: "Thought: t\nAction: clock\nAction Input: \"Asia/Tokyo\"",
			tool: "clock", input: "Asia/Tokyo", wantThoughtIs: "t",
		},
		{
			name: "code fenced", raw: "```\nThought: t\nAction: calculator\nAction Input: 3*3\n```",
			tool: "calculator", input: "3*3", wantThoughtIs: "t",
		},
		{
			// The load-bearing one: gpt-5 rejects stop sequences, so this is
			// the only thing keeping invented facts out of the scratchpad.
			name: "hallucinated observation is dropped",
			raw:  "Thought: t\nAction: calculator\nAction Input: 5*5\nObservation: 999\nFinal Answer: 999",
			tool: "calculator", input: "5*5", wantThoughtIs: "t",
		},
		{
			// GPT-5 often says what it is about to do, then does it. Both lines
			// begin with "Thought:"; the second one is the real block.
			name: "preamble before the block",
			raw:  "Thought: I'll check the clock for Tokyo.\nThought: I need the time in Tokyo\nAction: clock\nAction Input: Asia/Tokyo",
			tool: "clock", input: "Asia/Tokyo", wantThoughtIs: "I need the time in Tokyo",
		},
		{
			name: "final answer", raw: "Thought: done\nFinal Answer: The answer is 4.",
			final: "The answer is 4.", wantThoughtIs: "done",
		},
		{
			name: "bare prose is treated as the answer", raw: "Paris is the capital of France.",
			final: "Paris is the capital of France.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := parseStep(tc.raw)
			if err != nil {
				t.Fatalf("parseStep: %v", err)
			}
			if s.Tool != tc.tool || s.Input != tc.input || s.Final != tc.final {
				t.Errorf("got tool=%q input=%q final=%q; want tool=%q input=%q final=%q",
					s.Tool, s.Input, s.Final, tc.tool, tc.input, tc.final)
			}
			if s.Thought != tc.wantThoughtIs {
				t.Errorf("thought = %q, want %q", s.Thought, tc.wantThoughtIs)
			}
		})
	}
}

// The catalogue in the system prompt is the model's only documentation for the
// tools, so it must actually list them.
func TestSystemPromptListsTools(t *testing.T) {
	got := buildSystemPrompt(DefaultToolbox())
	for _, name := range []string{"calculator", "clock", "wikipedia"} {
		if !strings.Contains(got, "- "+name+":") {
			t.Errorf("system prompt does not describe %q:\n%s", name, got)
		}
	}
}

func TestCalculator(t *testing.T) {
	out, err := Calculator().Run(context.Background(), "sqrt(pow(3,2) + pow(4,2))")
	if err != nil {
		t.Fatalf("calculator: %v", err)
	}
	if out != "5" {
		t.Errorf("got %q, want %q", out, "5")
	}
}

func TestClockRejectsBadZone(t *testing.T) {
	if _, err := Clock().Run(context.Background(), "Mars/Olympus"); err == nil {
		t.Fatal("want an error for an unknown timezone")
	}
}
