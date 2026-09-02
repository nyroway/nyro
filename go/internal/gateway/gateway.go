package gateway

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	providerhttp "github.com/nyroway/nyro/go/internal/llm/provider/httptransport"
	"github.com/nyroway/nyro/go/internal/llm/routing"
	"github.com/nyroway/nyro/go/internal/pipeline"
	"github.com/nyroway/nyro/go/internal/quota"
	"github.com/nyroway/nyro/go/internal/telemetry"
)

// Gateway holds the runtime dependencies for dispatching requests. Config reads
// (upstreams, routes, consumer keys, proxy settings) go through Cache, an
// in-memory snapshot published by config-sync or built once from YAML; Quota is
// a stable facade over the configured quota State backend. The gateway holds NO storage handle:
// per-request telemetry flows through the OTel telemetry Stage (Obs/Handles,
// pointed at a provider once at startup) → configured sink
// (none/stdout/otlp). Router selects among a route's upstreams and tracks
// failover.
type Gateway struct {
	Cache     *configsnapshot.Cache
	Quota     *quota.Switch
	Router    *routing.Router
	Protocols *protocol.Catalog
	Providers *provider.Catalog

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

	transportMu        sync.Mutex
	directTransport    provider.Transport
	directTransportKey string
	proxyTransport     provider.Transport
	proxyTransportKey  string

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
func NewGateway(protocols *protocol.Catalog, providers *provider.Catalog) *Gateway {
	return NewGatewayWithCache(
		&configsnapshot.Cache{},
		quota.NewSwitch(quota.NewMemory()),
		protocols,
		providers,
	)
}

// NewGatewayWithCache builds a Gateway using a caller-provided snapshot Cache
// (standalone-YAML and config-sync path): the caller builds the snapshot from YAML or
// from the config-sync stream and swaps it in, so the gateway never needs storage for
// config. Obs/Handles are attached by gateway/runtime after construction.
func NewGatewayWithCache(cache *configsnapshot.Cache, quotas *quota.Switch, protocols *protocol.Catalog, providers *provider.Catalog) *Gateway {
	if cache == nil {
		cache = &configsnapshot.Cache{}
	}
	if quotas == nil {
		quotas = quota.NewUnavailableSwitch()
	}
	return &Gateway{
		Cache:     cache,
		Quota:     quotas,
		Router:    routing.New(),
		Protocols: protocols,
		Providers: providers,
	}
}

// Ready requires both a published configuration and healthy quota State.
func (g *Gateway) Ready() bool {
	return g != nil && g.Cache != nil && g.Cache.Ready() && g.Quota != nil && g.Quota.Ready()
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

// providerTransportFor returns the cached outbound transport selected by the
// upstream proxy URL and current timeout settings. HTTP construction lives in
// provider/httptransport; Gateway only owns lifecycle and caching.
func (g *Gateway) providerTransportFor(proxyURL string) (provider.Transport, error) {
	snap := g.snapshot()
	ps := resolveProxySettings(snap)
	proxyURL = providerhttp.NormalizeProxyURL(proxyURL)
	cacheKey := proxyURL + "|" + ps.RequestTimeout.String() + "|" + ps.ConnectTimeout.String()

	g.transportMu.Lock()
	defer g.transportMu.Unlock()
	if proxyURL == "" && g.directTransport != nil && g.directTransportKey == cacheKey {
		return g.directTransport, nil
	}
	if proxyURL != "" && g.proxyTransport != nil && g.proxyTransportKey == cacheKey {
		return g.proxyTransport, nil
	}
	transport, err := providerhttp.New(providerhttp.Config{
		RequestTimeout: ps.RequestTimeout,
		ConnectTimeout: ps.ConnectTimeout,
		ProxyURL:       proxyURL,
		RoundTripper:   g.UpstreamTransport,
	})
	if err != nil {
		return nil, err
	}
	if proxyURL == "" {
		if g.directTransport != nil {
			g.directTransport.CloseIdleConnections()
		}
		g.directTransport = transport
		g.directTransportKey = cacheKey
	} else {
		if g.proxyTransport != nil {
			g.proxyTransport.CloseIdleConnections()
		}
		g.proxyTransport = transport
		g.proxyTransportKey = cacheKey
	}
	return transport, nil
}

// CloseIdleConnections releases idle outbound connections and clears both
// bounded transport cache slots. Active requests are unaffected by net/http's
// CloseIdleConnections contract.
func (g *Gateway) CloseIdleConnections() {
	if g == nil {
		return
	}
	g.transportMu.Lock()
	defer g.transportMu.Unlock()
	if g.directTransport != nil {
		g.directTransport.CloseIdleConnections()
		g.directTransport = nil
		g.directTransportKey = ""
	}
	if g.proxyTransport != nil {
		g.proxyTransport.CloseIdleConnections()
		g.proxyTransport = nil
		g.proxyTransportKey = ""
	}
}
