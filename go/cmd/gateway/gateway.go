// Package gateway implements the `nyro gateway` subcommand: the data plane
// that forwards client requests to upstream providers.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/nyroway/nyro/go/internal/auth"
	"github.com/nyroway/nyro/go/internal/bootstrap"
	"github.com/nyroway/nyro/go/internal/config"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/proxy"
	"github.com/nyroway/nyro/go/internal/xds"
)

// NewCmd builds the gateway (data-plane) subcommand.
//
// Config sources (exactly one is required):
//   - --config: standalone YAML (no admin/DB needed). The snapshot is built once
//     at startup and never refreshed; edit + restart to change config.
//   - --xds-addr: admin's gRPC endpoint. The gateway subscribes to a long-lived
//     config stream and hot-reloads on every admin config change.
//
// Phase 3 removed the transitional Phase-1 DB-poll default — exactly one of
// --config / --xds-addr must now be set.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Run the data plane (proxy forwarding to upstreams)",
	}
	cmd.Flags().String("addr", "127.0.0.1:19530", "listen address for the data plane")
	cmd.Flags().String("config", "", "standalone YAML config file (no admin/DB needed)")
	cmd.Flags().String("xds-addr", "", "admin gRPC xDS endpoint (host:port) for config hot-reload")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		addr, _ := cmd.Flags().GetString("addr")
		cfgPath, _ := cmd.Flags().GetString("config")
		xdsAddr, _ := cmd.Flags().GetString("xds-addr")

		if cfgPath == "" && xdsAddr == "" {
			return errors.New("exactly one of --config or --xds-addr is required (the legacy DB-poll default was removed in Phase 3)")
		}
		if cfgPath != "" && xdsAddr != "" {
			return errors.New("--config and --xds-addr are mutually exclusive (set exactly one)")
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		gw, stopXDS, obsProvider, err := buildGateway(ctx, cfgPath, xdsAddr)
		if err != nil {
			return err
		}
		if stopXDS != nil {
			defer stopXDS()
		}
		if obsProvider != nil {
			defer func() {
				shutCtx, shutCancel := context.WithTimeout(context.Background(), shutdownTimeout)
				defer shutCancel()
				if err := obsProvider.Shutdown(shutCtx); err != nil {
					slog.Warn("observability provider shutdown failed", "error", err)
				}
			}()
		}

		reg := auth.NewRegistry()
		bootstrap.RegisterDrivers(reg)
		gw.SetDriverRegistry(reg)

		gw.StartOAuthRefreshLoop(ctx)

		engine := proxy.NewRouter(gw)
		return bootstrap.RunServer(engine, addr)
	}
	return cmd
}

// shutdownTimeout bounds the OTel provider flush on graceful exit.
const shutdownTimeout = 5 * time.Second

