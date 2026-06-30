package proxy

import (
	"net/http"
	"testing"

	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// TestGatewayHTTPClientForProxy verifies provider.use_proxy routing: when off
// (or proxy disabled) the direct client is used; when on with proxy_enabled +
// proxy_url, a distinct client with a Proxy transport is returned (cached by
// url|force_http1); an empty proxy_url errors. Ported from
// Gateway::http_client_for_provider.
func TestGatewayHTTPClientForProxy(t *testing.T) {
	st := memory.New()
	gw := NewGateway()
	if err := gw.Cache.LoadAndSwap(st.Storage()); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	direct := gw.HTTPClient

	// use_proxy=false → direct client.
	if c, err := gw.httpClientFor(false); err != nil || c != direct {
		t.Errorf("use_proxy=false: want direct client, got %v err=%v", c, err)
	}

	// use_proxy=true but proxy disabled → direct client.
	st.Settings().Set("proxy_enabled", "false")
	_ = gw.Cache.LoadAndSwap(st.Storage()) // reflect the settings change in the in-memory cache
	if c, err := gw.httpClientFor(true); err != nil || c != direct {
		t.Errorf("proxy disabled: want direct client, got %v err=%v", c, err)
	}

	// use_proxy=true + enabled + proxy_url → distinct proxied client.
	st.Settings().Set("proxy_enabled", "true")
	st.Settings().Set("proxy_url", "http://proxy.example:8080")
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

	// cached on second call (same url|force_http1).
	c2, _ := gw.httpClientFor(true)
	if c2 != c {
		t.Error("proxied client not cached across calls")
	}

	// empty proxy_url → error.
	st.Settings().Set("proxy_url", "")
	_ = gw.Cache.LoadAndSwap(st.Storage())
	if _, err := gw.httpClientFor(true); err == nil {
		t.Error("empty proxy_url: want error, got nil")
	}
}
