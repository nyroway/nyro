// Package dataplane assembles a ready-to-serve proxy.Gateway from one of the
// two config sources, together with its observability pipeline.
//
// It exists so that `nyro proxy` (standalone data plane) and `nyro serve`
// (control plane with an embedded data plane) share ONE assembly path. The
// embedded case differs only in how its config-sync client dials — over an
// in-process pipe (configsync.ServeInProcess) instead of TCP — so an embedded
// data plane exercises the same client, cache, snapshot decoding and router
// wiring as a remote one. Reading storage directly in the embedded case would
// have been shorter but would have created a second config path free to drift
// from the distributed one.
//
// Layer: 3 (serve) — assembles the data plane for both `nyro proxy` and the
// embedded plane inside `nyro serve`; may import any lower layer. Nothing
// below layer 3 may import it.
package dataplane

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"google.golang.org/grpc"

	"github.com/nyroway/nyro/go/internal/config"
	"github.com/nyroway/nyro/go/internal/configsync"
	"github.com/nyroway/nyro/go/internal/configsync/pki"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/proxy"
)

// certExpiryCheckInterval is how often WatchExpiry re-checks the loaded
// config-sync client certificate once running (it always checks once
// immediately at startup too). See pki.WatchExpiry / ExpiryWarningWindow.
const certExpiryCheckInterval = 24 * time.Hour

// Options selects the data plane's config source and how it describes itself
// to the control plane. Exactly one of ConfigPath or SyncTarget must be set.
type Options struct {
	// ConfigPath is a standalone YAML config file: the snapshot is built once
	// at startup and never refreshed. No control plane or database involved.
	ConfigPath string

	// SyncTarget is the config-sync dial target. For a standalone proxy this
	// is the operator's --server host:port; for an embedded data plane it is
	// configsync.InProcessTarget.
	SyncTarget string

	// SyncTLS is the mTLS config for dialing SyncTarget, or nil for plaintext.
	// Ignored when SyncDialOpts is set (an in-process pipe has no TLS).
	SyncTLS *tls.Config

	// SyncToken is the join token presented when subscribing, or empty for
	// none. Never set for the in-process channel, which has no transport to
	// authenticate across.
	SyncToken string

	// SyncDialOpts, when non-empty, replaces the client's dial options
	// wholesale. This is how the in-process channel is selected: pass what
	// configsync.ServeInProcess returned. Leave nil for a normal TCP dial.
	SyncDialOpts []grpc.DialOption

	// ListenAddr is this data plane's own listen address (host:port). Only its
	// port is used, reported over config-sync as Subscribe.service_port so the
	// control plane's node list can show where each node actually serves
	// traffic (distinct from the config-sync connection's ephemeral peer port).
	ListenAddr string
}

