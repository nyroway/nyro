package proxy

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/storage/memory"
	"github.com/nyroway/nyro/go/internal/xds"
)

// TestGatewayHTTPClientForProxy verifies upstream.proxy_url routing: when off
// (or proxy disabled) the direct client is used; when on with proxy_enabled +
// proxy_url, a distinct client with a Proxy transport is returned (cached by
// url|force_http1|timeouts); an empty proxy_url errors.
func TestGatewayHTTPClientForProxy(t *testing.T) {
	st := memory.New()
	gw := NewGateway()
	if err := gw.Cache.LoadAndSwap(st.Storage()); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	direct, err := gw.httpClientFor(false)
	if err != nil {
		t.Fatalf("direct client: %v", err)
	}

	// use_proxy=false → direct client (same cached instance on repeat calls).
	if c, err := gw.httpClientFor(false); err != nil || c != direct {
		t.Errorf("use_proxy=false: want direct client, got %v err=%v", c, err)
	}

	// use_proxy=true but proxy disabled → direct client.
	st.Storage().Settings().Set("proxy_enabled", "false")
	_ = gw.Cache.LoadAndSwap(st.Storage()) // reflect the settings change in the in-memory cache
	if c, err := gw.httpClientFor(true); err != nil || c != direct {
		t.Errorf("proxy disabled: want direct client, got %v err=%v", c, err)
	}

	// use_proxy=true + enabled + proxy_url → distinct proxied client.
	st.Storage().Settings().Set("proxy_enabled", "true")
	st.Storage().Settings().Set("proxy_url", "http://proxy.example:8080")
	_ = gw.Cache.LoadAndSwap(st.Storage())
	c, err := gw.httpClientFor(true)
	if err != nil {
		t.Fatalf("proxied client: %v", err)
	}
	if c == direct {
		t.Error("use_proxy=true + enabled: want distinct proxied client, got direct")
	}
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport not *http.Transport: %T", c.Transport)
	}
	if tr.Proxy == nil {
		t.Error("proxied client transport has no Proxy function")
	}

	// cached on second call (same url|force_http1|timeouts).
	c2, _ := gw.httpClientFor(true)
	if c2 != c {
		t.Error("proxied client not cached across calls")
	}

	// empty proxy_url → error.
	st.Storage().Settings().Set("proxy_url", "")
	_ = gw.Cache.LoadAndSwap(st.Storage())
	if _, err := gw.httpClientFor(true); err == nil {
		t.Error("empty proxy_url: want error, got nil")
	}
}

// TestResolveProxySettings_Defaults verifies the config-schema plan's example
// defaults apply when settings.proxy.* is absent.
func TestResolveProxySettings_Defaults(t *testing.T) {
	snap := (&xds.Snapshot{}).Done()
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
	core.Settings().Set("proxy.request_timeout", "45s")
	core.Settings().Set("proxy.connect_timeout", "5s")
	core.Settings().Set("proxy.max_retries", "4")
	codes, _ := json.Marshal([]int{408, 429})
	core.Settings().Set("proxy.retry_on_status", string(codes))

	c := &xds.ConfigCache{}
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
