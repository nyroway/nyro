package proxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nyroway/nyro/go/internal/router"
)

// TestDispatchRecordsUpstreamLatency verifies the dispatcher records the real
// upstream latency (not a hard-coded 0) after a successful call, so the
// BalanceLatency strategy can actually reorder backends. Previously every
// Record(...) call passed 0, leaving the EMA permanently 0.
func TestDispatchRecordsUpstreamLatency(t *testing.T) {
	upstream := nonStreamUpstream(t)
	defer upstream.Close()
	gw := newTestGateway(t, upstream.URL)
	r := NewRouter(gw)

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		bytes.NewReader([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("dispatch → %d %s", rec.Code, rec.Body.String())
	}

	rt := gw.snapshot().RouteByModel("gpt-4o")
	if rt == nil || len(rt.Upstreams) == 0 {
		t.Fatalf("route/backends missing: %+v", rt)
	}
	if lat := gw.Router.Latency(router.KeyOf(rt.Upstreams[0])); lat <= 0 {
		t.Errorf("upstream latency not recorded (got %v); Record must receive real latency, not 0", lat)
	}
}