// Build returns a ready, storage-free Gateway plus its observability manager
// (always non-nil on success — telemetry is wired in every mode).
//
// It constructs the initial ObsProvider, wraps it in a SwappableProvider, and
// points the OTel telemetry Stage at it. In config-sync mode it registers the
// manager's hot-reload callback on the cache BEFORE starting the config
// stream, so the control-plane-seeded obs settings — which arrive with the
// first snapshot, after this initial build — are applied instead of being
// stuck on the stdout default.
//
// The returned Gateway's /readyz reflects cache fill, not storage health. The
// config-sync client stops when ctx is cancelled; there is no separate stop
// function to call.
func Build(ctx context.Context, opts Options) (*proxy.Gateway, *ObsManager, error) {
	switch {
	case opts.ConfigPath != "":
		// Standalone YAML: build the config snapshot directly (no DB). The
		// observability config comes from settings.observability in the YAML
		// file (flattened into the snapshot by internal/config); if the file
		// declares nothing, defaults are logs→stdout, metrics/traces→disabled.
		// See resolveObsConfig — environment variables are never consulted.
		cfg, missing, err := config.LoadYAML(opts.ConfigPath)
		if err != nil {
			return nil, nil, err
		}
		for _, name := range missing {
			slog.Warn("config references an unset environment variable", "var", name)
		}
		snap, err := cfg.BuildSnapshot()
		if err != nil {
			return nil, nil, fmt.Errorf("build snapshot: %w", err)
		}
		cache := &configsync.ConfigCache{}
		cache.Swap(snap)
		gw := proxy.NewGatewayWithCache(cache)

		// Standalone config is static: the snapshot is already in the cache, so
		// the initial resolve sees the real (YAML-declared) obs config and no
		// hot-reload callback is needed — the cache never swaps again.
		obsCfg := resolveObsConfig(cache)
		prov, err := observability.NewProvider(ctx, obsCfg)
		if err != nil {
			return nil, nil, fmt.Errorf("observability provider: %w", err)
		}
		sp := observability.NewSwappableProvider(prov)
		attachObservability(gw, prov, sp)
		return gw, newObsManager(ctx, cache, sp, prov, obsCfg), nil

	case opts.SyncTarget != "":
		// config-sync hot-reload: empty cache is filled by the stream.
		cache := &configsync.ConfigCache{}
		gw := proxy.NewGatewayWithCache(cache)

		// Build the INITIAL provider from the still-empty cache: it resolves to
		// the fixed default (logs→stdout, metrics/traces disabled). The real obs
		// config (the control-plane-seeded otlp settings, or anything an
		// operator edits later) arrives with config-sync snapshots and is
		// applied by mgr.rebuild. Registering SetOnSwap BEFORE starting the
		// client is what closes the startup race that previously left the data
		// plane stuck on the stdout default: no snapshot can be published until
		// client.Run starts.
		obsCfg := resolveObsConfig(cache)
		prov, err := observability.NewProvider(ctx, obsCfg)
		if err != nil {
			return nil, nil, fmt.Errorf("observability provider: %w", err)
		}
		sp := observability.NewSwappableProvider(prov)
		attachObservability(gw, prov, sp)
		mgr := newObsManager(ctx, cache, sp, prov, obsCfg)
		cache.SetOnSwap(mgr.rebuild)

		client := configsync.NewConfigClient(opts.SyncTarget, cache, servicePort(opts.ListenAddr), opts.SyncTLS)
		if len(opts.SyncDialOpts) > 0 {
			client.SetDialOptions(opts.SyncDialOpts...)
		}
		client.SetJoinToken(opts.SyncToken)
		go func() { _ = client.Run(ctx) }()

		// Only meaningful for a real TLS dial; WatchExpiry no-ops on a nil
		// config, which is what the in-process channel always passes.
		pki.WatchExpiry(ctx, opts.SyncTLS, certExpiryCheckInterval, func(notAfter time.Time) {
			slog.Warn("config-sync client certificate expiring soon — run `nyro tool ca sign-proxy` and redistribute before it lapses",
				"not_after", notAfter, "remaining", time.Until(notAfter).Round(time.Hour))
		})
		return gw, mgr, nil

	default:
		// Unreachable from the CLI, which enforces the XOR. Guard anyway.
		return nil, nil, errors.New("dataplane: exactly one of ConfigPath or SyncTarget is required")
	}
}

// servicePort extracts the port from a host:port listen address for
// node-visibility reporting. Returns "" (best-effort, never fatal) if addr
// isn't in host:port form.
func servicePort(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
}

// attachObservability wires the initial ObsProvider into the Gateway and points
// the telemetry Stage at the SwappableProvider. gw.Obs/gw.Handles are
// informational only (the proxy dispatch path does not read them — telemetry
// flows entirely through the sp-backed Stage) and reflect the initial provider.
//
// RegisterObservability is idempotent and re-pointable, which is what makes it
// safe for one process to assemble more than one data plane over its lifetime.
func attachObservability(gw *proxy.Gateway, prov *observability.ObsProvider, sp *observability.SwappableProvider) {
	gw.Obs = prov
	gw.Handles = observability.NewHandles(prov.Meter)
	observability.RegisterObservability(sp)
}

// cacheObsGet returns a get-func that reads obs_* settings from the
// config-sync-published snapshot, falling back to "" (absent) before the
// first push lands.
func cacheObsGet(cache *configsync.ConfigCache) func(string) (string, error) {
	return func(key string) (string, error) {
		if s := cache.Load(); s != nil {
			if v, ok := s.SettingGet(key); ok {
				return v, nil
			}
		}
		return "", nil
	}
}

