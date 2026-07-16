// Package gateway implements the `nyro gateway` subcommand: the data plane
// that forwards client requests to upstream providers.
package gateway

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/nyroway/nyro/go/internal/bootstrap"
	"github.com/nyroway/nyro/go/internal/config"
	"github.com/nyroway/nyro/go/internal/configsync"
	"github.com/nyroway/nyro/go/internal/configsync/pki"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/proxy"
)

// NewCmd builds the gateway (data-plane) subcommand.
//
// Config sources (exactly one is required):
//   - --config-file: standalone YAML (no admin/DB needed). The snapshot is
//     built once at startup and never refreshed; edit + restart to change
//     config.
//   - --config-server: admin's gRPC endpoint. The gateway subscribes to a
//     long-lived config stream and hot-reloads on every admin config change.
//
// Phase 3 removed the transitional Phase-1 DB-poll default — exactly one of
// --config-file / --config-server must now be set.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Run the data plane (proxy forwarding to upstreams)",
	}
	// 0.0.0.0, not loopback: the gateway's entire job is accepting traffic
	// from real clients (like nginx/envoy/traefik), often from outside its
	// own host/container — unlike the admin control plane, which manages
	// sensitive credentials and defaults to loopback-only on purpose.
	cmd.Flags().String("listen", "0.0.0.0:19530", "listen address for the data plane")
	cmd.Flags().String("config-file", "", "standalone YAML config file (no admin/DB needed)")
	cmd.Flags().String("config-server", "", "admin gRPC config-sync endpoint (host:port) for config hot-reload")
	cmd.Flags().String("config-tls-ca", "", "config-sync mTLS: path to the CA certificate that signs admin/gateway leaf certs (see `nyro ca`); must be set together with --config-tls-cert/-key")
	cmd.Flags().String("config-tls-cert", "", "config-sync mTLS: path to this gateway's client certificate")
	cmd.Flags().String("config-tls-key", "", "config-sync mTLS: path to this gateway's client private key")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		addr, _ := cmd.Flags().GetString("listen")
		cfgPath, _ := cmd.Flags().GetString("config-file")
		configSyncAddr, _ := cmd.Flags().GetString("config-server")
		tlsCA, _ := cmd.Flags().GetString("config-tls-ca")
		tlsCert, _ := cmd.Flags().GetString("config-tls-cert")
		tlsKey, _ := cmd.Flags().GetString("config-tls-key")

		if cfgPath == "" && configSyncAddr == "" {
			return errors.New("exactly one of --config-file or --config-server is required (the legacy DB-poll default was removed in Phase 3)")
		}
		if cfgPath != "" && configSyncAddr != "" {
			return errors.New("--config-file and --config-server are mutually exclusive (set exactly one)")
		}
		if cfgPath != "" {
			for _, name := range []string{"config-tls-ca", "config-tls-cert", "config-tls-key"} {
				if cmd.Flags().Changed(name) {
					return fmt.Errorf("--%s is only valid with --config-server (not --config-file)", name)
				}
			}
		}

		var configTLS *tls.Config
		if configSyncAddr != "" {
			var err error
			configTLS, err = resolveConfigSyncClientTLS(tlsCA, tlsCert, tlsKey)
			if err != nil {
				return err
			}
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		gw, stopConfigSync, obsMgr, err := buildGateway(ctx, cfgPath, configSyncAddr, addr, configTLS)
		if err != nil {
			return err
		}
		if stopConfigSync != nil {
			defer stopConfigSync()
		}
		if obsMgr != nil {
			defer func() {
				shutCtx, shutCancel := context.WithTimeout(context.Background(), shutdownTimeout)
				defer shutCancel()
				if err := obsMgr.Shutdown(shutCtx); err != nil {
					slog.Warn("observability provider shutdown failed", "error", err)
				}
			}()
		}

		engine := proxy.NewRouter(gw)
		return bootstrap.RunServer(engine, addr)
	}
	return cmd
}

// shutdownTimeout bounds the OTel provider flush on graceful exit.
const shutdownTimeout = 5 * time.Second

