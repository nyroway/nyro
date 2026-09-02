package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyroway/nyro/go/internal/llm"
	llmpipeline "github.com/nyroway/nyro/go/internal/llm/pipeline"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

type cachedResponsePhase struct{}

func (cachedResponsePhase) Name() string { return "cache" }

func (cachedResponsePhase) Apply(_ context.Context, ex *llmpipeline.Exchange) (llmpipeline.Outcome, llmpipeline.Finalizer) {
	response := llm.NewChatResponse("cached-id", ex.Request.ModelID())
	response.Content = "cached response"
	ex.Response = response
	return llmpipeline.Outcome{Decision: llmpipeline.ShortCircuit}, nil
}

// TestRejectsOversizedBody verifies handleProxy rejects request bodies larger
// than settings.proxy.max_body_bytes (default 32MiB) with 413, instead of
// buffering the whole thing via io.ReadAll.
func TestRejectsOversizedBody(t *testing.T) {
	st := memory.New().Storage()
	engine := NewRouter(newTestGatewayFromStorage(t, st))

	huge := `{"model":"gpt-4o","messages":[{"role":"user","content":"` +
		strings.Repeat("x", 33<<20) + `"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(huge))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status=%d, want 413 for a >32MiB body", rec.Code)
	}
}

func newTestGateway(t *testing.T, upstreamURL string) *Gateway {
	return newTestGatewayProto(t, upstreamURL, "openai-chatcompletions")
}

// newTestGatewayFromStorage builds a storage-less Gateway and populates its
// config cache from the given (typically in-memory) storage. This is the test
// equivalent of the old NewGateway(s) one-shot storage projection: production no
// longer reads the DB for config (config-sync / YAML), so tests seed the cache via the
// same LoadAndSwap the config-sync loader uses.
func newTestGatewayFromStorage(t *testing.T, s storage.Storage) *Gateway {
	t.Helper()
	gw := NewGateway(testProtocolCatalog(t), testProviderCatalog(t))
	if err := storage.LoadAndSwap(gw.Cache, s); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	return gw
}

func newTestGatewayProto(t *testing.T, upstreamURL, protocol string) *Gateway {
	return newTestGatewayProviderProto(t, upstreamURL, "test", protocol)
}

// newTestGatewayProviderProto is like newTestGatewayProto but also takes a
// providerID (e.g. "anthropic", "gemini") that's stored on the upstream row.
// The catalog resolves a known ID to its provider-specific driver and an
// unknown ID to the generic protocol-aware fallback. Passing a real preset ID
// therefore exercises explicit driver selection end-to-end.
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
	gw := NewGateway(testProtocolCatalog(t), testProviderCatalog(t))
	if err := storage.LoadAndSwap(gw.Cache, core); err != nil {
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
			_, _ = fmt.Fprintf(w, "data: %s\n\n", c)
			f.Flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
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
	if got := strings.Count(out, `"chat.completion"`); got != 1 {
		t.Errorf("response encoded %d times, want the already-written response preserved: %s", got, out)
	}
}

func TestDispatchWritesPreDispatchShortCircuitResponse(t *testing.T) {
	var upstreamCalls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&upstreamCalls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	gw := newTestGateway(t, upstream.URL)
	gw.preDispatchPhases = []llmpipeline.Phase{cachedResponsePhase{}}
	engine := NewRouter(gw)
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, `"content":"cached response"`) {
		t.Fatalf("response body = %s, want encoded cached response", got)
	}
	if got := atomic.LoadInt32(&upstreamCalls); got != 0 {
		t.Fatalf("upstream calls = %d, want 0 after short circuit", got)
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
