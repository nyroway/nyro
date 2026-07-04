package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// TestDispatchMaxRetries verifies a single always-failing backend is attempted
// exactly settings.proxy.max_retries times (not once, not unbounded) before the
// dispatcher gives up.
func TestDispatchMaxRetries(t *testing.T) {
	var attempts int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer up.Close()

	st := memory.New()
	core := st.Storage()
	if err := core.Settings().Set("proxy.max_retries", "3"); err != nil {
		t.Fatal(err)
	}
	p, _ := core.Upstreams().Create(storage.CreateUpstream{
		Name: "p", Provider: "p", Protocol: "openai-compatible", BaseURL: up.URL,
		CredentialsJSON: []byte(`{"api_key":"k"}`),
	})
	_, _ = core.Routes().Create(storage.CreateRoute{
		Model:     "gpt-4o",
		Upstreams: []storage.CreateRouteUpstream{{UpstreamID: p.ID, Model: "gpt-4o"}},
	})
	engine := NewRouter(newTestGatewayFromStorage(t, core))

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502 (all backends exhausted); body=%s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("upstream attempts = %d, want 3 (max_retries)", got)
	}
}

// TestDispatchRetryOnStatusCustom verifies a status code NOT in a custom
// retry_on_status set is served as-is (no retry, no failover) even though it's
// a 5xx that the default set would have retried.
func TestDispatchRetryOnStatusCustom(t *testing.T) {
	var attempts int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError) // 500: in the default set, NOT in this test's custom set
	}))
	defer up.Close()

	st := memory.New()
	core := st.Storage()
	codes, _ := json.Marshal([]int{429})
	if err := core.Settings().Set("proxy.retry_on_status", string(codes)); err != nil {
		t.Fatal(err)
	}
	p, _ := core.Upstreams().Create(storage.CreateUpstream{
		Name: "p", Provider: "p", Protocol: "openai-compatible", BaseURL: up.URL,
		CredentialsJSON: []byte(`{"api_key":"k"}`),
	})
	_, _ = core.Routes().Create(storage.CreateRoute{
		Model:     "gpt-4o",
		Upstreams: []storage.CreateRouteUpstream{{UpstreamID: p.ID, Model: "gpt-4o"}},
	})
	engine := NewRouter(newTestGatewayFromStorage(t, core))

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	// 500 is forwarded verbatim (not retried, not turned into 502) because it's
	// outside the configured retry_on_status set.
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500 forwarded as-is; body=%s", rec.Code, rec.Body.String())
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("upstream attempts = %d, want 1 (500 not in custom retry_on_status, no retry)", got)
	}
}

// TestDispatchConnectTimeout verifies settings.proxy.connect_timeout is applied
// to the outbound dialer: dialing an address that never accepts connections
// (RFC 5737 TEST-NET-1) must fail well before the default 120s request timeout.
func TestDispatchConnectTimeout(t *testing.T) {
	st := memory.New()
	core := st.Storage()
	if err := core.Settings().Set("proxy.connect_timeout", "200ms"); err != nil {
		t.Fatal(err)
	}
	if err := core.Settings().Set("proxy.max_retries", "1"); err != nil {
		t.Fatal(err)
	}
	p, _ := core.Upstreams().Create(storage.CreateUpstream{
		Name: "p", Provider: "p", Protocol: "openai-compatible",
		BaseURL:         "http://192.0.2.1:81", // TEST-NET-1: reserved, non-routable
		CredentialsJSON: []byte(`{"api_key":"k"}`),
	})
	_, _ = core.Routes().Create(storage.CreateRoute{
		Model:     "gpt-4o",
		Upstreams: []storage.CreateRouteUpstream{{UpstreamID: p.ID, Model: "gpt-4o"}},
	})
	engine := NewRouter(newTestGatewayFromStorage(t, core))

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		engine.ServeHTTP(rec, req)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch did not return within 2s; connect_timeout=200ms was not applied")
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502 (connect timeout → network error → exhausted); body=%s", rec.Code, rec.Body.String())
	}
}
