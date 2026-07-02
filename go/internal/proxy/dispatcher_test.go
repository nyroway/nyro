package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

func newTestGateway(t *testing.T, upstreamURL string) *Gateway {
	return newTestGatewayProto(t, upstreamURL, "openai-compatible")
}

// newTestGatewayFromStorage builds a storage-less Gateway and populates its
// config cache from the given (typically in-memory) storage. This is the test
// equivalent of the old NewGateway(s) one-shot LoadFromStorage: production no
// longer reads the DB for config (xDS / YAML), so tests seed the cache via the
// same LoadAndSwap the xDS loader uses.
func newTestGatewayFromStorage(t *testing.T, s storage.Storage) *Gateway {
	t.Helper()
	gw := NewGateway()
	if err := gw.Cache.LoadAndSwap(s); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	return gw
}

func newTestGatewayProto(t *testing.T, upstreamURL, protocol string) *Gateway {
	return newTestGatewayProviderProto(t, upstreamURL, "test", protocol)
}

// newTestGatewayProviderProto is like newTestGatewayProto but lets the caller
// pin a real provider id (e.g. "anthropic", "gemini") so provider.Resolve
// picks the vendor-specific Authenticator instead of falling back to custom.
func newTestGatewayProviderProto(t *testing.T, upstreamURL, providerID, protocol string) *Gateway {
	t.Helper()
	st := memory.New()
	core := st.Storage()
	up, _ := core.Upstreams().Create(storage.CreateUpstream{
		Name: "test", Provider: providerID, Protocol: protocol, BaseURL: upstreamURL,
		CredentialsJSON: []byte(`{"api_key":"test-key"}`),
	})
	_, _ = core.Routes().Create(storage.CreateRoute{
		Model:     "gpt-4o",
		Upstreams: []storage.CreateRouteUpstream{{UpstreamID: up.ID, Model: "gpt-4o"}},
	})
	gw := NewGateway()
	if err := gw.Cache.LoadAndSwap(core); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	return gw
}

// streamUpstream simulates an OpenAI SSE chat-completion stream.
func streamUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f := w.(http.Flusher)
		chunks := []string{
			`{"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
			`{"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hi"}}]}`,
			`{"id":"c1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`,
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			f.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}))
}

func nonStreamUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":"r1","object":"chat.completion","model":"gpt-4o",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	}))
}

func TestDispatchStreamEndToEnd(t *testing.T) {
	upstream := streamUpstream(t)
	defer upstream.Close()

	engine := NewRouter(newTestGateway(t, upstream.URL))
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"include_usage":true}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"content":"Hi"`) {
		t.Errorf("missing content delta: %s", out)
	}
	if !strings.Contains(out, `"finish_reason":"stop"`) {
		t.Errorf("missing finish_reason: %s", out)
	}
	if !strings.Contains(out, `"total_tokens":2`) {
		t.Errorf("missing usage chunk: %s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("missing [DONE] terminator: %s", out)
	}
}

func TestDispatchNonStreamEndToEnd(t *testing.T) {
	upstream := nonStreamUpstream(t)
	defer upstream.Close()

	engine := NewRouter(newTestGateway(t, upstream.URL))
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"chat.completion"`) {
		t.Errorf("missing object marker: %s", out)
	}
	if !strings.Contains(out, `"content":"hello"`) {
		t.Errorf("missing content: %s", out)
	}
}

func TestDispatchModelNotFound(t *testing.T) {
	upstream := streamUpstream(t)
	defer upstream.Close()

	engine := NewRouter(newTestGateway(t, upstream.URL))
	body := `{"model":"unknown-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404, body=%s", rec.Code, rec.Body.String())
	}
}
