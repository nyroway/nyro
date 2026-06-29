package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// responsesStreamUpstream simulates an OpenAI Responses SSE stream.
func responsesStreamUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		events := []string{
			`{"type":"response.created","response":{"id":"r1","model":"gpt-4o","status":"in_progress"}}`,
			`{"type":"response.output_text.delta","delta":"Hi"}`,
			`{"type":"response.completed","response":{"id":"r1","status":"completed","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`,
		}
		for _, e := range events {
			fmt.Fprintf(w, "data: %s\n\n", e)
			f.Flush()
		}
	}))
}

func TestDispatchResponsesStreamEndToEnd(t *testing.T) {
	upstream := responsesStreamUpstream(t)
	defer upstream.Close()

	engine := NewRouter(newTestGatewayProto(t, upstream.URL, "openai-responses"))
	body := `{"model":"gpt-4o","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"delta":"Hi"`) {
		t.Errorf("missing text delta:\n%s", out)
	}
	if !strings.Contains(out, `"type":"response.completed"`) {
		t.Errorf("missing completed:\n%s", out)
	}
}
