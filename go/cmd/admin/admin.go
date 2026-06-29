// Package admin implements the `nyro admin` subcommand: the control plane
// (management API + WebUI + OAuth session lifecycle).
package admin

import (
	"context"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/spf13/cobra"

	"github.com/nyroway/nyro/go/internal/admin"
	"github.com/nyroway/nyro/go/internal/auth"
	"github.com/nyroway/nyro/go/internal/bootstrap"
	"github.com/nyroway/nyro/go/internal/proxy"
)

// NewCmd builds the admin (control-plane) subcommand.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Run the control plane (management API + WebUI)",
	}
	cmd.Flags().String("addr", "127.0.0.1:19531", "listen address for the control plane")
	cmd.Flags().String("admin-token", "", "Bearer token protecting /api/v1 admin routes")
	cmd.Flags().String("webui-dir", "", "path to the built WebUI (serves the SPA at /)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		addr, _ := cmd.Flags().GetString("addr")
		adminToken, _ := cmd.Flags().GetString("admin-token")
		webuiDir, _ := cmd.Flags().GetString("webui-dir")
		storageBackend, _ := cmd.Flags().GetString("storage")
		dbDSN, _ := cmd.Flags().GetString("db-dsn")

		st, err := bootstrap.OpenStorage(storageBackend, dbDSN)
		if err != nil {
			return err
		}

		reg := auth.NewRegistry()
		bootstrap.RegisterDrivers(reg)
		sessions := auth.NewSessionStore()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		bootstrap.StartRetentionLoop(ctx, st)

		engine := chi.NewRouter()
		engine.Use(middleware.Recoverer)
		admin.Mount(engine, st, adminToken)
		admin.MountOAuth(engine, st, reg, sessions)
		proxy.MountWebui(engine, webuiDir)
		return bootstrap.RunServer(engine, addr)
	}
	return cmd
}
