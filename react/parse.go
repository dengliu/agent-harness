package react

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// This file is the other half of the harness: what we read back.
//
// The provider returns one string. Everything the agent knows about what the
// model "decided" comes from reading that string — there is no structured
// field to consult. So the parser is where the protocol is actually enforced,
// and its failure modes are the agent's failure modes.

// step is one parsed block: either a tool call or a final answer, never both.
type step struct {
	Thought string // the reasoning line, shown in the UI
	Tool    string // the tool named on the Action line
	Input   string // the Action Input, normalised by parseActionInput
	Final   string // set when the model answered instead of acting
	Raw     string // the untouched reply, kept for the trace
}

// These match the protocol from systemPromptTmpl. They are deliberately loose:
// (?is) makes them case-insensitive and lets . cross newlines, because models
// capitalise and wrap unpredictably.
var (
	// A Thought runs until the next keyword or the end of the reply.
	reThought = regexp.MustCompile(`(?is)Thought:\s*(.*?)(?:\n\s*(?:Action|Final Answer)\s*:|\z)`)
	reAction  = regexp.MustCompile(`(?is)\bAction:\s*(.*?)\n`)
	reInput   = regexp.MustCompile(`(?is)Action Input:\s*(.*?)(?:\n\s*(?:Observation|Thought|Action)\s*:|\z)`)
	reFinal   = regexp.MustCompile(`(?is)Final Answer:\s*(.*)\z`)
)

// parseStep reads one model block.
//
// It is forgiving on purpose. Every reply costs a round trip, so bouncing a
// slightly-off reply back to the model for correction is the most expensive
// thing the loop can do. Each repair below stands for a way a real model
// actually drifted during development:
//
//   - wrapping the block in ``` fences;
//   - writing its own Observation and carrying on;
//   - answering in bare prose with no keywords at all.
//
// The order matters: Final Answer is checked before Action, so a reply that
// contains both is treated as an answer. (That choice has a cost — if the model
// announces a lookup and answers in the same block, the tool never runs and the
// answer comes from memory. Preferring the Action would trade that for an extra
// step. It is a real fork in the design, not an oversight.)
func parseStep(raw string) (step, error) {
	s := step{Raw: raw}
	text := stripFences(raw)

	// The single most important line in this file.
	//
	// The prompt tells the model never to write an Observation, and a stop
	// sequence would enforce that at the API — but gpt-5 rejects the `stop`
	// parameter outright, so nothing stops a model that decides to write one
	// and then reason over it. Cutting everything from its first self-written
	// Observation onwards is what keeps invented facts out of the loop: the
	// text is discarded here and never reaches the scratchpad, so it can never
	// be re-sent as though it had happened.
	if i := indexKeyword(text, "Observation:"); i >= 0 {
		text = text[:i]
	}

	// A preamble before the block is common: the model says what it is about
	// to do in ordinary prose, then writes the block properly. Both start with
	// "Thought:", and without this the reply is read as one run-on thought.
	// The last Thought before the Action is the one it meant.
	text = trimPreamble(text)

	if m := reThought.FindStringSubmatch(text); m != nil {
		s.Thought = clean(m[1])
	}
	if m := reFinal.FindStringSubmatch(text); m != nil {
		s.Final = clean(m[1])
		return s, nil
	}
	if m := reAction.FindStringSubmatch(text + "\n"); m != nil {
		s.Tool = clean(m[1])
		if mi := reInput.FindStringSubmatch(text); mi != nil {
			s.Input = parseActionInput(clean(mi[1]))
		}
		if s.Tool != "" {
			return s, nil
		}
	}

	// No keywords at all: the model ignored the format and simply answered.
	// Treating that as the answer is better than failing the run over syntax.
	if body := clean(text); body != "" && s.Thought == "" {
		s.Final = body
		return s, nil
	}

	// A Thought and nothing else. The model stopped mid-block; the loop will
	// hand it back a correction and let it try again.
	if s.Thought != "" {
		return s, fmt.Errorf("model produced a Thought but no Action and no Final Answer")
	}
	return s, fmt.Errorf("model reply matched neither an Action nor a Final Answer")
}

// parseActionInput normalises the many shapes a model uses for one argument.
//
// This function is the price of the text protocol. The prompt asks for a bare
// value on one line; models variously send a bare value, a quoted string, a
// JSON string, or a JSON object wrapping the value under whatever key seems
// natural. A provider's native tool interface would have validated this against
// a schema; here, we guess — carefully, and in a fixed order.
func parseActionInput(in string) string {
	in = strings.TrimSpace(strings.Trim(strings.TrimSpace(in), "`"))
	if in == "" {
		return ""
	}
	switch in[0] {
	case '"':
		// A JSON string: "Asia/Tokyo" → Asia/Tokyo.
		var s string
		if json.Unmarshal([]byte(in), &s) == nil {
			return strings.TrimSpace(s)
		}
	case '{':
		// A JSON object: {"query": "Ada Lovelace"} → Ada Lovelace.
		var m map[string]any
		if json.Unmarshal([]byte(in), &m) == nil {
			// Try the keys a model plausibly picks, in order.
			for _, k := range []string{"input", "query", "expression", "q", "text", "value", "timezone", "tz"} {
				if v, ok := m[k]; ok {
					return strings.TrimSpace(fmt.Sprintf("%v", v))
				}
			}
			// One unknown key: it can only have meant that.
			if len(m) == 1 {
				for _, v := range m {
					return strings.TrimSpace(fmt.Sprintf("%v", v))
				}
			}
		}
	}
	return in
}

// indexKeyword finds a keyword at the start of a line, case-insensitively.
// Line-anchored, so the word "Observation:" quoted inside a sentence does not
// truncate a perfectly good reply.
func indexKeyword(s, kw string) int {
	low, lkw := strings.ToLower(s), strings.ToLower(kw)
	for i := 0; ; {
		j := strings.Index(low[i:], lkw)
		if j < 0 {
			return -1
		}
		at := i + j
		if at == 0 || low[at-1] == '\n' {
			return at
		}
		i = at + len(lkw)
	}
}

// stripFences removes a ```…``` wrapper, which models add when they decide the
// block looks like code.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}

func clean(s string) string {
	return strings.TrimSpace(strings.Trim(strings.TrimSpace(s), "`"))
}

// trimPreamble drops everything before the last "Thought:" that still has an
// Action or a Final Answer after it. A reply with a single Thought — the normal
// case — is returned untouched.
func trimPreamble(text string) string {
	last := -1
	for i := 0; ; {
		j := indexKeyword(text[i:], "Thought:")
		if j < 0 {
			break
		}
		at := i + j
		// Only treat it as the start of a block if the block is complete.
		rest := text[at:]
		if indexKeyword(rest, "Action:") < 0 && indexKeyword(rest, "Final Answer:") < 0 {
			break
		}
		last = at
		i = at + len("Thought:")
	}
	if last > 0 {
		return text[last:]
	}
	return text
}
