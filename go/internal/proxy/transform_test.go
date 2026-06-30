package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// TestCrossProtocolAnthropicToOpenAI proves cross-protocol Transform: an
// Anthropic Messages client hits a model backed by an OpenAI-compatible
// provider. The gateway decodes Anthropic → IR → encodes OpenAI → upstream
// returns OpenAI → parses with OpenAI decoder → formats with Anthropic encoder.
func TestCrossProtocolAnthropicToOpenAI(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The egress codec (OpenAI) sends to /v1/chat/completions.
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("upstream path=%s, want /v1/chat/completions (egress should be OpenAI)", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"r1","object":"chat.completion","model":"gpt-4o",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"hello from openai"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
	defer up.Close()

	// Provider speaks openai-compatible; client will POST /v1/messages (Anthropic).
	st := memory.New()
	prov, _ := st.Providers().Create(storage.CreateProvider{
		Name: "openai-upstream", Protocol: "openai-compatible", BaseURL: up.URL, APIKey: "sk-test",
	})
	_, _ = st.Models().Create(storage.CreateModel{
		Name:    "claude-sonnet",
		Targets: []storage.CreateModelBackend{{ProviderID: prov.ID, Model: "gpt-4o"}},
	})
	engine := NewRouter(newTestGatewayFromStorage(t, st.Storage()))

	// Client sends an Anthropic Messages request.
	body := `{"model":"claude-sonnet","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	// Response must be in Anthropic format (the ingress codec formats it).
	if !strings.Contains(out, `"type":"message"`) {
		t.Errorf("expected Anthropic response, got:\n%s", out)
	}
	// Content from the OpenAI upstream must survive the transform.
	if !strings.Contains(out, "hello from openai") {
		t.Errorf("content lost in transform:\n%s", out)
	}
}