// configExpiryCheckInterval is how often WatchExpiry re-checks the loaded
// config-sync client certificate once running (it always checks once
// immediately at startup too). See pki.WatchExpiry / ExpiryWarningWindow.
const configExpiryCheckInterval = 24 * time.Hour

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

// buildGateway selects the config source and returns a ready, storage-free
// Gateway plus an optional config-sync client stop function (nil unless
// --config-server) and the observability manager (always non-nil on success —
// telemetry is wired in every mode). It constructs the initial ObsProvider,
// wraps it in a SwappableProvider, and calls RegisterHooks exactly ONCE per
// process (the plugin registry accumulates appends, so re-registering would
// double-emit). In --config-server mode it also registers the manager's
// hot-reload callback on the cache BEFORE starting the config stream, so the
// control-plane-seeded obs settings (which arrive with the first snapshot,
// after this initial build) are applied instead of stuck on the stdout default.
// /readyz reflects cache fill, not storage health.
//
// listenAddr is this gateway's own data-plane --listen (host:port); only its
// port is used, reported over config-sync as Subscribe.service_port so the
// admin's node list can show where each gateway actually serves traffic
// (distinct from the config-sync gRPC connection's ephemeral peer port).
func buildGateway(ctx context.Context, cfgPath, configSyncAddr, listenAddr string, configTLS *tls.Config) (gw *proxy.Gateway, stopConfigSync func(), obs *obsManager, err error) {
	switch {
	case cfgPath != "":
		// Standalone YAML: build the config snapshot directly (no DB). The
		// observability config comes from settings.observability in the YAML
		// file (flattened into the snapshot by internal/config); if the file
		// declares nothing, defaults are logs→stdout, metrics/traces→disabled. See
		// resolveObsConfig — environment variables are never consulted here.
		cfg, missing, err := config.LoadYAML(cfgPath)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, name := range missing {
			slog.Warn("config references an unset environment variable", "var", name)
		}
		snap, err := cfg.BuildSnapshot()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("build snapshot: %w", err)
		}
		cache := &configsync.ConfigCache{}
		cache.Swap(snap)
		gw = proxy.NewGatewayWithCache(cache)

		// Standalone config is static: the snapshot is already in the cache, so
		// the initial resolve sees the real (YAML-declared) obs config and no
		// hot-reload callback is needed — the cache never swaps again.
		obsCfg := resolveObsConfig(cache)
		prov, perr := observability.NewProvider(ctx, obsCfg)
		if perr != nil {
			return nil, nil, nil, fmt.Errorf("observability provider: %w", perr)
		}
		sp := observability.NewSwappableProvider(prov)
		attachObservability(gw, prov, sp)
		return gw, nil, newObsManager(ctx, cache, sp, prov, obsCfg), nil

	case configSyncAddr != "":
		// config-sync hot-reload: empty cache is filled by the stream.
		cache := &configsync.ConfigCache{}
		gw = proxy.NewGatewayWithCache(cache)

		// Build the INITIAL provider from the still-empty cache: it resolves to
		// the fixed default (logs→stdout, metrics/traces disabled). The real obs
		// config (the admin-seeded otlp settings, or anything an operator edits
		// later) arrives with config-sync snapshots and is applied by
		// mgr.rebuild. Registering SetOnSwap BEFORE starting the client is what
		// closes the startup race that previously left the gateway stuck on the
		// stdout default: no snapshot can be published until client.Run starts.
		obsCfg := resolveObsConfig(cache)
		prov, perr := observability.NewProvider(ctx, obsCfg)
		if perr != nil {
			return nil, nil, nil, fmt.Errorf("observability provider: %w", perr)
		}
		sp := observability.NewSwappableProvider(prov)
		attachObservability(gw, prov, sp)
		mgr := newObsManager(ctx, cache, sp, prov, obsCfg)
		cache.SetOnSwap(mgr.rebuild)

		client := configsync.NewConfigClient(configSyncAddr, cache, servicePort(listenAddr), configTLS)
		go func() { _ = client.Run(ctx) }()
		pki.WatchExpiry(ctx, configTLS, configExpiryCheckInterval, func(notAfter time.Time) {
			slog.Warn("config-sync client certificate expiring soon — run `nyro ca sign-gateway` and redistribute before it lapses",
				"not_after", notAfter, "remaining", time.Until(notAfter).Round(time.Hour))
		})
		return gw, nil, mgr, nil

	default:
		// Unreachable: RunE enforces the XOR. Guard anyway.
		return nil, nil, nil, errors.New("exactly one of --config-file or --config-server is required")
	}
}

