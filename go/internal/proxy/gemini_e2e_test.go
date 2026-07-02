package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// geminiStreamUpstream simulates a Gemini streamGenerateContent SSE stream.
func geminiStreamUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-goog-api-key"); got == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		chunks := []string{
			`{"candidates":[{"content":{"parts":[{"text":"Hi"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"there"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`,
		}
		for _, c := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", c)
			f.Flush()
		}
	}))
}

func TestDispatchGeminiStreamEndToEnd(t *testing.T) {
	upstream := geminiStreamUpstream(t)
	defer upstream.Close()

	engine := NewRouter(newTestGatewayProviderProto(t, upstream.URL, "gemini", "google-gemini"))
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gpt-4o:streamGenerateContent", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"text":"Hi"`) {
		t.Errorf("missing text delta:\n%s", out)
	}
	if !strings.Contains(out, `"text":"there"`) {
		t.Errorf("missing second text delta:\n%s", out)
	}
	if !strings.Contains(out, `"finishReason":"stop"`) {
		t.Errorf("missing finishReason:\n%s", out)
	}
}

func TestDispatchGeminiModelNotFound(t *testing.T) {
	upstream := geminiStreamUpstream(t)
	defer upstream.Close()

	engine := NewRouter(newTestGatewayProviderProto(t, upstream.URL, "gemini", "google-gemini"))
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	// Model alias "unknown-model" has no route → 404.
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/unknown-model:generateContent", strings.NewReader(body))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404, body=%s", rec.Code, rec.Body.String())
	}
}
