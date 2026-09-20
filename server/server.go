// Package server exposes a react.Agent over HTTP. The reasoning trace is
// streamed to the browser as Server-Sent Events, so the web UI can show each
// Thought, Action and Observation as it happens instead of waiting for the
// final answer — which, for a multi-step agent, can be a long time.
package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dengliu/agent-harness/react"
)

type Server struct {
	agent  *react.Agent
	model  string
	webDir string // optional directory of static files (a Next.js export)
}

func New(agent *react.Agent, model, webDir string) *Server {
	return &Server{agent: agent, model: model, webDir: webDir}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/tools", s.handleTools)
	mux.HandleFunc("POST /api/run", s.handleRun)
	mux.HandleFunc("OPTIONS /api/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })

	if s.webDir != "" {
		if _, err := os.Stat(s.webDir); err == nil {
			mux.Handle("/", http.FileServer(http.Dir(s.webDir)))
			log.Printf("serving static UI from %s", s.webDir)
		} else {
			log.Printf("static UI directory %s not found; API only", s.webDir)
		}
	}
	return cors(mux)
}

// cors keeps `next dev` on :3000 able to call the API on :8080 directly, which
// is handy when you want to run the UI without the dev-server proxy.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "model": s.model})
}

func (s *Server) handleTools(w http.ResponseWriter, _ *http.Request) {
	type toolInfo struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	var out []toolInfo
	for _, t := range s.agent.Tools().List() {
		out = append(out, toolInfo{Name: t.Name(), Description: t.Description()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"model": s.model, "tools": out})
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question string `json:"question"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if strings.TrimSpace(req.Question) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "question is required"})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // don't let a proxy buffer the stream
	w.WriteHeader(http.StatusOK)

	// The agent emits inline from this goroutine, but a heartbeat also writes,
	// so guard the ResponseWriter.
	var mu sync.Mutex
	send := func(ev react.Event) {
		mu.Lock()
		defer mu.Unlock()
		data, err := json.Marshal(ev)
		if err != nil {
			return
		}
		_, _ = w.Write([]byte("event: " + string(ev.Type) + "\ndata: " + string(data) + "\n\n"))
		flusher.Flush()
	}
	comment := func(text string) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write([]byte(": " + text + "\n\n"))
		flusher.Flush()
	}

	ctx := r.Context()
	done := make(chan struct{})
	defer close(done)
	go func() { // keep intermediaries from closing an idle-looking connection
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				comment("keepalive")
			}
		}
	}()

	log.Printf("run: %s", truncate(req.Question, 120))
	if _, err := s.agent.Run(ctx, req.Question, send); err != nil && ctx.Err() == nil {
		log.Printf("run failed: %v", err)
	}
	comment("done")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// WebDir resolves the default static-UI directory next to the binary's working
// directory, i.e. the Next.js export at web/out.
func WebDir() string { return filepath.Join("web", "out") }
