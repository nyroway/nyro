package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/bootstrap"
	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

type closingRoundTripper struct{ closes int }

func (*closingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (transport *closingRoundTripper) CloseIdleConnections() { transport.closes++ }

func testProtocolCatalog(t *testing.T) *protocol.Catalog {
	t.Helper()
	catalog, err := bootstrap.NewLLMProtocolCatalog()
	if err != nil {
		t.Fatalf("compose LLM protocols: %v", err)
	}
	return catalog
}

func testProviderCatalog(t *testing.T) *provider.Catalog {
	t.Helper()
	catalog, err := bootstrap.NewLLMProviderCatalog()
	if err != nil {
		t.Fatalf("compose LLM providers: %v", err)
	}
	return catalog
}

// TestGatewayProviderTransportCaching verifies that resolved outbound
// transports are cached by proxy and timeout configuration. Proxy mechanics
// are covered by provider/httptransport's focused tests.
func TestGatewayProviderTransportCaching(t *testing.T) {
	st := memory.New()
	gw := NewGateway(testProtocolCatalog(t), testProviderCatalog(t))
	if err := gw.Cache.LoadAndSwap(st.Storage()); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	direct, err := gw.providerTransportFor("")
	if err != nil {
		t.Fatalf("direct client: %v", err)
	}

	// empty proxyURL → direct client (same cached instance on repeat calls).
	if c, err := gw.providerTransportFor(""); err != nil || c != direct {
		t.Errorf("empty proxyURL: want direct client, got %v err=%v", c, err)
	}

	// leftover legacy "enabled" sentinel (pre-fix placeholder, not a real
	// URL) → falls back to direct rather than erroring the dispatch.
	if _, err := gw.providerTransportFor("enabled"); err != nil {
		t.Errorf("legacy \"enabled\" sentinel: want direct transport behavior, got err=%v", err)
	}

	// valid proxyURL → distinct proxied client.
	c, err := gw.providerTransportFor("http://proxy.example:8080")
	if err != nil {
		t.Fatalf("proxied client: %v", err)
	}
	if c == direct {
		t.Error("valid proxyURL: want distinct proxied client, got direct")
	}
	// cached on second call (same url|timeouts).
	c2, _ := gw.providerTransportFor("http://proxy.example:8080")
	if c2 != c {
		t.Error("proxied client not cached across calls")
	}

	// a different proxyURL → distinct client (not the stale cached one).
	c3, err := gw.providerTransportFor("http://other-proxy.example:9090")
	if err != nil {
		t.Fatalf("second proxied client: %v", err)
	}
	if c3 == c {
		t.Error("different proxyURL: want a distinct client, got the previous one")
	}
}

func TestGatewayRetiresReplacedAndActiveProviderTransports(t *testing.T) {
	t.Parallel()
	closer := &closingRoundTripper{}
	gw := NewGateway(testProtocolCatalog(t), testProviderCatalog(t))
	gw.UpstreamTransport = closer
	if _, err := gw.providerTransportFor("http://first-proxy.example:8080"); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.providerTransportFor("http://second-proxy.example:8080"); err != nil {
		t.Fatal(err)
	}
	if closer.closes != 1 {
		t.Fatalf("replaced transport close calls = %d, want 1", closer.closes)
	}
	gw.CloseIdleConnections()
	if closer.closes != 2 {
		t.Fatalf("shutdown transport close calls = %d, want 2", closer.closes)
	}
	gw.CloseIdleConnections()
	if closer.closes != 2 {
		t.Fatalf("CloseIdleConnections() was not idempotent: %d calls", closer.closes)
	}
}

// TestResolveProxySettings_Defaults verifies the config-schema plan's example
// defaults apply when settings.proxy.* is absent.
func TestResolveProxySettings_Defaults(t *testing.T) {
	snap := (&configsnapshot.Builder{}).Build()
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

	c := &configsnapshot.Cache{}
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
