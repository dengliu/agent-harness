package react

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Wire is the raw HTTP exchange with the model provider for one step: the JSON
// that went out and the JSON that came back, as bytes, not as a reconstruction.
// The reconstruction is what the rest of this package works with; this is the
// thing it is reconstructed from, which is the only way to answer "but what did
// we actually send?".
type Wire struct {
	URL      string `json:"url,omitempty"`
	Status   string `json:"status,omitempty"`
	Request  string `json:"request,omitempty"`
	Response string `json:"response,omitempty"`
}

type wireKey struct{}

// withWire returns a context that collects the next exchange made through a
// client wrapped in captureTransport, and the Wire it will be collected into.
// A Model that does not use such a client simply leaves it empty.
func withWire(ctx context.Context) (context.Context, *Wire) {
	w := &Wire{}
	return context.WithValue(ctx, wireKey{}, w), w
}

// maxCapture bounds what a single exchange can contribute to an SSE frame. The
// request grows with the scratchpad, so it is the half worth bounding.
const maxCapture = 64 << 10

// captureTransport records request and response bodies for calls whose context
// carries a Wire. It is the only place the raw payloads exist; everything else
// in the package sees parsed text.
type captureTransport struct {
	base http.RoundTripper
	// secret is the API key, redacted wherever it appears. The captured bytes
	// are sent to the browser, so this is not optional.
	secret string
}

func (t *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	w, ok := req.Context().Value(wireKey{}).(*Wire)
	if !ok {
		return t.base.RoundTrip(req)
	}

	// RoundTrip must not modify the request it is given, so read the body from
	// a clone and hand the clone downstream.
	out := req.Clone(req.Context())
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		w.Request = t.scrub(string(body))
		out.Body = io.NopCloser(bytes.NewReader(body))
		out.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}
	w.URL = req.Method + " " + redactURL(req.URL)

	resp, err := t.base.RoundTrip(out)
	if err != nil || resp == nil {
		return resp, err
	}

	// Drain the response so it can be captured, then put it back for the SDK.
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxCapture))
	resp.Body.Close()
	if readErr != nil {
		return resp, readErr
	}
	w.Status = resp.Status
	w.Response = t.scrub(string(body))
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, nil
}

func (t *captureTransport) scrub(s string) string {
	if len(s) > maxCapture {
		s = s[:maxCapture] + "\n… [truncated]"
	}
	if t.secret != "" {
		s = strings.ReplaceAll(s, t.secret, "[redacted]")
	}
	return s
}

// redactURL removes credentials that some backends pass as query parameters.
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	clean := *u
	if q := clean.Query(); q.Has("key") {
		q.Set("key", "[redacted]")
		clean.RawQuery = q.Encode()
	}
	return clean.String()
}
