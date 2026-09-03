package httpingress_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/pipeline"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

type cachedResponsePhase struct{}

func (cachedResponsePhase) Name() string { return "cache" }

func (cachedResponsePhase) Apply(_ context.Context, exchange *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
	response := llm.NewChatResponse("cached-id", exchange.Request.ModelID())
	response.Content = "cached response"
	exchange.Response = response
	return pipeline.Outcome{Decision: pipeline.ShortCircuit}, nil
}

func TestIngressRejectsOversizedBody(t *testing.T) {
	handler := newTestHandler(t, newTestSourceFromStorage(t, memory.New().Storage()))
	huge := `{"model":"gpt-4o","messages":[{"role":"user","content":"` +
		strings.Repeat("x", 33<<20) + `"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(huge))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 for a >32MiB body", response.Code)
	}
}

func TestIngressStreamsEndToEnd(t *testing.T) {
	upstream := streamUpstream(t)
	defer upstream.Close()

	handler := newTestHandler(t, newTestSource(t, upstream.URL))
	body := `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	wire := response.Body.String()
	for _, expected := range []string{`"content":"Hi"`, `"finish_reason":"stop"`, `"total_tokens":2`, "data: [DONE]"} {
		if !strings.Contains(wire, expected) {
			t.Errorf("stream response missing %q: %s", expected, wire)
		}
	}
}

func TestIngressNonStreamEndToEnd(t *testing.T) {
	upstream := nonStreamUpstream(t)
	defer upstream.Close()

	handler := newTestHandler(t, newTestSource(t, upstream.URL))
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	wire := response.Body.String()
	if !strings.Contains(wire, `"chat.completion"`) || !strings.Contains(wire, `"content":"hello"`) {
		t.Fatalf("unexpected response: %s", wire)
	}
	if count := strings.Count(wire, `"chat.completion"`); count != 1 {
		t.Fatalf("response encoded %d times, want once: %s", count, wire)
	}
}

func TestIngressModelNotFound(t *testing.T) {
	upstream := streamUpstream(t)
	defer upstream.Close()

	handler := newTestHandler(t, newTestSource(t, upstream.URL))
	body := `{"model":"unknown-model","messages":[{"role":"user","content":"hi"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", response.Code, response.Body.String())
	}
}

func TestRuntimeSourceDeliversPreDispatchShortCircuitResponse(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()

	source := newTestSource(t, upstream.URL)
	source.pre = []pipeline.Phase{cachedResponsePhase{}}
	handler := newTestHandler(t, source)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Body.String(); !strings.Contains(got, `"content":"cached response"`) {
		t.Fatalf("response body = %s, want encoded cached response", got)
	}
	if got := upstreamCalls.Load(); got != 0 {
		t.Fatalf("upstream calls = %d, want 0 after short circuit", got)
	}
}
