// Command react-agent is a demo of the ReAct pattern — a language model that
// answers a question by alternating between reasoning and calling tools:
//
//	Thought → Action → Observation → Thought → … → Final Answer
//
// The agent itself is in package react; this file wires it to a terminal and
// to an HTTP server that streams the reasoning trace to the Next.js UI in web/.
//
//	export OPEN_API_KEY=...
//
//	go run . -q "How old would Ada Lovelace be today?"   # one-shot, in the terminal
//	go run .                                             # serve the API on :8080
//
// With the server running, start the UI with `cd web && npm run dev` and open
// http://localhost:3000.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dengliu/agent-harness/react"
	"github.com/dengliu/agent-harness/server"
)

func main() {
	var (
		question = flag.String("q", "", "answer this question in the terminal and exit; omit to run the server")
		addr     = flag.String("addr", ":8080", "address for the HTTP server")
		webDir   = flag.String("web", server.WebDir(), "directory of static UI files to serve, if it exists")
		maxSteps = flag.Int("max-steps", 8, "maximum Thought/Action rounds per question")
		verbose  = flag.Bool("v", false, "print the raw prompts and replies")
	)
	flag.Parse()

	// Ctrl-C cancels an in-flight run and drains the server.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	model, err := react.NewOpenAI(ctx)
	if err != nil {
		log.Fatalf("%v\n\nSet OPEN_API_KEY to a key from https://platform.openai.com/api-keys", err)
	}

	var llm react.Model = model
	if *verbose {
		llm = tracingModel{llm}
	}
	agent := react.New(llm, react.DefaultToolbox(), react.Config{MaxSteps: *maxSteps})

	if strings.TrimSpace(*question) != "" {
		if err := runOnce(ctx, agent, *question); err != nil {
			log.Fatalf("agent: %v", err)
		}
		return
	}
	if err := serve(ctx, agent, model.Name(), *addr, *webDir); err != nil {
		log.Fatal(err)
	}
}

// runOnce answers a single question, printing the trace as it unfolds.
func runOnce(ctx context.Context, agent *react.Agent, question string) error {
	fmt.Printf("%s %s\n\n", bold("Question:"), question)
	_, err := agent.Run(ctx, question, printEvent)
	return err
}

func serve(ctx context.Context, agent *react.Agent, model, addr, webDir string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           server.New(agent, model, webDir).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: an SSE run is a long-lived response.
	}
	errc := make(chan error, 1)
	go func() {
		log.Printf("react agent (%s) listening on http://localhost%s", model, addr)
		log.Printf("try: curl -N localhost%s/api/run -d '{\"question\":\"what time is it in Tokyo?\"}'", addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("listen: %w", err)
		}
		return nil
	case <-ctx.Done():
		log.Println("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	}
}

// printEvent renders one trace event for a terminal.
func printEvent(e react.Event) {
	switch e.Type {
	case react.EventThought:
		fmt.Printf("%s %s\n", cyan(fmt.Sprintf("[%d] Thought", e.Step)), e.Text)
	case react.EventAction:
		fmt.Printf("%s %s(%s)\n", yellow(fmt.Sprintf("[%d] Action ", e.Step)), bold(e.Tool), e.Input)
	case react.EventObservation:
		mark := green("Observ.")
		if !e.Ok {
			mark = red("Observ.")
		}
		fmt.Printf("%s %s %s\n\n", dim(fmt.Sprintf("[%d]", e.Step)), mark, indent(e.Text))
	case react.EventFinal:
		fmt.Printf("%s %s\n%s\n", green("Answer:"), dim(fmt.Sprintf("(%d steps, %.1fs)", e.Step, float64(e.Elapsed)/1000)), e.Text)
	case react.EventError:
		fmt.Printf("%s %s\n", red("Error:"), e.Text)
	}
}

// indent keeps multi-line tool output aligned under its label.
func indent(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 600 {
		s = s[:600] + "…"
	}
	return strings.ReplaceAll(s, "\n", "\n                ")
}

// tracingModel logs every prompt and reply, for -v.
type tracingModel struct{ react.Model }

func (t tracingModel) Generate(ctx context.Context, system string, turns []react.Turn, stop []string) (string, error) {
	fmt.Printf("%s\n%s\n", dim("--- request ---"), dim(react.FormatTurns(turns)))
	out, err := t.Model.Generate(ctx, system, turns, stop)
	fmt.Printf("%s\n%s\n%s\n", dim("--- reply ---"), dim(strings.TrimSpace(out)), dim("--------------"))
	return out, err
}

// Minimal ANSI helpers; colour is suppressed when stdout is not a terminal or
// NO_COLOR is set (https://no-color.org).
var useColor = func() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}()

func paint(code, s string) string {
	if !useColor {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func bold(s string) string   { return paint("1", s) }
func dim(s string) string    { return paint("2", s) }
func red(s string) string    { return paint("31", s) }
func green(s string) string  { return paint("32", s) }
func yellow(s string) string { return paint("33", s) }
func cyan(s string) string   { return paint("36", s) }
