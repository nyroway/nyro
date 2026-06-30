package proxy

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nyroway/nyro/go/internal/auth"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/proxy/quota"
	"github.com/nyroway/nyro/go/internal/router"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/xds"
)

// Gateway holds the runtime dependencies for dispatching requests. Config reads
// (models, providers, API keys, bindings, proxy settings, OAuth credentials) go
// through Cache, an in-memory snapshot published by xDS or built once from YAML;
// Quota is the in-memory rpm/rpd/tpm/tpd sliding window (decoupled from
// request_logs in P3a). The gateway holds NO storage handle: per-request
// telemetry flows through the OTel phase hooks (Obs/Handles, registered once at
// startup) → configured sink (none/stdout/otlp). Router selects among a model's
// backends and tracks failover.
type Gateway struct {
	HTTPClient *http.Client
	Cache      *xds.ConfigCache
	Quota      *quota.Counter
	Router     *router.Router

	// Obs is the OTel provider (logger/meter/tracer). Populated by cmd/gateway
	// once at startup; nil in unit tests (the dispatcher still works, the
	// phase hooks simply aren't registered so no telemetry is emitted).
	Obs     *observability.ObsProvider
	Handles *observability.Handles

	driverRegistry *auth.Registry

	proxyMu        sync.Mutex
	proxyClient    *http.Client
	proxyClientKey string

	// OAuth refresh overlay (P3b). The cache snapshot is immutable, so a
	// locally-refreshed token is held in oauthOverlay (providerID → refreshed
	// cred) and checked before the snapshot. oauthMu serializes refresh per
	// provider: oauthRefreshInFlight[providerID] is held while one goroutine
	// refreshes that provider, so concurrent requests reuse the same token
	// instead of each spawning an upstream refresh.
	oauthMu              sync.Mutex
	oauthOverlay         map[string]storage.OAuthCredential
	oauthRefreshInFlight map[string]chan struct{}
}

// NewGateway builds a Gateway with a fresh, empty ConfigCache. Tests use this
// and populate the cache directly via Cache.LoadAndSwap / Cache.Swap. Production
// callers (cmd/gateway) use NewGatewayWithCache with a snapshot built from YAML
// or filled by the xDS stream.
func NewGateway() *Gateway {
	return NewGatewayWithCache(&xds.ConfigCache{})
}

// NewGatewayWithCache builds a Gateway using a caller-provided ConfigCache
// (standalone-YAML and xDS path): the caller builds the snapshot from YAML or
// from the xDS stream and swaps it in, so the gateway never needs storage for
// config. Obs/Handles are attached by cmd/gateway after construction.
func NewGatewayWithCache(cache *xds.ConfigCache) *Gateway {
	return &Gateway{
		HTTPClient:           &http.Client{Timeout: 5 * time.Minute},
		Cache:                cache,
		Quota:                quota.New(),
		Router:               router.New(),
		oauthOverlay:         map[string]storage.OAuthCredential{},
		oauthRefreshInFlight: map[string]chan struct{}{},
	}
}

// SetDriverRegistry wires the OAuth driver registry (for token refresh).
func (g *Gateway) SetDriverRegistry(r *auth.Registry) { g.driverRegistry = r }

// snapshot returns the current config snapshot, falling back to an empty one so
// callers never see a nil pointer (readers on an empty snapshot simply report
// "not found", matching storage behavior before any config is loaded).
func (g *Gateway) snapshot() *xds.ConfigSnapshot {
	if s := g.Cache.Load(); s != nil {
		return s
	}
	return &xds.ConfigSnapshot{}
}

// httpClientFor returns the HTTP client for an upstream provider. When useProxy
// is false (or the proxy is disabled/empty in settings) it returns the default
// direct client; when useProxy is true and "proxy_enabled" is on, it returns a
// client routed through "proxy_url" (cached by url|force_http1). Ported from
// Gateway::http_client_for_provider.
func (g *Gateway) httpClientFor(useProxy bool) (*http.Client, error) {
	if !useProxy {
		return g.HTTPClient, nil
	}
	snap := g.snapshot()
	enabled, _ := snap.SettingGet("proxy_enabled")
	if !parseBoolSetting(enabled) {
		return g.HTTPClient, nil
	}
	proxyURL, _ := snap.SettingGet("proxy_url")
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return nil, errors.New("upstream proxy_url is empty")
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy_url: %w", err)
	}
	forceHTTP1Str, _ := snap.SettingGet("proxy_force_http1")
	forceHTTP1 := parseBoolSetting(forceHTTP1Str)

	cacheKey := proxyURL + "|" + strconv.FormatBool(forceHTTP1)
	g.proxyMu.Lock()
	defer g.proxyMu.Unlock()
	if g.proxyClient != nil && g.proxyClientKey == cacheKey {
		return g.proxyClient, nil
	}

	transport := &http.Transport{Proxy: http.ProxyURL(parsed)}
	if forceHTTP1 {
		transport.ForceAttemptHTTP2 = false
	}
	client := &http.Client{Timeout: 5 * time.Minute, Transport: transport}
	g.proxyClient = client
	g.proxyClientKey = cacheKey
	return client, nil
}

// parseBoolSetting parses a settings-stored boolean (true/1/yes/on).
func parseBoolSetting(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}