// buildGateway selects the config source and returns a ready, storage-free
// Gateway plus an optional xDS-client stop function (nil unless --xds-addr) and
// the OTel provider (always non-nil on success — telemetry is wired in every
// mode). It constructs the ObsProvider + Handles and calls RegisterHooks exactly
// ONCE per process (the plugin registry accumulates appends, so re-registering
// would double-emit). /readyz reflects cache fill, not storage health.
func buildGateway(ctx context.Context, cfgPath, xdsAddr string) (gw *proxy.Gateway, stopXDS func(), obs *observability.ObsProvider, err error) {
	switch {
	case cfgPath != "":
		// Standalone YAML: build the config snapshot directly (no DB). The
		// observability config comes from env (OTEL_EXPORTER_OTLP_ENDPOINT /
		// OTEL_*_EXPORTER); defaults are logs→stdout, metrics/traces→none.
		cfg, err := config.LoadYAML(cfgPath)
		if err != nil {
			return nil, nil, nil, err
		}
		snap, err := cfg.BuildSnapshot()
		if err != nil {
			return nil, nil, nil, fmt.Errorf("build snapshot: %w", err)
		}
		cache := &xds.ConfigCache{}
		cache.Swap(snap)
		gw = proxy.NewGatewayWithCache(cache)

		obsCfg := observability.LoadConfig(standaloneObsGet)
		prov, perr := observability.NewProvider(ctx, obsCfg)
		if perr != nil {
			return nil, nil, nil, fmt.Errorf("observability provider: %w", perr)
		}
		attachObservability(gw, prov)
		return gw, nil, prov, nil

	case xdsAddr != "":
		// xDS hot-reload: empty cache is filled by the stream. Observability
		// config is read from the cache snapshot (published by the admin) once
		// it arrives; for the provider we read synchronously at startup and
		// accept that env overrides apply. The default is all→otlp via
		// obs_otlp_endpoint from settings; env can override.
		cache := &xds.ConfigCache{}
		client := xds.NewConfigClient(xdsAddr, cache)
		go func() { _ = client.Run(ctx) }()
		gw = proxy.NewGatewayWithCache(cache)

		// Read obs settings from the cache snapshot when present; fall back to
		// env so the provider can be built before the first xDS push lands.
		obsCfg := observability.LoadConfig(cacheObsGet(cache))
		if obsCfg.Sink == "" && obsCfg.LogsSink == "" && obsCfg.MetricsSink == "" && obsCfg.TracesSink == "" && obsCfg.OTLPEndpoint == "" {
			// No xDS settings yet → env-based defaults (all→otlp if
			// OTEL_EXPORTER_OTLP_ENDPOINT is set, else none).
			obsCfg = observability.LoadConfig(standaloneObsGet)
		}
		prov, perr := observability.NewProvider(ctx, obsCfg)
		if perr != nil {
			return nil, nil, nil, fmt.Errorf("observability provider: %w", perr)
		}
		attachObservability(gw, prov)
		return gw, nil, prov, nil

	default:
		// Unreachable: RunE enforces the XOR. Guard anyway.
		return nil, nil, nil, errors.New("exactly one of --config or --xds-addr is required")
	}
}

// attachObservability wires the ObsProvider + Handles into the Gateway and
// registers the OTel phase hooks ONCE per process. Safe to call exactly once.
func attachObservability(gw *proxy.Gateway, prov *observability.ObsProvider) {
	gw.Obs = prov
	gw.Handles = observability.NewHandles(prov.Meter)
	observability.RegisterHooks(prov.Tracer, prov.Logger, gw.Handles)
}

// standaloneObsGet maps OTel-standard env vars onto the observability settings
// keys, so the standalone YAML path (which has no settings store) can still
// configure the sinks via env. Defaults: logs→stdout, metrics/traces→none. If
// OTEL_EXPORTER_OTLP_ENDPOINT is set, every signal whose exporter resolves to
// "otlp" routes there; per-signal OTEL_<SIGNAL>_EXPORTER=none|console|otlp wins
// over the global endpoint default.
func standaloneObsGet(key string) (string, error) {
	// Per-key mapping onto the obs_* settings keys that LoadConfig reads.
	switch key {
	case "obs_otlp_endpoint":
		return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), nil
	case "obs_logs_sink":
		return resolveSinkEnv("OTEL_LOGS_EXPORTER", "stdout"), nil
	case "obs_metrics_sink":
		return resolveSinkEnv("OTEL_METRICS_EXPORTER", "none"), nil
	case "obs_traces_sink":
		return resolveSinkEnv("OTEL_TRACES_EXPORTER", "none"), nil
	case "obs_sink":
		// No global default in standalone mode (per-signal defaults above win).
		return "", nil
	}
	return "", nil
}

// cacheObsGet returns a get-func that reads obs_* settings from the xDS-published
// snapshot, falling back to "" (absent) before the first push lands.
func cacheObsGet(cache *xds.ConfigCache) func(string) (string, error) {
	return func(key string) (string, error) {
		if s := cache.Load(); s != nil {
			if v, ok := s.SettingGet(key); ok {
				return v, nil
			}
		}
		return "", nil
	}
}

// resolveSinkEnv maps an OTEL_<SIGNAL>_EXPORTER value onto an obs sink name
// (console≡stdout), applying def when the env var is unset/empty.
func resolveSinkEnv(envVar, def string) string {
	v := os.Getenv(envVar)
	switch v {
	case "":
		// If an OTLP endpoint is configured and the signal didn't pin its own
		// exporter, follow the endpoint (otlp). Otherwise keep the per-signal
		// default.
		if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
			return "otlp"
		}
		return def
	case "otlp":
		return "otlp"
	case "console":
		return "stdout"
	case "none":
		return "none"
	}
	return def
}
