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
	"github.com/nyroway/nyro/go/internal/router"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/xds"
)

// Gateway holds the runtime dependencies for dispatching requests. Config reads
// (models, providers, API keys, bindings, proxy settings) go through Cache, an
// in-memory snapshot published by a background loader; Storage is retained for
// OAuth credential refresh, request-log quota counters, and log writes (those
// migrate off storage in later phases). Router selects among a model's backends
// and tracks failover.
type Gateway struct {
	HTTPClient     *http.Client
	Storage        storage.Storage
	Cache          *xds.ConfigCache
	Router         *router.Router
	driverRegistry *auth.Registry

	proxyMu        sync.Mutex
	proxyClient    *http.Client
	proxyClientKey string
}

// NewGateway builds a Gateway backed by the given storage. It creates an empty
// ConfigCache and seeds it with a one-shot LoadFromStorage so config reads work
// immediately; call StartConfigLoader to keep it fresh while the DB is the
// source of truth (Phase 1).
func NewGateway(s storage.Storage) *Gateway {
	cache := &xds.ConfigCache{}
	if snap, err := xds.LoadFromStorage(s); err == nil {
		cache.Swap(snap)
	}
	return newGateway(s, cache)
}

// NewGatewayWithCache builds a Gateway using a caller-provided, pre-populated
// ConfigCache. This is the standalone-YAML and xDS path: the caller (cmd/gateway)
// builds the snapshot from YAML or from the xDS stream and swaps it in, so the
// gateway never needs to read the DB for config. storage is still retained for
// OAuth/quota/logs until Phase 3.
func NewGatewayWithCache(s storage.Storage, cache *xds.ConfigCache) *Gateway {
	return newGateway(s, cache)
}

func newGateway(s storage.Storage, cache *xds.ConfigCache) *Gateway {
	return &Gateway{
		HTTPClient: &http.Client{Timeout: 5 * time.Minute},
		Storage:    s,
		Cache:      cache,
		Router:     router.New(),
	}
}

// SetDriverRegistry wires the OAuth driver registry (for token refresh).
func (g *Gateway) SetDriverRegistry(r *auth.Registry) { g.driverRegistry = r }

// ReloadCache rebuilds the config snapshot from storage and publishes it. Tests
// call this after mutating storage to reflect their changes at the read path;
// in production the background loader (StartConfigLoader) keeps it fresh.
func (g *Gateway) ReloadCache() error { return g.Cache.LoadAndSwap(g.Storage) }

// StartConfigLoader starts a background loop that refreshes the ConfigCache
// from storage at the given interval. Pass a positive interval (<=0 defaults to
// 10s). Returns a stop function; the loop also exits when the process ends.
func (g *Gateway) StartConfigLoader(interval time.Duration) func() {
	return g.Cache.StartLoaderLoop(g.Storage, interval, nil)
}

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
