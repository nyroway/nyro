package gateway

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/pipeline"
	"github.com/nyroway/nyro/go/internal/quota"
	"github.com/nyroway/nyro/go/internal/router"
	"github.com/nyroway/nyro/go/internal/telemetry"
)

// Gateway holds the runtime dependencies for dispatching requests. Config reads
// (upstreams, routes, consumer keys, proxy settings) go through Cache, an
// in-memory snapshot published by config-sync or built once from YAML; Quota is the
// in-memory consumer-quota sliding window. The gateway holds NO storage handle:
// per-request telemetry flows through the OTel telemetry Stage (Obs/Handles,
// pointed at a provider once at startup) → configured sink
// (none/stdout/otlp). Router selects among a route's upstreams and tracks
// failover.
type Gateway struct {
	Cache  *configsnapshot.Cache
	Quota  *quota.Counter
	Router *router.Router

	// Obs is the OTel provider (logger/meter/tracer). Populated by the data
	// plane once at startup; nil in unit tests (the dispatcher still works,
	// the telemetry Stage simply stays inert so nothing is emitted).
	Obs     *telemetry.Provider
	Handles *telemetry.Handles

	// UpstreamTransport, when non-nil, replaces the RoundTripper of every
	// outbound upstream client. Production leaves it nil (real *http.Transport
	// is built per proxySettings). It exists purely as a test seam: the
	// protocol-conversion harness (tests/conversion) injects a go-vcr
	// recorder here to record/replay real provider interactions offline. Since
	// nil is the production default, behaviour is unchanged when unset.
	UpstreamTransport http.RoundTripper

	clientMu       sync.Mutex
	client         *http.Client // direct (no proxy) client, rebuilt when timeouts change
	clientKey      string
	proxyClient    *http.Client
	proxyClientKey string

	// OuterStages run before the built-in Stages, wrapping the whole chain.
	// Production leaves this nil; tests use it to observe an exchange the
	// way the telemetry Stage does — from outside, so a short circuit
	// further in is still seen on the way out. Set it before the first
	// request: the chain is built once.
	OuterStages []pipeline.Stage

	chainOnce  sync.Once
	stageChain *pipeline.Chain
}

// chain returns the request Stage chain, building it once on first use.
//
// Order is the contract. The telemetry Stage is outermost so its deferred emit
// runs after every other Stage has unwound — that is what reports a request
// rejected by access control, or one that never reached a backend. route comes
// before access because the access check is per-route, and quota is innermost
// of the cross-cutting Stages so it records only exchanges that got past auth.
func (g *Gateway) chain() *pipeline.Chain {
	g.chainOnce.Do(func() {
		stages := append([]pipeline.Stage(nil), g.OuterStages...)
		stages = append(stages,
			telemetry.NewRegisteredStage(),
			routeStage{gw: g},
			accessStage{gw: g},
			quotaStage{gw: g},
		)
		g.stageChain = pipeline.NewChain(stages...)
	})
	return g.stageChain
}

// NewGateway builds a Gateway with a fresh, empty snapshot Cache. Tests use this
// and populate the cache directly via Cache.LoadAndSwap / Cache.Swap. Production
// callers use NewGatewayWithCache through gateway/runtime with a snapshot built
// from YAML or filled by the config-sync stream.
func NewGateway() *Gateway {
	return NewGatewayWithCache(&configsnapshot.Cache{})
}

// NewGatewayWithCache builds a Gateway using a caller-provided snapshot Cache
// (standalone-YAML and config-sync path): the caller builds the snapshot from YAML or
// from the config-sync stream and swaps it in, so the gateway never needs storage for
// config. Obs/Handles are attached by gateway/runtime after construction.
func NewGatewayWithCache(cache *configsnapshot.Cache) *Gateway {
	return &Gateway{
		Cache:  cache,
		Quota:  quota.New(),
		Router: router.New(),
	}
}

// snapshot returns the current config snapshot, falling back to an empty one so
// callers never see a nil pointer (readers on an empty snapshot simply report
// "not found", matching storage behavior before any config is loaded).
func (g *Gateway) snapshot() *configsnapshot.Snapshot {
	if s := g.Cache.Load(); s != nil {
		return s
	}
	return &configsnapshot.Snapshot{}
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
func resolveProxySettings(snap *configsnapshot.Snapshot) proxySettings {
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

// httpClientFor returns the HTTP client for an upstream's outbound requests,
// built from the current snapshot's settings.proxy timeouts. proxyURL is the
// upstream's own Upstream.ProxyURL (empty means no proxy — the direct client
// is returned). A non-empty proxyURL that isn't a valid absolute URL
// (scheme+host) — including any leftover pre-fix "enabled" sentinel value,
// which parses "successfully" as a bare relative path with no scheme/host —
// falls back to the direct client rather than failing the request outright.
// Both clients are cached and rebuilt only when their resolved configuration
// (timeouts, proxy URL) changes.
func (g *Gateway) httpClientFor(proxyURL string) (*http.Client, error) {
	snap := g.snapshot()
	ps := resolveProxySettings(snap)

	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return g.directClient(ps), nil
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return g.directClient(ps), nil
	}

	cacheKey := proxyURL + "|" + ps.RequestTimeout.String() + "|" + ps.ConnectTimeout.String()
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
		ForceAttemptHTTP2:   true,
	}
	client := &http.Client{Timeout: ps.RequestTimeout, Transport: g.transportOr(transport)}
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
		Transport: g.transportOr(&http.Transport{
			DialContext:         (&net.Dialer{Timeout: ps.ConnectTimeout}).DialContext,
			MaxIdleConns:        256,
			MaxIdleConnsPerHost: 64,
			IdleConnTimeout:     90 * time.Second,
			ForceAttemptHTTP2:   true,
		}),
	}
	g.client = client
	g.clientKey = cacheKey
	return client
}

// transportOr returns g.UpstreamTransport when set (test seam), else def. It
// lets the conversion harness swap in a go-vcr recorder without touching the
// production transport construction. See the UpstreamTransport field.
func (g *Gateway) transportOr(def http.RoundTripper) http.RoundTripper {
	if g.UpstreamTransport != nil {
		return g.UpstreamTransport
	}
	return def
}
