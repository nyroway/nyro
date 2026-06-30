// Package gateway implements the `nyro gateway` subcommand: the data plane
// that forwards client requests to upstream providers.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/nyroway/nyro/go/internal/auth"
	"github.com/nyroway/nyro/go/internal/bootstrap"
	"github.com/nyroway/nyro/go/internal/config"
	"github.com/nyroway/nyro/go/internal/proxy"
	"github.com/nyroway/nyro/go/internal/storage/memory"
	"github.com/nyroway/nyro/go/internal/xds"
)

// NewCmd builds the gateway (data-plane) subcommand.
//
// Config sources (exactly one):
//   - --config: standalone YAML (no admin/DB needed). The snapshot is built once
//     at startup and never refreshed; edit + restart to change config.
//   - --xds-addr: admin's gRPC endpoint. The gateway subscribes to a long-lived
//     config stream and hot-reloads on every admin config change.
//
// If NEITHER flag is set, the gateway falls back to the transitional Phase-1
// DB-poll loader (kept for dev compatibility; Phase 3 removes it).
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
		storageBackend, _ := cmd.Flags().GetString("storage")
		dbDSN, _ := cmd.Flags().GetString("db-dsn")

		if cfgPath != "" && xdsAddr != "" {
			return errors.New("--config and --xds-addr are mutually exclusive (set exactly one)")
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		gw, stopXDS, err := buildGateway(ctx, cfgPath, xdsAddr, storageBackend, dbDSN)
		if err != nil {
			return err
		}
		if stopXDS != nil {
			defer stopXDS()
		}

		reg := auth.NewRegistry()
		bootstrap.RegisterDrivers(reg)
		gw.SetDriverRegistry(reg)

		gw.StartOAuthRefreshLoop(ctx)
		bootstrap.StartRetentionLoop(ctx, gw.Storage)

		engine := proxy.NewRouter(gw)
		return bootstrap.RunServer(engine, addr)
	}
	return cmd
}

// buildGateway selects the config source and returns a ready Gateway plus an
// optional xDS-client stop function (nil unless --xds-addr).
func buildGateway(ctx context.Context, cfgPath, xdsAddr, backend, dsn string) (gw *proxy.Gateway, stopXDS func(), err error) {
	switch {
	case cfgPath != "":
		// Standalone YAML: build snapshot directly, memory storage for OAuth/quota/logs.
		st := memory.New()
		cfg, err := config.LoadYAML(cfgPath)
		if err != nil {
			return nil, nil, err
		}
		if err := cfg.ApplyTo(st); err != nil {
			return nil, nil, fmt.Errorf("apply config: %w", err)
		}
		snap, err := cfg.BuildSnapshot()
		if err != nil {
			return nil, nil, fmt.Errorf("build snapshot: %w", err)
		}
		cache := &xds.ConfigCache{}
		cache.Swap(snap)
		return proxy.NewGatewayWithCache(st.Storage(), cache), nil, nil

	case xdsAddr != "":
		// xDS hot-reload: DB storage (for OAuth/quota/logs), empty cache filled by the stream.
		st, err := bootstrap.OpenStorage(backend, dsn)
		if err != nil {
			return nil, nil, err
		}
		cache := &xds.ConfigCache{}
		client := xds.NewConfigClient(xdsAddr, cache)
		go func() { _ = client.Run(ctx) }()
		return proxy.NewGatewayWithCache(st, cache), nil, nil

	default:
		// Transitional Phase-1 DB-poll mode. Kept for dev compatibility; Phase 3
		// removes it in favor of an explicit choice between YAML and xDS.
		st, err := bootstrap.OpenStorage(backend, dsn)
		if err != nil {
			return nil, nil, err
		}
		gw := proxy.NewGateway(st)
		stopLoader := gw.StartConfigLoader(10 * time.Second)
		return gw, stopLoader, nil
	}
}
