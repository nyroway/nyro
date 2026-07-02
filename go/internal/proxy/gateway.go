package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/proxy/quota"
	"github.com/nyroway/nyro/go/internal/router"
	"github.com/nyroway/nyro/go/internal/xds"
)

// Gateway holds the runtime dependencies for dispatching requests. Config reads
// (upstreams, routes, consumer keys, proxy settings) go through Cache, an
// in-memory snapshot published by xDS or built once from YAML; Quota is the
// in-memory consumer-quota sliding window. The gateway holds NO storage handle:
// per-request telemetry flows through the OTel phase hooks (Obs/Handles,
// registered once at startup) → configured sink (none/stdout/otlp). Router
// selects among a route's upstreams and tracks failover.
type Gateway struct {
	Cache  *xds.ConfigCache
	Quota  *quota.Counter
	Router *router.Router

	// Obs is the OTel provider (logger/meter/tracer). Populated by cmd/gateway
	// once at startup; nil in unit tests (the dispatcher still works, the
	// phase hooks simply aren't registered so no telemetry is emitted).
	Obs     *observability.ObsProvider
	Handles *observability.Handles

	clientMu       sync.Mutex
	client         *http.Client // direct (no proxy) client, rebuilt when timeouts change
	clientKey      string
	proxyClient    *http.Client
	proxyClientKey string
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
		Cache:  cache,
		Quota:  quota.New(),
		Router: router.New(),
	}
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

// proxySettings is the resolved settings.proxy configuration for the current
// snapshot: request/connect timeouts, the per-backend retry budget, and the
// status codes that trigger a retry/failover. Defaults mirror the config-schema
// plan's example config.yaml.
type proxySettings struct {
	RequestTimeout time.Duration
	ConnectTimeout time.Duration
	MaxRetries     int
	RetryOnStatus  map[int]bool
	MaxBodyBytes   int64
}

var defaultRetryOnStatus = map[int]bool{429: true, 500: true, 502: true, 503: true, 504: true}

// resolveProxySettings reads settings.proxy.* from the snapshot (flattened by
// internal/config.flattenSettings under the proxy.* dot-key namespace),
// applying the config-schema plan's example defaults for anything absent or
// unparseable.
func resolveProxySettings(snap *xds.ConfigSnapshot) proxySettings {
	ps := proxySettings{
		RequestTimeout: 120 * time.Second,
		ConnectTimeout: 30 * time.Second,
		MaxRetries:     2,
		RetryOnStatus:  defaultRetryOnStatus,
		MaxBodyBytes:   32 << 20,
	}
	if v, ok := snap.SettingGet("proxy.request_timeout"); ok {
		if d, err := time.ParseDuration(v); err == nil {
			ps.RequestTimeout = d
		}
	}
	if v, ok := snap.SettingGet("proxy.connect_timeout"); ok {
		if d, err := time.ParseDuration(v); err == nil {
			ps.ConnectTimeout = d
		}
	}
	if v, ok := snap.SettingGet("proxy.max_retries"); ok {
		if n, err := strconv.Atoi(v); err == nil {
			ps.MaxRetries = n
		}
	}
	if v, ok := snap.SettingGet("proxy.retry_on_status"); ok {
		var codes []int
		if err := json.Unmarshal([]byte(v), &codes); err == nil && len(codes) > 0 {
			set := make(map[int]bool, len(codes))
			for _, c := range codes {
				set[c] = true
			}
			ps.RetryOnStatus = set
		}
	}
	if v, ok := snap.SettingGet("proxy.max_body_bytes"); ok {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			ps.MaxBodyBytes = n
		}
	}
	return ps
}

// httpClientFor returns the HTTP client for an upstream provider, built from
// the current snapshot's settings.proxy timeouts. When useProxy is false (or
// the proxy is disabled/empty in settings) it returns the direct client; when
// useProxy is true and "proxy_enabled" is on, it returns a client routed
// through "proxy_url". Both clients are cached and rebuilt only when their
// resolved configuration (timeouts, proxy URL, HTTP/1.1 force) changes.
func (g *Gateway) httpClientFor(useProxy bool) (*http.Client, error) {
	snap := g.snapshot()
	ps := resolveProxySettings(snap)

	if !useProxy {
		return g.directClient(ps), nil
	}
	enabled, _ := snap.SettingGet("proxy_enabled")
	if !parseBoolSetting(enabled) {
		return g.directClient(ps), nil
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

	cacheKey := proxyURL + "|" + strconv.FormatBool(forceHTTP1) + "|" + ps.RequestTimeout.String() + "|" + ps.ConnectTimeout.String()
	g.clientMu.Lock()
	defer g.clientMu.Unlock()
	if g.proxyClient != nil && g.proxyClientKey == cacheKey {
		return g.proxyClient, nil
	}

	transport := &http.Transport{
		Proxy:               http.ProxyURL(parsed),
		DialContext:         (&net.Dialer{Timeout: ps.ConnectTimeout}).DialContext,
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
	}
	if forceHTTP1 {
		transport.ForceAttemptHTTP2 = false
	} else {
		transport.ForceAttemptHTTP2 = true
	}
	client := &http.Client{Timeout: ps.RequestTimeout, Transport: transport}
	g.proxyClient = client
	g.proxyClientKey = cacheKey
	return client, nil
}

// directClient returns the cached no-proxy client for ps's timeouts, rebuilding
// it only when the resolved timeouts change.
func (g *Gateway) directClient(ps proxySettings) *http.Client {
	cacheKey := ps.RequestTimeout.String() + "|" + ps.ConnectTimeout.String()
	g.clientMu.Lock()
	defer g.clientMu.Unlock()
	if g.client != nil && g.clientKey == cacheKey {
		return g.client
	}
	client := &http.Client{
		Timeout: ps.RequestTimeout,
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: ps.ConnectTimeout}).DialContext,
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 64,
			IdleConnTimeout:     90 * time.Second,
			ForceAttemptHTTP2:   true,
		},
	}
	g.client = client
	g.clientKey = cacheKey
	return client
}

// parseBoolSetting parses a settings-stored boolean (true/1/yes/on).
func parseBoolSetting(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}
