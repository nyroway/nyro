// Package gateway implements the `nyro gateway` subcommand: the data plane
// that forwards client requests to upstream providers.
package gateway

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nyroway/nyro/go/internal/auth"
	"github.com/nyroway/nyro/go/internal/bootstrap"
	"github.com/nyroway/nyro/go/internal/config"
	"github.com/nyroway/nyro/go/internal/proxy"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// NewCmd builds the gateway (data-plane) subcommand.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gateway",
		Short: "Run the data plane (proxy forwarding to upstreams)",
	}
	cmd.Flags().String("addr", "127.0.0.1:19530", "listen address for the data plane")
	cmd.Flags().String("config", "", "standalone YAML config file (no admin/DB needed)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		addr, _ := cmd.Flags().GetString("addr")
		cfgPath, _ := cmd.Flags().GetString("config")
		storageBackend, _ := cmd.Flags().GetString("storage")
		dbDSN, _ := cmd.Flags().GetString("db-dsn")

		st, err := openGatewayStorage(cfgPath, storageBackend, dbDSN)
		if err != nil {
			return err
		}

		reg := auth.NewRegistry()
		bootstrap.RegisterDrivers(reg)

		gw := proxy.NewGateway(st)
		gw.SetDriverRegistry(reg)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		gw.StartOAuthRefreshLoop(ctx)
		bootstrap.StartRetentionLoop(ctx, st)

		engine := proxy.NewRouter(gw)
		return bootstrap.RunServer(engine, addr)
	}
	return cmd
}

// openGatewayStorage selects memory-from-config (standalone) or a DB backend.
func openGatewayStorage(cfgPath, backend, dsn string) (storage.Storage, error) {
	if cfgPath != "" {
		st := memory.New()
		cfg, err := config.LoadYAML(cfgPath)
		if err != nil {
			return nil, err
		}
		if err := cfg.ApplyTo(st); err != nil {
			return nil, fmt.Errorf("apply config: %w", err)
		}
		return st, nil
	}
	return bootstrap.OpenStorage(backend, dsn)
}
