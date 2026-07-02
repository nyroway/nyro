// Package gateway implements the `nyro gateway` subcommand: the data plane
// that forwards client requests to upstream providers.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

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
		// observability config comes from settings.observability in the YAML
		// file (flattened into the snapshot by internal/config); if the file
		// declares nothing, defaults are logs→stdout, metrics/traces→none. See
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
		cache := &xds.ConfigCache{}
		cache.Swap(snap)
		gw = proxy.NewGatewayWithCache(cache)

		obsCfg := resolveObsConfig(cache)
		prov, perr := observability.NewProvider(ctx, obsCfg)
		if perr != nil {
			return nil, nil, nil, fmt.Errorf("observability provider: %w", perr)
		}
		attachObservability(gw, prov)
		return gw, nil, prov, nil

	case xdsAddr != "":
		// xDS hot-reload: empty cache is filled by the stream. Observability
		// config is read from the cache snapshot (published by the admin) once
		// it arrives.
		cache := &xds.ConfigCache{}
		client := xds.NewConfigClient(xdsAddr, cache)
		go func() { _ = client.Run(ctx) }()
		gw = proxy.NewGatewayWithCache(cache)

		// Read obs settings from the cache snapshot when present (the control-
		// plane push); before the first push lands, or when nothing was pushed,
		// apply the fixed default — never env vars (see resolveObsConfig).
		obsCfg := resolveObsConfig(cache)
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

// resolveObsConfig reads observability settings from the config snapshot — the
// gateway's only two data sources are the config file (standalone) and the
// control-plane push (xDS); both end up in the same ConfigCache/snapshot
// shape, so both branches of buildGateway call this identically. If the
// snapshot has no observability settings at all (absent from the config file,
// or not yet pushed over xDS), the fixed default in defaultObsGet applies.
// Process environment variables are never consulted here — a deployment that
// wants an env var to drive a sink must reference it explicitly inside
// config.yaml (e.g. endpoint: "${OTEL_EXPORTER_OTLP_ENDPOINT}"), which
// config.LoadYAML's ${VAR} expansion already handles.
func resolveObsConfig(cache *xds.ConfigCache) observability.ObsConfig {
	obsCfg := observability.LoadConfig(cacheObsGet(cache))
	if obsCfg.Sink == "" && obsCfg.LogsSink == "" && obsCfg.MetricsSink == "" && obsCfg.TracesSink == "" && obsCfg.OTLPEndpoint == "" {
		obsCfg = observability.LoadConfig(defaultObsGet)
	}
	return obsCfg
}

// defaultObsGet supplies fixed defaults (logs→stdout, metrics/traces→none)
// when neither the config file nor a control-plane push declares any
// observability setting. It never reads the process environment.
func defaultObsGet(key string) (string, error) {
	switch key {
	case "obs_logs_sink":
		return "stdout", nil
	case "obs_metrics_sink", "obs_traces_sink":
		return "none", nil
	}
	return "", nil
}
