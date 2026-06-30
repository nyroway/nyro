package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/plugin"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
	"github.com/nyroway/nyro/go/internal/storage"
)

// bagCapture is a test-only OnLog hook that stashes the per-request ContextBag
// so a test can assert the dispatcher populated it fully BEFORE the hook ran.
// Registered once (sync.Once); the latest bag is kept in lastBag. Other proxy
// tests also Dispatch (re-firing this hook), which only overwrites lastBag —
// harmless, since only the assertion test reads it.
var (
	bagOnce    sync.Once
	lastBagMu  sync.Mutex
	lastBag    *plugin.ContextBag
	bagCapture = bagCaptureHook{}
)

type bagCaptureHook struct{}

func (bagCaptureHook) Name() string        { return "test.bag_capture" }
func (bagCaptureHook) Phase() plugin.Phase { return plugin.PhaseOnLog }
func (bagCaptureHook) Run(pctx *plugin.PhaseContext) plugin.PhaseOutcome {
	lastBagMu.Lock()
	lastBag = pctx.Bag
	lastBagMu.Unlock()
	return plugin.OutcomeContinue
}

func registerBagCaptureOnce() {
	bagOnce.Do(func() { plugin.Register(bagCapture) })
}

// bagGet reads a key off the captured bag and returns its typed value (or the
// zero value if absent / wrong type).
func bagGet[T any](t *testing.T, key string) T {
	t.Helper()
	lastBagMu.Lock()
	b := lastBag
	lastBagMu.Unlock()
	if b == nil {
		t.Fatalf("no bag captured: OnLog hook never ran (dispatcher didn't reach the terminal defer?)")
	}
	v, ok := b.Get(key)
	if !ok {
		t.Fatalf("bag key %q not set before OnLog", key)
	}
	tv, _ := v.(T)
	return tv
}

// TestDispatchPopulatesBagBeforeOnLog is the core ordering invariant: by the
// time the OnLog phase hook runs, the per-request ContextBag must hold model,
// provider, usage, status, apiKeyID, started, and the LogCtx. The dispatcher
// populates the bag in the terminal LIFO defer (registered after the OnLog
// defer), guaranteeing this. (Per the controller's guidance, this is the one
// place proxy tests touch hook wiring; the hook emit itself is tested in
// internal/observability/hooks_test.go.)
func TestDispatchPopulatesBagBeforeOnLog(t *testing.T) {
	registerBagCaptureOnce()

	upstream := nonStreamUpstream(t)
	defer upstream.Close()
	gw := newTestGateway(t, upstream.URL)
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

	// Bag fully populated before the OnLog hook read it.
	model := bagGet[storage.Model](t, string(observability.BagModel))
	if model.Name != "gpt-4o" {
		t.Errorf("bag model = %+v; want name gpt-4o", model)
	}
	provider := bagGet[storage.Provider](t, string(observability.BagProvider))
	if provider.Name != "test" {
		t.Errorf("bag provider = %+v; want name test", provider)
	}
	status := bagGet[int](t, string(observability.BagStatus))
	if status != http.StatusOK {
		t.Errorf("bag status = %d; want 200", status)
	}
	usage := bagGet[ir.Usage](t, string(observability.BagUsage))
	if usage.PromptTokens != 3 || usage.CompletionTokens != 2 {
		t.Errorf("bag usage = %+v; want prompt=3 completion=2 (from nonStreamUpstream fixture)", usage)
	}
	started := bagGet[time.Time](t, string(observability.BagStarted))
	if started.Before(startWall) {
		t.Errorf("bag started %v before dispatch entry %v", started, startWall)
	}
	lc := bagGet[observability.LogCtx](t, string(observability.BagLogCtx))
	if lc.ClientModel != "gpt-4o" || lc.Method != http.MethodPost {
		t.Errorf("bag logctx = %+v; want ClientModel=gpt-4o Method=POST", lc)
	}
	// apiKeyID empty: the fixture model has EnableAuth=false (open), so no key
	// resolves. Asserting the empty string documents that the field is set (not
	// left uninitialized / absent).
	if apiKeyID := bagGet[string](t, string(observability.BagAPIKeyID)); apiKeyID != "" {
		t.Errorf("bag apiKeyID = %q; want empty for open model", apiKeyID)
	}
}

// TestDispatchPopulatesBagOnEarlyExit asserts the bag is still fully populated
// when Dispatch returns BEFORE reaching the upstream (model-not-found path):
// model stays zero-value, status is the 404, and the OnLog hook still fires
// with a bag (the LIFO defer runs on every return path).
func TestDispatchPopulatesBagOnEarlyExit(t *testing.T) {
	registerBagCaptureOnce()

	upstream := nonStreamUpstream(t) // never hit (model not found)
	defer upstream.Close()
	gw := newTestGateway(t, upstream.URL)
	r := NewRouter(gw)

	body := `{"model":"no-such-model","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(body)))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("dispatch → %d, want 404", rec.Code)
	}

	status := bagGet[int](t, string(observability.BagStatus))
	if status != http.StatusNotFound {
		t.Errorf("bag status = %d; want 404 (early-exit path must still set status)", status)
	}
}
