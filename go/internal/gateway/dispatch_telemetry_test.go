package gateway

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/pipeline"
)

// capturePhase records the Exchange from the Observe finalizer, at the same
// lifecycle point where telemetry emits.
type capturePhase struct{ got **pipeline.Exchange }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (capturePhase) Name() string { return "observe" }

func (c capturePhase) Apply(context.Context, *pipeline.Exchange) (pipeline.Outcome, pipeline.Finalizer) {
	return pipeline.Outcome{Decision: pipeline.Continue}, func(_ context.Context, ex *pipeline.Exchange, _ pipeline.Completion) error {
		*c.got = ex
		return nil
	}
}

// newCapturingGateway replaces only the mandatory Observe implementation.
func newCapturingGateway(t *testing.T, upstreamURL string) (*Gateway, func(*testing.T) *pipeline.Exchange) {
	t.Helper()
	gw := newTestGateway(t, upstreamURL)
	var captured *pipeline.Exchange
	gw.observePhase = capturePhase{got: &captured}
	return gw, func(t *testing.T) *pipeline.Exchange {
		t.Helper()
		if captured == nil {
			t.Fatal("no Exchange captured: the chain never ran")
		}
		return captured
	}
}

// TestDispatchPopulatesExchangeBeforeTelemetry is the core ordering invariant:
// by the time the Observe finalizer runs, the Exchange must hold route, target,
// usage, status, identity, request metadata, and response.
func TestDispatchPopulatesExchangeBeforeTelemetry(t *testing.T) {
	upstream := nonStreamUpstream(t)
	defer upstream.Close()
	gw, captured := newCapturingGateway(t, upstream.URL)
	r := newTestHandler(t, gw)

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	startWall := time.Now()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch → %d %s", rec.Code, rec.Body.String())
	}

	ex := captured(t)
	if ex.Route.Model != "gpt-4o" {
		t.Errorf("exchange route = %+v; want model gpt-4o", ex.Route)
	}
	if ex.Target.UpstreamName != "test" {
		t.Errorf("exchange target = %+v; want upstream name test", ex.Target)
	}
	if ex.Target.ID == "" {
		t.Error("exchange target ID is empty")
	}
	if ex.Usage.PromptTokens != 3 || ex.Usage.CompletionTokens != 2 {
		t.Errorf("exchange usage = %+v; want prompt=3 completion=2 (from nonStreamUpstream fixture)", ex.Usage)
	}
	if ex.Started.Before(startWall) {
		t.Errorf("exchange started %v before dispatch entry %v", ex.Started, startWall)
	}
	if ex.Request.ModelID() != "gpt-4o" || ex.RequestInfo.Operation != http.MethodPost {
		t.Errorf("exchange request metadata = %+v; want ClientModel=gpt-4o Method=POST", ex.RequestInfo)
	}
	// Identity subject is empty: the fixture model has EnableAuth=false, so no
	// key resolves.
	if ex.Identity.Subject != "" || !ex.Identity.Anonymous {
		t.Errorf("exchange identity = %+v; want anonymous for open model", ex.Identity)
	}
	// The upstream's response usage must have reached the exchange.
	if ex.Response == nil {
		t.Error("exchange Response is nil for a non-streaming response")
	}
}

// TestDispatchPopulatesExchangeOnEarlyExit asserts the Exchange still carries
// the status when the request is rejected BEFORE reaching an upstream
// (model-not-found): Resolve rejects, but the Observe Finalizer still runs.
func TestDispatchPopulatesExchangeOnEarlyExit(t *testing.T) {
	upstream := nonStreamUpstream(t) // never hit (model not found)
	defer upstream.Close()
	gw, captured := newCapturingGateway(t, upstream.URL)
	r := newTestHandler(t, gw)

	body := `{"model":"no-such-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("dispatch → %d, want 404", rec.Code)
	}

	ex := captured(t)
	// Resolve stamps the status before returning Reject, so Observe sees it.
	if ex.Status != http.StatusNotFound {
		t.Errorf("exchange status = %d; want 404 (early-exit path must still record it)", ex.Status)
	}
	if ex.Route != (pipeline.LogicalRoute{}) {
		t.Error("exchange carries a route for a model that does not exist")
	}
	if ex.Usage != (llm.Usage{}) {
		t.Errorf("exchange usage = %+v; want zero on the early-exit path", ex.Usage)
	}
}

func TestDispatchPublishesTargetAndTypedErrorWhenTransportsAreExhausted(t *testing.T) {
	gw, captured := newCapturingGateway(t, "https://upstream.invalid")
	gw.UpstreamTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("synthetic transport failure")
	})
	r := newTestHandler(t, gw)

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "all upstream backends failed") {
		t.Fatalf("dispatch = %d %s, want unchanged exhausted-backends 502", rec.Code, rec.Body.String())
	}
	ex := captured(t)
	if ex.Target.ID == "" || ex.Target.UpstreamName != "test" || ex.Target.Model != "gpt-4o" {
		t.Errorf("exchange target = %+v, want attempted test/gpt-4o target", ex.Target)
	}
	if ex.Target.UpstreamLatencyMs == nil {
		t.Error("exchange target has no latency for the failed transport attempt")
	}
	if ex.Error == nil || ex.Error.StatusCode == nil || *ex.Error.StatusCode != http.StatusBadGateway {
		t.Errorf("exchange error = %+v, want typed 502 terminal error", ex.Error)
	}
}

func TestDispatchPublishesTypedErrorForProviderFailureWithoutChangingWireResponse(t *testing.T) {
	const providerBody = `{"error":{"message":"provider rejected"}}`
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(providerBody))
	}))
	defer upstream.Close()

	gw, captured := newCapturingGateway(t, upstream.URL)
	r := newTestHandler(t, gw)
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest || rec.Body.String() != providerBody {
		t.Fatalf("dispatch = %d %s, want verbatim provider 400 body", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("content type = %q, want provider content type", got)
	}
	ex := captured(t)
	if ex.Target.ID == "" || ex.Target.UpstreamName != "test" {
		t.Errorf("exchange target = %+v, want selected test target", ex.Target)
	}
	if ex.Target.UpstreamStatus == nil || *ex.Target.UpstreamStatus != http.StatusBadRequest {
		t.Errorf("exchange target status = %v, want provider 400", ex.Target.UpstreamStatus)
	}
	if ex.Error == nil || ex.Error.StatusCode == nil || *ex.Error.StatusCode != http.StatusBadRequest {
		t.Errorf("exchange error = %+v, want typed provider 400 terminal error", ex.Error)
	}
}