// resolveConfigSyncClientTLS turns the --config-tls-ca/-cert/-key flags into
// a *tls.Config for dialing --config-server, or nil for plaintext. It uses the
// same all-or-none behavior as resolveConfigSyncServerTLS on the admin side.
//
// There is deliberately no --config-server-name-style override here:
// pki.LoadClientTLS verifies admin's server certificate by SPIFFE identity
// (spiffe://nyro/admin), not by matching its SAN against the dial address,
// so the address used in --config-server (direct, load balancer, k8s
// Service name, IP) never affects verification.
func resolveConfigSyncClientTLS(caPath, certPath, keyPath string) (*tls.Config, error) {
	set := 0
	for _, p := range []string{caPath, certPath, keyPath} {
		if p != "" {
			set++
		}
	}
	switch {
	case set == 3:
		return pki.LoadClientTLS(caPath, certPath, keyPath)
	case set > 0:
		return nil, fmt.Errorf("--config-tls-ca, --config-tls-cert, and --config-tls-key must be set together (got %d of 3)", set)
	default:
		slog.Warn("config-sync client connecting to --config-server in plaintext: no transport encryption or server authentication")
		return nil, nil
	}
}

// attachObservability wires the initial ObsProvider into the Gateway and
// registers the OTel phase hooks ONCE per process against the SwappableProvider.
// Must be called exactly once: the hooks read the current pipeline from sp on
// every request, so a hot-reload swaps sp (obsManager.rebuild) rather than
// re-registering. gw.Obs/gw.Handles are informational only (the proxy dispatch
// path does not read them — telemetry flows entirely through the sp-backed
// hooks) and reflect the initial provider.
func attachObservability(gw *proxy.Gateway, prov *observability.ObsProvider, sp *observability.SwappableProvider) {
	gw.Obs = prov
	gw.Handles = observability.NewHandles(prov.Meter)
	observability.RegisterHooks(sp)
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
// gateway's only two data sources are the config file (standalone) and the
// control-plane push (config-sync); both end up in the same
// ConfigCache/snapshot shape, so both branches of buildGateway call this
// identically. If the snapshot has no observability settings at all (absent
// from the config file, or not yet pushed over config-sync), the fixed
// default in defaultObsGet applies. If the snapshot's observability settings
// fail to load (e.g. an unregistered exporter kind or another validation
// error from observability.LoadConfig), the error is logged and the fixed
// default is used as well — a malformed obs setting must not prevent the
// gateway from starting. Process environment variables are never consulted
// here — a deployment that wants an env var to drive an exporter must
// reference it explicitly inside config.yaml (e.g. endpoint:
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
// crashing the gateway.
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

// newMetricsServer builds (without starting) the http.Server the obsManager
// runs for prometheus scraping: obs.PromHandler mounted at path on a fresh mux,
// listening on obs.PromListen. path defaults to "/metrics" when empty —
// defensive, since observability.LoadConfig always fills the prometheus
// exporter's "path" field from its registry default when the signal is
// configured, but NewProvider (and therefore ObsProvider) can also be built
// directly from a hand-assembled ObsConfig that skipped LoadConfig. Split out
// from the obsManager's start path so tests can inspect routing/Addr without
// binding a real network port.
func newMetricsServer(obs *observability.ObsProvider, path string) *http.Server {
	if path == "" {
		path = "/metrics"
	}
	mux := http.NewServeMux()
	mux.Handle(path, obs.PromHandler)
	return &http.Server{Addr: obs.PromListen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
}
