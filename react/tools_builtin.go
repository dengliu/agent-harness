package react

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/expr-lang/expr"
)

// DefaultToolbox is the demo tool set: arithmetic, a clock and an encyclopedia.
// Together they cover the three reasons a ReAct loop reaches for a tool —
// compute something, look up something ambient, look up something external.
func DefaultToolbox() *Toolbox {
	return NewToolbox(Calculator(), Clock(), Wikipedia(http.DefaultClient))
}

// Calculator evaluates an arithmetic expression with expr-lang. The model is
// far better at deciding *what* to compute than at computing it, so we hand the
// arithmetic to a real evaluator.
func Calculator() Tool {
	// expr hands integer literals through as int, so every function takes any
	// and coerces: without this, "sqrt(pow(3,2) + pow(4,2))" fails on types
	// rather than returning 5.
	f1 := func(fn func(float64) float64) func(any) float64 {
		return func(x any) float64 { return fn(toFloat(x)) }
	}
	env := map[string]any{
		"pi":    math.Pi,
		"e":     math.E,
		"sqrt":  f1(math.Sqrt),
		"abs":   f1(math.Abs),
		"log":   f1(math.Log),
		"log10": f1(math.Log10),
		"exp":   f1(math.Exp),
		"sin":   f1(math.Sin),
		"cos":   f1(math.Cos),
		"tan":   f1(math.Tan),
		"round": f1(math.Round),
		"floor": f1(math.Floor),
		"ceil":  f1(math.Ceil),
		"pow":   func(x, y any) float64 { return math.Pow(toFloat(x), toFloat(y)) },
	}
	// The description is the model's only documentation for this tool: it has
	// to say what the input looks like, because nothing validates it.
	return NewTool("calculator",
		`Evaluate an arithmetic expression. Input is the expression alone, e.g. "(1200 * 1.08) / 12" or "sqrt(pow(3,2) + pow(4,2))". Supports + - * / % ** and sqrt, pow, abs, log, log10, exp, sin, cos, tan, round, floor, ceil, pi, e.`,
		func(_ context.Context, in string) (string, error) {
			in = strings.TrimSpace(strings.Trim(strings.TrimSpace(in), "`"))
			if in == "" {
				return "", fmt.Errorf("no expression given")
			}
			out, err := expr.Eval(in, env)
			if err != nil {
				// This message goes back to the model as an Observation, so it
				// explains the problem rather than dumping a Go error.
				return "", fmt.Errorf("cannot evaluate %q: %v", in, firstLine(err.Error()))
			}
			return formatNumber(out), nil
		})
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case uint:
		return float64(n)
	case uint64:
		return float64(n)
	}
	f, err := strconv.ParseFloat(fmt.Sprintf("%v", v), 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

// formatNumber prints a result the way a person would write it: 5 rather than
// 5.000000, but 2.5 kept intact.
func formatNumber(v any) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return fmt.Sprintf("%v", v)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Clock reports the current time, optionally in a named IANA zone. Without it
// the model has no idea what "today" means — its only notion of the date comes
// from its training data.
func Clock() Tool {
	return NewTool("clock",
		`Get the current date and time. Input is an optional IANA timezone such as "Asia/Tokyo" or "UTC"; leave it empty for the server's local time.`,
		func(_ context.Context, in string) (string, error) {
			in = strings.TrimSpace(in)
			loc := time.Local
			if in != "" {
				l, err := time.LoadLocation(in)
				if err != nil {
					return "", fmt.Errorf("unknown timezone %q; use an IANA name like Europe/Paris", in)
				}
				loc = l
			}
			return time.Now().In(loc).Format("Monday, 2 January 2006, 15:04:05 MST (-07:00)"), nil
		})
}

// Wikipedia searches English Wikipedia and returns the lead paragraph of the
// best match, plus the titles of the runners-up so the model can try again with
// a more specific query.
func Wikipedia(hc *http.Client) Tool {
	return NewTool("wikipedia",
		`Look something up in English Wikipedia. Input is a search phrase such as "Ada Lovelace" or "Kyoto Protocol". Returns the article's lead section. Use it for facts about people, places, events and concepts.`,
		func(ctx context.Context, in string) (string, error) {
			q := strings.TrimSpace(in)
			if q == "" {
				return "", fmt.Errorf("no search phrase given")
			}
			titles, err := wikiSearch(ctx, hc, q)
			if err != nil {
				return "", err
			}
			if len(titles) == 0 {
				return fmt.Sprintf("No Wikipedia article matches %q. Try a different phrasing.", q), nil
			}
			summary, err := wikiSummary(ctx, hc, titles[0])
			if err != nil {
				return "", err
			}
			// The runner-up titles let the model retry with a narrower query
			// instead of giving up — a cheap way to make one tool self-correcting.
			out := fmt.Sprintf("%s — %s", titles[0], summary)
			if len(titles) > 1 {
				out += "\n(other matches: " + strings.Join(titles[1:], ", ") + ")"
			}
			return out, nil
		})
}

const wikiUA = "go-react-agent-demo/1.0 (https://example.invalid; educational demo)"

func wikiGet(ctx context.Context, hc *http.Client, u string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", wikiUA)
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("wikipedia request failed: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("wikipedia response unreadable: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("wikipedia returned %s", resp.Status)
	}
	return json.Unmarshal(body, into)
}

func wikiSearch(ctx context.Context, hc *http.Client, q string) ([]string, error) {
	u := "https://en.wikipedia.org/w/api.php?action=query&format=json&list=search&srlimit=3&srsearch=" + url.QueryEscape(q)
	var out struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := wikiGet(ctx, hc, u, &out); err != nil {
		return nil, err
	}
	var titles []string
	for _, s := range out.Query.Search {
		titles = append(titles, s.Title)
	}
	return titles, nil
}

// wikiSummary returns the article's lead section. It uses action=query rather
// than the REST summary endpoint because REST strips the parenthetical that
// opens most biographies — "(May 19, 1930 – July 2, 2016)" — and those dates
// are exactly what an agent asks Wikipedia for. Without them the agent cannot
// finish, and either burns its step budget rephrasing the query or falls back
// on what it thinks it remembers.
func wikiSummary(ctx context.Context, hc *http.Client, title string) (string, error) {
	u := "https://en.wikipedia.org/w/api.php?action=query&format=json&prop=extracts" +
		"&exintro=1&explaintext=1&redirects=1&titles=" + url.QueryEscape(title)
	var out struct {
		Query struct {
			Pages map[string]struct {
				Extract string `json:"extract"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := wikiGet(ctx, hc, u, &out); err != nil {
		return "", err
	}
	for _, page := range out.Query.Pages {
		if strings.TrimSpace(page.Extract) != "" {
			return strings.TrimSpace(page.Extract), nil
		}
	}
	return "(the article has no lead section)", nil
}
