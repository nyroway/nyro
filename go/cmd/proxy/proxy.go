// Package proxy implements the `nyro proxy` subcommand: the data plane that
// forwards client requests to upstream providers.
package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/nyroway/nyro/go/internal/bootstrap"
	"github.com/nyroway/nyro/go/internal/configsync"
	"github.com/nyroway/nyro/go/internal/configsync/pki"
	"github.com/nyroway/nyro/go/internal/gateway"
	gatewayruntime "github.com/nyroway/nyro/go/internal/gateway/runtime"
)

// NewCmd builds the proxy (data-plane) subcommand.
//
// Config sources (exactly one is required):
//   - --config: standalone YAML (no server/DB needed). The snapshot is built
//     once at startup and never refreshed; edit + restart to change config.
//   - --server: the server's gRPC config-sync endpoint. The proxy subscribes
//     to a long-lived config stream and hot-reloads on every config change.
//
// Phase 3 removed the transitional Phase-1 DB-poll default — exactly one of
// --config / --server must now be set.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Start a standalone data plane",
	}
	// 0.0.0.0, not loopback: the standalone proxy's entire job is accepting
	// traffic from real clients (like nginx/envoy/traefik), often from outside
	// its own host/container — unlike the control plane, which manages
	// sensitive credentials and defaults to loopback-only on purpose.
	cmd.Flags().String("listen", "0.0.0.0:19530", "Data plane listen address")
	cmd.Flags().String("config", "", "Standalone YAML configuration file")
	cmd.Flags().String("server", "", "Config sync server address")
	cmd.Flags().String("sync-token", "", "Token used to join config sync")
	cmd.Flags().String("sync-tls-ca", "", "CA certificate for config sync")
	cmd.Flags().String("sync-tls-cert", "", "Client certificate for config sync")
	cmd.Flags().String("sync-tls-key", "", "Client private key for config sync")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		addr, _ := cmd.Flags().GetString("listen")
		cfgPath, _ := cmd.Flags().GetString("config")
		configSyncAddr, _ := cmd.Flags().GetString("server")
		syncToken, _ := cmd.Flags().GetString("sync-token")
		tlsCA, _ := cmd.Flags().GetString("sync-tls-ca")
		tlsCert, _ := cmd.Flags().GetString("sync-tls-cert")
		tlsKey, _ := cmd.Flags().GetString("sync-tls-key")

		if cfgPath == "" && configSyncAddr == "" {
			return errors.New("exactly one of --config or --server is required (the legacy DB-poll default was removed in Phase 3)")
		}
		if cfgPath != "" && configSyncAddr != "" {
			return errors.New("--config and --server are mutually exclusive (set exactly one)")
		}
		if cfgPath != "" {
			for _, name := range []string{"sync-token", "sync-tls-ca", "sync-tls-cert", "sync-tls-key"} {
				if cmd.Flags().Changed(name) {
					return fmt.Errorf("--%s is only valid with --server (not --config)", name)
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
			// Symmetric with the server-side guard: refuse to pull upstream
			// credentials across a network in the clear with nothing
			// authenticating either end.
			if err := configsync.GuardPlaintextDial(configSyncAddr, configTLS != nil, syncToken != ""); err != nil {
				return err
			}
			if configTLS == nil && syncToken != "" {
				slog.Warn("config-sync join token is sent over an unencrypted connection; it crosses the network in the clear and can be replayed — prefer mTLS (`nyro tool ca`) off-host",
					"server", configSyncAddr)
			}
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		protocols, err := bootstrap.NewLLMProtocolCatalog()
		if err != nil {
			return fmt.Errorf("compose LLM protocols: %w", err)
		}
		providers, err := bootstrap.NewLLMProviderCatalog()
		if err != nil {
			return fmt.Errorf("compose LLM providers: %w", err)
		}

		gw, runtimeMgr, err := gatewayruntime.Build(ctx, gatewayruntime.Options{
			Protocols:  protocols,
			Providers:  providers,
			ConfigPath: cfgPath,
			SyncTarget: configSyncAddr,
			SyncTLS:    configTLS,
			SyncToken:  syncToken,
			ListenAddr: addr,
		})
		if err != nil {
			return err
		}
		defer func() {
			shutCtx, shutCancel := context.WithTimeout(context.Background(), gatewayruntime.ShutdownTimeout)
			defer shutCancel()
			if err := runtimeMgr.Shutdown(shutCtx); err != nil {
				slog.Warn("gateway runtime shutdown failed", "error", err)
			}
		}()

		engine := gateway.NewRouter(gw)
		return bootstrap.RunServer(engine, addr)
	}
	return cmd
}

// resolveConfigSyncClientTLS turns the --sync-tls-ca/-cert/-key flags into
// a *tls.Config for dialing --server, or nil for plaintext. It uses the
// same all-or-none behavior as resolveConfigSyncServerTLS on the server side.
//
// Plaintext is selected silently; configsync.GuardPlaintextDial decides whether
// it is acceptable based on whether --server leaves this host.
//
// There is deliberately no --server-name-style override here:
// pki.LoadClientTLS verifies the server certificate by SPIFFE identity
// (spiffe://nyro/server), not by matching its SAN against the dial address,
// so the address used in --server (direct, load balancer, k8s
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
		return nil, fmt.Errorf("--sync-tls-ca, --sync-tls-cert, and --sync-tls-key must be set together (got %d of 3)", set)
	default:
		return nil, nil
	}
}
