// Package admin implements the `nyro admin` subcommand: the control plane
// (management API + WebUI + OAuth session lifecycle + xDS config push).
package admin

import (
	"context"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/spf13/cobra"

	"github.com/nyroway/nyro/go/internal/admin"
	"github.com/nyroway/nyro/go/internal/auth"
	"github.com/nyroway/nyro/go/internal/bootstrap"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
	"github.com/nyroway/nyro/go/internal/proxy"
	"github.com/nyroway/nyro/go/internal/xds"
)

// NewCmd builds the admin (control-plane) subcommand.
//
// In addition to the REST API + WebUI, the admin optionally runs a gRPC
// ConfigService server (--grpc-addr) that pushes the full config snapshot to
// every connected gateway. When enabled, every config write (providers, models,
// api keys, settings) triggers an immediate push to all gateways.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Run the control plane (management API + WebUI)",
	}
	cmd.Flags().String("addr", "127.0.0.1:19531", "listen address for the control plane")
	cmd.Flags().String("grpc-addr", "", "listen address for the gRPC xDS server (disabled if empty)")
	cmd.Flags().String("admin-token", "", "Bearer token protecting /api/v1 admin routes")
	cmd.Flags().String("webui-dir", "", "path to the built WebUI (serves the SPA at /)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		addr, _ := cmd.Flags().GetString("addr")
		grpcAddr, _ := cmd.Flags().GetString("grpc-addr")
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

		// ── Observability sinks (admin side) ──
		// Three parquet sinks (logs/metrics/traces) feed the OTLP/HTTP receiver.
		// The receiver decodes the official OTLP protobuf and buffers rows; each
		// sink rotates its parquet file on its own (maxRows) and on Flush below.
		obsCfg := observability.LoadConfig(st.Settings().Get)
		logSink, err := parquet.NewSink[observability.LogRecord](obsCfg.DataDir, "logs", 50000)
		if err != nil {
			return err
		}
		metricSink, err := parquet.NewSink[observability.MetricSample](obsCfg.DataDir, "metrics", 50000)
		if err != nil {
			return err
		}
		traceSink, err := parquet.NewSink[observability.SpanSnapshot](obsCfg.DataDir, "traces", 50000)
		if err != nil {
			return err
		}
		rcv := observability.NewReceiver(logSink, metricSink, traceSink)

		// Janitor sweeps aged parquet files per signal on an hourly tick; exits
		// when ctx is cancelled (server shutdown).
		observability.StartJanitor(ctx, obsCfg.DataDir, observability.SignalRetention{
			Logs:    obsCfg.LogsRetentionDays,
			Metrics: obsCfg.MetricsRetentionDays,
			Traces:  obsCfg.TracesRetentionDays,
		}, time.Hour)

		// Optionally start the gRPC xDS server and wire it as the config-push
		// target so every admin config write reaches connected gateways.
		if grpcAddr != "" {
			srv := xds.NewConfigServer(st)
			shutdown, err := xds.ServeGRPC(ctx, grpcAddr, srv)
			if err != nil {
				return err
			}
			defer shutdown()
			admin.SetBroadcaster(srv)
		}

		engine := chi.NewRouter()
		engine.Use(middleware.Recoverer)

		// Mount the OTLP receiver at the TOP LEVEL (/v1/{logs,metrics,traces})
		// BEFORE the bearer-protected /api/v1 group — these routes are NOT behind
		// the admin token (the auth boundary is the network/admin deployment,
		// matching the gateway→admin push contract).
		rcv.Mount(engine)

		// admin.Mount wires the parquet-backed read sources:
		//   - LogSource:   /logs reads the parquet observability store.
		//   - StatsSource: /stats/* reads the metrics parquet store.
		// The request_logs table was removed in Phase 4; these are the only
		// request-log/metrics read paths.
		admin.Mount(engine, st, adminToken, admin.NewParquetLogSource(obsCfg.DataDir), admin.NewParquetStatsSource(obsCfg.DataDir))
		admin.MountOAuth(engine, st, reg, sessions)
		proxy.MountWebui(engine, webuiDir)

		// Best-effort flush of buffered OTLP rows on shutdown; do not block
		// shutdown — the sinks' own rotation already bounds data loss.
		defer rcv.Flush(ctx)

		return bootstrap.RunServer(engine, addr)
	}
	return cmd
}