// resolveObsConfig reads observability settings from the config snapshot — the
// data plane's only two data sources are the config file (standalone) and the
// control-plane push (config-sync); both end up in the same
// ConfigCache/snapshot shape, so both branches of Build call this identically.
// If the snapshot has no observability settings at all (absent from the config
// file, or not yet pushed over config-sync), the fixed default in defaultObsGet
// applies. If the snapshot's observability settings fail to load (e.g. an
// unregistered exporter kind or another validation error from
// observability.LoadConfig), the error is logged and the fixed default is used
// as well — a malformed obs setting must not prevent the data plane from
// starting. Process environment variables are never consulted here — a
// deployment that wants an env var to drive an exporter must reference it
// explicitly inside config.yaml (e.g. endpoint:
// "${OTEL_EXPORTER_OTLP_ENDPOINT}"), which config.LoadYAML's ${VAR} expansion
// already handles.
func resolveObsConfig(cache *configsync.ConfigCache) observability.ObsConfig {
	obsCfg, err := observability.LoadConfig(cacheObsGet(cache))
	if err != nil {
		slog.Error("observability config from snapshot is invalid; falling back to defaults", "error", err)
		return defaultObsConfig()
	}
	if obsConfigIsEmpty(obsCfg) {
		return defaultObsConfig()
	}
	return obsCfg
}

// obsConfigIsEmpty reports whether cfg carries no observability settings at
// all: every signal's exporter kind is unset AND every signal's otlp
// endpoint is unset. This is the "nothing was configured" case that
// resolveObsConfig falls back to defaultObsGet for. In practice
// observability.LoadConfig never sets Params["endpoint"] without also
// setting Kind (Kind comes from the separate obs_<signal>_exporter key), so
// the endpoint checks are currently implied by the Kind checks — they are
// kept explicit anyway to match the "exporter kind empty AND endpoint empty"
// contract exactly, defensively, in case that invariant ever changes.
func obsConfigIsEmpty(cfg observability.ObsConfig) bool {
	return cfg.Logs.Kind == "" && cfg.Metrics.Kind == "" && cfg.Traces.Kind == "" &&
		cfg.Logs.Params["endpoint"] == "" && cfg.Metrics.Params["endpoint"] == "" && cfg.Traces.Params["endpoint"] == ""
}

// defaultObsConfig resolves the fixed default (logs→stdout, metrics/traces
// disabled) via defaultObsGet. defaultObsGet's values are fixed and known to
// be valid (stdout is registered for every signal that uses it, and empty
// disables a signal), so LoadConfig should never fail on them; if it somehow
// does, that indicates a broken exporter registry rather than a bad runtime
// config, and the safest fallback is every signal disabled rather than
// crashing the data plane.
func defaultObsConfig() observability.ObsConfig {
	cfg, err := observability.LoadConfig(defaultObsGet)
	if err != nil {
		slog.Error("observability default config failed to load (should be unreachable)", "error", err)
		return observability.ObsConfig{}
	}
	return cfg
}

// defaultObsGet supplies fixed defaults (logs→stdout, metrics/traces
// disabled) when neither the config file nor a control-plane push declares
// any observability setting. There is no "none" sentinel — an unset/empty
// obs_<signal>_exporter means the signal is disabled. It never reads the
// process environment.
func defaultObsGet(key string) (string, error) {
	switch key {
	case "obs_logs_exporter":
		return "stdout", nil
	case "obs_metrics_exporter", "obs_traces_exporter":
		return "", nil
	}
	return "", nil
}

// newMetricsServer builds (without starting) the http.Server the ObsManager
// runs for prometheus scraping: obs.PromHandler mounted at path on a fresh mux,
// listening on obs.PromListen. path defaults to "/metrics" when empty —
// defensive, since observability.LoadConfig always fills the prometheus
// exporter's "path" field from its registry default when the signal is
// configured, but NewProvider (and therefore ObsProvider) can also be built
// directly from a hand-assembled ObsConfig that skipped LoadConfig. Split out
// from the ObsManager's start path so tests can inspect routing/Addr without
// binding a real network port.
func newMetricsServer(obs *observability.ObsProvider, path string) *http.Server {
	if path == "" {
		path = "/metrics"
	}
	mux := http.NewServeMux()
	mux.Handle(path, obs.PromHandler)
	return &http.Server{Addr: obs.PromListen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
}
