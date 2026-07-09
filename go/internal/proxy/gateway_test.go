package proxy

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/storage/memory"
	"github.com/nyroway/nyro/go/internal/configsync"
)

// TestGatewayHTTPClientForProxy verifies per-upstream proxy routing: an empty
// proxyURL uses the direct client; a malformed proxyURL (including a
// leftover pre-fix "enabled" sentinel) falls back to the direct client
// instead of erroring; a valid proxyURL returns a distinct, cached client
// with a Proxy transport; two different valid URLs return distinct clients.
func TestGatewayHTTPClientForProxy(t *testing.T) {
	st := memory.New()
	gw := NewGateway()
	if err := gw.Cache.LoadAndSwap(st.Storage()); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	direct, err := gw.httpClientFor("")
	if err != nil {
		t.Fatalf("direct client: %v", err)
	}

	// empty proxyURL → direct client (same cached instance on repeat calls).
	if c, err := gw.httpClientFor(""); err != nil || c != direct {
		t.Errorf("empty proxyURL: want direct client, got %v err=%v", c, err)
	}

	// leftover legacy "enabled" sentinel (pre-fix placeholder, not a real
	// URL) → falls back to direct rather than erroring the dispatch.
	if c, err := gw.httpClientFor("enabled"); err != nil || c != direct {
		t.Errorf("legacy \"enabled\" sentinel: want direct client (no error), got %v err=%v", c, err)
	}

	// valid proxyURL → distinct proxied client.
	c, err := gw.httpClientFor("http://proxy.example:8080")
	if err != nil {
		t.Fatalf("proxied client: %v", err)
	}
	if c == direct {
		t.Error("valid proxyURL: want distinct proxied client, got direct")
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport not *http.Transport: %T", c.Transport)
	}
	if tr.Proxy == nil {
		t.Error("proxied client transport has no Proxy function")
	}

	// cached on second call (same url|timeouts).
	c2, _ := gw.httpClientFor("http://proxy.example:8080")
	if c2 != c {
		t.Error("proxied client not cached across calls")
	}

	// a different proxyURL → distinct client (not the stale cached one).
	c3, err := gw.httpClientFor("http://other-proxy.example:9090")
	if err != nil {
		t.Fatalf("second proxied client: %v", err)
	}
	if c3 == c {
		t.Error("different proxyURL: want a distinct client, got the previous one")
	}
}

// TestResolveProxySettings_Defaults verifies the config-schema plan's example
// defaults apply when settings.proxy.* is absent.
func TestResolveProxySettings_Defaults(t *testing.T) {
	snap := (&configsync.Snapshot{}).Done()
	ps := resolveProxySettings(snap)
	if ps.RequestTimeout != 120*time.Second {
		t.Errorf("RequestTimeout = %v, want 120s", ps.RequestTimeout)
	}
	if ps.ConnectTimeout != 30*time.Second {
		t.Errorf("ConnectTimeout = %v, want 30s", ps.ConnectTimeout)
	}
	if ps.MaxRetries != 2 {
		t.Errorf("MaxRetries = %d, want 2", ps.MaxRetries)
	}
	for _, code := range []int{429, 500, 502, 503, 504} {
		if !ps.RetryOnStatus[code] {
			t.Errorf("default RetryOnStatus missing %d", code)
		}
	}
}

// TestResolveProxySettings_Overrides verifies settings.proxy.* values (as
// flattened by internal/config.flattenSettings) are parsed and override the
// defaults.
func TestResolveProxySettings_Overrides(t *testing.T) {
	st := memory.New()
	core := st.Storage()
	mustSet := func(key, value string) {
		t.Helper()
		if err := core.Settings().Set(key, value); err != nil {
			t.Fatal(err)
		}
	}
	mustSet("proxy.request_timeout", "45s")
	mustSet("proxy.connect_timeout", "5s")
	mustSet("proxy.max_retries", "4")
	codes, _ := json.Marshal([]int{408, 429})
	mustSet("proxy.retry_on_status", string(codes))

	c := &configsync.ConfigCache{}
	if err := c.LoadAndSwap(core); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	ps := resolveProxySettings(c.Load())
	if ps.RequestTimeout != 45*time.Second {
		t.Errorf("RequestTimeout = %v, want 45s", ps.RequestTimeout)
	}
	if ps.ConnectTimeout != 5*time.Second {
		t.Errorf("ConnectTimeout = %v, want 5s", ps.ConnectTimeout)
	}
	if ps.MaxRetries != 4 {
		t.Errorf("MaxRetries = %d, want 4", ps.MaxRetries)
	}
	if !ps.RetryOnStatus[408] || !ps.RetryOnStatus[429] || ps.RetryOnStatus[500] {
		t.Errorf("RetryOnStatus = %v, want exactly {408,429}", ps.RetryOnStatus)
	}
}

// TestDirectClientTransportTuning verifies the direct upstream client's
// transport is tuned for concurrency (Go's http.Transport defaults to
// MaxIdleConnsPerHost=2, which churns connections under load).
func TestDirectClientTransportTuning(t *testing.T) {
	g := NewGateway()
	client := g.directClient(proxySettings{RequestTimeout: time.Minute, ConnectTimeout: 10 * time.Second})
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", client.Transport)
	}
	if tr.MaxIdleConnsPerHost < 64 {
		t.Errorf("MaxIdleConnsPerHost=%d, want >=64 (default 2 churns connections)", tr.MaxIdleConnsPerHost)
	}
	if tr.IdleConnTimeout == 0 {
		t.Error("IdleConnTimeout unset")
	}
}
