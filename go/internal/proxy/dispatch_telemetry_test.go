package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/pipeline"
	"github.com/nyroway/nyro/go/internal/protocol/llm/ir"
	"github.com/nyroway/nyro/go/internal/storage"
)

// captureStage records the Exchange as the chain unwinds, standing in for the
// telemetry Stage so a test can assert what telemetry would have seen.
//
// It captures after next returns, which is the same position the real
// telemetry Stage emits from. Placed first in the chain (see
// newCapturingGateway), that also means it observes exchanges a later Stage
// short-circuited.
type captureStage struct{ got **pipeline.Exchange }

func (captureStage) Name() string { return "test.capture" }

func (c captureStage) Handle(ex *pipeline.Exchange, next func() error) error {
	defer func() { *c.got = ex }()
	return next()
}

// newCapturingGateway builds a test gateway whose chain starts with a
// captureStage, and returns a func yielding the captured Exchange.
func newCapturingGateway(t *testing.T, upstreamURL string) (*Gateway, func(*testing.T) *pipeline.Exchange) {
	t.Helper()
	gw := newTestGateway(t, upstreamURL)
	var captured *pipeline.Exchange
	gw.OuterStages = []pipeline.Stage{captureStage{got: &captured}}
	return gw, func(t *testing.T) *pipeline.Exchange {
		t.Helper()
		if captured == nil {
			t.Fatal("no Exchange captured: the chain never ran")
		}
		return captured
	}
}

// TestDispatchPopulatesExchangeBeforeTelemetry is the core ordering invariant:
// by the time the chain unwinds to the telemetry Stage, the Exchange must hold
// route, upstream, usage, status, consumer, started, and the LogCtx. The
// dispatcher fills these in as they become known and the telemetry Stage emits
// from a defer, guaranteeing it sees the finished picture. (The emit itself is
// tested in internal/observability/stage_test.go.)
func TestDispatchPopulatesExchangeBeforeTelemetry(t *testing.T) {
	upstream := nonStreamUpstream(t)
	defer upstream.Close()
	gw, captured := newCapturingGateway(t, upstream.URL)
	r := NewRouter(gw)

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
	route, _ := ex.GetExt(observability.ExtRoute).(storage.Route)
	if route.Model != "gpt-4o" {
		t.Errorf("exchange route = %+v; want model gpt-4o", route)
	}
	up, _ := ex.GetExt(observability.ExtUpstream).(storage.Upstream)
	if up.Name != "test" {
		t.Errorf("exchange upstream = %+v; want name test", up)
	}
	if ex.Usage.PromptTokens != 3 || ex.Usage.CompletionTokens != 2 {
		t.Errorf("exchange usage = %+v; want prompt=3 completion=2 (from nonStreamUpstream fixture)", ex.Usage)
	}
	if ex.Started.Before(startWall) {
		t.Errorf("exchange started %v before dispatch entry %v", ex.Started, startWall)
	}
	lc, _ := ex.GetExt(observability.ExtLogCtx).(observability.LogCtx)
	if lc.ClientModel != "gpt-4o" || lc.Method != http.MethodPost {
		t.Errorf("exchange logctx = %+v; want ClientModel=gpt-4o Method=POST", lc)
	}
	// ConsumerID empty: the fixture model has EnableAuth=false (open), so no
	// key resolves.
	if ex.ConsumerID != "" {
		t.Errorf("exchange consumer = %q; want empty for open model", ex.ConsumerID)
	}
	// The upstream's response usage must have reached the exchange.
	if ex.Resp == nil {
		t.Error("exchange Resp is nil for a non-streaming response")
	}
}

// TestDispatchPopulatesExchangeOnEarlyExit asserts the Exchange still carries
// the status when the request is rejected BEFORE reaching an upstream
// (model-not-found): the route Stage short-circuits, and the outer Stages —
// telemetry in production, captureStage here — still unwind.
func TestDispatchPopulatesExchangeOnEarlyExit(t *testing.T) {
	upstream := nonStreamUpstream(t) // never hit (model not found)
	defer upstream.Close()
	gw, captured := newCapturingGateway(t, upstream.URL)
	r := NewRouter(gw)

	body := `{"model":"no-such-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("dispatch → %d, want 404", rec.Code)
	}

	ex := captured(t)
	// Dispatch stamps the recorder's status onto the Exchange after the chain
	// returns, so a Stage reading ex.Status in its own defer sees the code the
	// client actually got.
	if ex.Status != http.StatusNotFound {
		t.Errorf("exchange status = %d; want 404 (early-exit path must still record it)", ex.Status)
	}
	if _, ok := ex.GetExt(observability.ExtRoute).(storage.Route); ok {
		t.Error("exchange carries a route for a model that does not exist")
	}
	if ex.Usage != (ir.Usage{}) {
		t.Errorf("exchange usage = %+v; want zero on the early-exit path", ex.Usage)
	}
}
