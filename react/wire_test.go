package react

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The captured bytes are sent to the browser, so the key must never survive in
// them — not in the body, not in the URL.
func TestCaptureRedactsTheAPIKey(t *testing.T) {
	const key = "AIzaSy-super-secret-key"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "hello") {
			t.Errorf("request body did not reach the server: %q", body)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"echo":"` + key + `"}`))
	}))
	defer srv.Close()

	client := &http.Client{
		Transport: &captureTransport{base: http.DefaultTransport, secret: key},
	}
	ctx, wire := withWire(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		srv.URL+"/v1beta/models/x:generateContent?key="+key,
		strings.NewReader(`{"contents":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// The SDK must still see an intact body after we have read it.
	got, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(got), "echo") {
		t.Errorf("response body was consumed by the capture: %q", got)
	}

	if !strings.Contains(wire.Request, `"contents":"hello"`) {
		t.Errorf("request not captured: %q", wire.Request)
	}
	if wire.Status != "200 OK" {
		t.Errorf("status = %q, want 200 OK", wire.Status)
	}
	for name, field := range map[string]string{
		"url":      wire.URL,
		"request":  wire.Request,
		"response": wire.Response,
	} {
		if strings.Contains(field, key) {
			t.Errorf("the API key leaked into the captured %s: %q", name, field)
		}
	}
	if !strings.Contains(wire.URL, "key=%5Bredacted%5D") {
		t.Errorf("url = %q, want the key parameter redacted", wire.URL)
	}
	if !strings.Contains(wire.Response, "[redacted]") {
		t.Errorf("response = %q, want the key redacted", wire.Response)
	}
}

// A Model that never makes an HTTP call leaves the exchange empty rather than
// failing, which is what keeps the scripted tests working.
func TestWireIsEmptyWithoutAnHTTPCall(t *testing.T) {
	_, wire := withWire(context.Background())
	if wire.Request != "" || wire.Response != "" {
		t.Errorf("expected an empty exchange, got %+v", wire)
	}
}
