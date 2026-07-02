package proxy

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// anthropicStreamUpstream simulates an Anthropic SSE message stream.
func anthropicStreamUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-api-key"); got == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		events := []struct{ event, data string }{
			{"message_start", `{"type":"message_start","message":{"id":"m1","model":"claude","usage":{"input_tokens":3}}}`},
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`},
			{"message_stop", `{"type":"message_stop"}`},
		}
		for _, e := range events {
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.event, e.data)
			f.Flush()
		}
	}))
}

func TestDispatchAnthropicStreamEndToEnd(t *testing.T) {
	upstream := anthropicStreamUpstream(t)
	defer upstream.Close()

	engine := NewRouter(newTestGatewayProviderProto(t, upstream.URL, "anthropic", "anthropic-messages"))
	body := `{"model":"gpt-4o","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
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
	if !strings.Contains(out, "event: message_stop") {
		t.Errorf("missing message_stop terminator:\n%s", out)
	}
	if !strings.Contains(out, `"stop_reason":"end_turn"`) {
		t.Errorf("missing stop_reason:\n%s", out)
	}
}
