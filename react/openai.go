package react

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"
)

// Model is the agent's entire view of an LLM: a system instruction, the
// conversation so far, and a stop sequence in; one string out.
//
// One method is all the loop needs, and keeping it that small is what makes the
// rest testable: agent_test.go swaps in a scriptedModel that replays canned
// strings, so the loop, the parser and the recovery paths are all exercised
// with no API key and no network.
//
// Note what is *not* here: no tools parameter. The provider is never told that
// tools exist. It returns text; this package decides what the text means.
type Model interface {
	Generate(ctx context.Context, system string, turns []Turn, stop []string) (string, error)
}

// DefaultModel is the model used when OPENAI_MODEL is unset.
const DefaultModel = "gpt-5"

// maxOutputTokens bounds one reply. Reasoning tokens come from the same budget,
// so this is deliberately larger than a Thought/Action block needs.
const maxOutputTokens = 4096

// OpenAI implements Model against the Responses API — used here as a plain
// text-completion endpoint, since the harness supplies its own tool protocol.
type OpenAI struct {
	client openai.Client
	model  string
}

// NewOpenAI builds a client from OPEN_API_KEY, falling back to the more
// conventional OPENAI_API_KEY. The model name comes from OPENAI_MODEL, or
// DefaultModel.
func NewOpenAI(_ context.Context) (*OpenAI, error) {
	key := strings.TrimSpace(os.Getenv("OPEN_API_KEY"))
	if key == "" {
		key = strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	}
	if key == "" {
		return nil, fmt.Errorf("OPEN_API_KEY is not set")
	}
	model := strings.TrimSpace(os.Getenv("OPENAI_MODEL"))
	if model == "" {
		model = DefaultModel
	}
	client := openai.NewClient(
		option.WithAPIKey(key),
		// Route the SDK through a transport that keeps the raw bodies, so the
		// UI can show what actually went over the wire rather than a
		// plausible-looking rebuild of it. See wire.go.
		option.WithHTTPClient(&http.Client{
			Timeout:   2 * time.Minute,
			Transport: &captureTransport{base: http.DefaultTransport, secret: key},
		}),
	)
	return &OpenAI{client: client, model: model}, nil
}

func (o *OpenAI) Name() string { return o.model }

// Generate sends one request and returns the model's text.
//
// The stop argument is accepted and ignored, and that is worth knowing rather
// than hiding: gpt-5 rejects the `stop` parameter outright
// ("Unsupported parameter: 'stop' is not supported with this model"), so the
// harness cannot make the model halt before writing an Observation. parseStep
// truncates one instead. The parameter stays in the interface because it
// expresses the agent's intent, and a Model for a provider that supports stop
// sequences should honour it.
//
// Temperature is not set either: GPT-5 accepts only the default.
func (o *OpenAI) Generate(ctx context.Context, system string, turns []Turn, _ []string) (string, error) {
	resp, err := o.client.Responses.New(ctx, responses.ResponseNewParams{
		Model:           shared.ChatModel(o.model),
		Instructions:    openai.String(system),
		Input:           responses.ResponseNewParamsInputUnion{OfInputItemList: inputItems(turns)},
		MaxOutputTokens: openai.Int(maxOutputTokens),
		// Low effort, and no reasoning summary: the model's own "Thought:" line
		// is the visible reasoning in this design, so paying for a second,
		// provider-generated one would be redundant.
		Reasoning: shared.ReasoningParam{Effort: shared.ReasoningEffortLow},
	})
	if err != nil {
		return "", fmt.Errorf("openai: %w", err)
	}

	// Join the message items ourselves rather than using resp.OutputText(),
	// which concatenates them with no separator. GPT-5 often emits a short
	// preamble message before the block it was asked for, and without a
	// newline the two run together into "…Asia/Tokyo.Thought: I need…".
	var parts []string
	for _, item := range resp.Output {
		if item.Type != "message" {
			continue
		}
		for _, c := range item.AsMessage().Content {
			if text := strings.TrimSpace(c.Text); text != "" {
				parts = append(parts, text)
			}
		}
	}
	out := strings.TrimSpace(strings.Join(parts, "\n"))
	if out == "" {
		if resp.Status == "incomplete" {
			return "", fmt.Errorf("openai stopped early (%s) with no text; the whole %d-token budget went to reasoning",
				resp.IncompleteDetails.Reason, maxOutputTokens)
		}
		return "", fmt.Errorf("openai returned an empty response")
	}
	return out, nil
}

// inputItems maps the conversation onto the API's input items. Each Turn
// becomes one message: ours as `user`, the model's own blocks as `assistant`.
func inputItems(turns []Turn) []responses.ResponseInputItemUnionParam {
	out := make([]responses.ResponseInputItemUnionParam, 0, len(turns))
	for _, t := range turns {
		role := responses.EasyInputMessageRoleUser
		if t.Role == TurnModel {
			role = responses.EasyInputMessageRoleAssistant
		}
		out = append(out, responses.ResponseInputItemParamOfMessage(t.Text, role))
	}
	return out
}
