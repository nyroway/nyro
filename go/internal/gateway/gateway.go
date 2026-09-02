package gateway

import (
	"context"
	"net/http"
	"sync"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	llmpipeline "github.com/nyroway/nyro/go/internal/llm/pipeline"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/provider"
	providerhttp "github.com/nyroway/nyro/go/internal/llm/provider/httptransport"
	"github.com/nyroway/nyro/go/internal/llm/routing"
	llmruntime "github.com/nyroway/nyro/go/internal/llm/runtime"
	"github.com/nyroway/nyro/go/internal/quota"
	"github.com/nyroway/nyro/go/internal/telemetry"
)

// Gateway holds the runtime dependencies for dispatching requests. Config reads
// (upstreams, routes, consumer keys, proxy settings) go through Cache, an
// in-memory snapshot published by config-sync or built once from YAML; Quota is
// a stable facade over the configured quota State backend. The gateway holds NO storage handle:
// per-request telemetry flows through the OTel telemetry Phase (Obs/Handles,
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
	// the telemetry Phase simply stays inert so nothing is emitted).
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
	runtimeMu          sync.Mutex
	runtimeSnapshot    *configsnapshot.Snapshot
	llmRuntime         *llmruntime.Runtime

	// observePhase is a test seam for the mandatory Observe position.
	// Production leaves it nil and uses telemetry.NewRegisteredPhase.
	observePhase llmpipeline.Phase
	// preDispatchPhases exercises the ordered optional slot during the
	// Gateway-to-Runtime transition. Production currently leaves it empty.
	preDispatchPhases []llmpipeline.Phase
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

type proxySettings = llmruntime.Settings

// resolveProxySettings reads settings.proxy.* from the snapshot (flattened by
// internal/config.flattenSettings under the proxy.* dot-key namespace),
// applying the config-schema plan's example defaults for anything absent or
// unparseable.
func resolveProxySettings(snap *configsnapshot.Snapshot) proxySettings {
	return llmruntime.SettingsFromSnapshot(snap)
}

// providerTransportFor returns the cached outbound transport selected by the
// upstream proxy URL and current timeout settings. HTTP construction lives in
// provider/httptransport; Gateway only owns lifecycle and caching.
func (g *Gateway) providerTransportFor(proxyURL string) (provider.Transport, error) {
	return g.providerTransportForSettings(proxyURL, resolveProxySettings(g.snapshot()))
}

func (g *Gateway) providerTransportForSettings(proxyURL string, ps proxySettings) (provider.Transport, error) {
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

// runtimeFor binds one Runtime to each immutable Snapshot observed during the
// Gateway transition. Task 10 replaces this small cache with generation-owned
// construction and activation.
func (g *Gateway) runtimeFor(snapshot *configsnapshot.Snapshot) (*llmruntime.Runtime, error) {
	g.runtimeMu.Lock()
	defer g.runtimeMu.Unlock()
	if g.runtimeSnapshot == snapshot && g.llmRuntime != nil {
		return g.llmRuntime, nil
	}
	settings := resolveProxySettings(snapshot)
	runtime, err := llmruntime.New(llmruntime.Config{
		Snapshot:    snapshot,
		Protocols:   g.Protocols,
		Providers:   g.Providers,
		Router:      g.Router,
		Transport:   gatewayProviderTransport{gateway: g, settings: settings},
		Quota:       g.Quota,
		Observe:     g.observePhaseOrDefault(),
		PreDispatch: g.preDispatchPhases,
	})
	if err != nil {
		return nil, err
	}
	g.runtimeSnapshot = snapshot
	g.llmRuntime = runtime
	return runtime, nil
}

func (g *Gateway) observePhaseOrDefault() llmpipeline.Phase {
	if g.observePhase != nil {
		return g.observePhase
	}
	return telemetry.NewRegisteredPhase()
}

type gatewayProviderTransport struct {
	gateway  *Gateway
	settings proxySettings
}

func (transport gatewayProviderTransport) Do(ctx context.Context, request provider.Request) (*provider.Response, error) {
	selected, err := transport.gateway.providerTransportForSettings(request.ProxyURL, transport.settings)
	if err != nil {
		return nil, err
	}
	return selected.Do(ctx, request)
}

func (gatewayProviderTransport) CloseIdleConnections() {}

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
