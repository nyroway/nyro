// Package admin implements the `nyro admin` subcommand: the control plane
// (management API + WebUI + OAuth session lifecycle + config-sync push).
package admin

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/spf13/cobra"

	"github.com/nyroway/nyro/go/internal/admin"
	"github.com/nyroway/nyro/go/internal/bootstrap"
	"github.com/nyroway/nyro/go/internal/configsync"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/webui"
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
	cmd.Flags().String("grpc-addr", "", "listen address for the config-sync gRPC server (disabled if empty)")
	cmd.Flags().String("admin-token", "", "Bearer token protecting /api/v1 admin routes")
	cmd.Flags().String("webui-dir", "", "path to the built WebUI (serves the SPA at /)")
	cmd.Flags().String("storage", "sqlite", "storage backend: sqlite|postgres|mysql")
	cmd.Flags().String("db-dsn", "", "database path/DSN (sqlite: file path, default ./data/nyro.db; postgres/mysql: full DSN, required)")
	cmd.Flags().String("obs-data-dir", "./data/obs", "directory for admin-local observability parquet data (logs/metrics/traces)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		addr, _ := cmd.Flags().GetString("addr")
		grpcAddr, _ := cmd.Flags().GetString("grpc-addr")
		adminToken, _ := cmd.Flags().GetString("admin-token")
		webuiDir, _ := cmd.Flags().GetString("webui-dir")
		storageBackend, _ := cmd.Flags().GetString("storage")
		dbDSN, _ := cmd.Flags().GetString("db-dsn")
		obsDataDir, _ := cmd.Flags().GetString("obs-data-dir")

		switch storageBackend {
		case "sqlite":
			if dbDSN == "" {
				dbDSN = "./data/nyro.db"
			}
		case "postgres", "mysql":
			if dbDSN == "" {
				return fmt.Errorf("--db-dsn is required for --storage %s", storageBackend)
			}
		default:
			return fmt.Errorf("unknown --storage %q (want sqlite|postgres|mysql)", storageBackend)
		}

		st, err := bootstrap.OpenStorage(storageBackend, dbDSN)
		if err != nil {
			return err
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// ── One-time settings migration + first-boot seed (best-effort) ──
		// Order matters: migration may populate the new obs_<signal>_exporter /
		// obs_<signal>_otlp_endpoint keys from deprecated ones, and seeding must
		// only fire when those new keys are still empty afterwards.
		migrateLegacyObsSettings(st.Settings())
		seedDefaultObsEndpoint(st.Settings(), addr)

		// ── Observability sinks (admin side) ──
		// Three parquet sinks (logs/metrics/traces) feed the OTLP/HTTP receiver.
		// The receiver decodes the official OTLP protobuf and buffers rows; each
		// sink rotates its parquet file on its own (maxRows) and on Flush below.
		obsCfg, err := observability.LoadConfig(st.Settings().Get)
		if err != nil {
			return fmt.Errorf("load observability config: %w", err)
		}
		// obs_data_dir is no longer a setting; DataDir comes from --obs-data-dir.
		obsCfg.DataDir = obsDataDir
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

		// Optionally start the config-sync gRPC server and wire it as the
		// config-push target so every admin config write reaches connected
		// gateways.
		if grpcAddr != "" {
			srv := configsync.NewConfigServer(st)
			shutdown, err := configsync.ServeGRPC(ctx, grpcAddr, srv)
			if err != nil {
				return err
			}
			defer shutdown()
			admin.SetBroadcaster(srv)
			admin.SetNodeLister(srv)
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
		webui.Mount(engine, webuiDir)

		// Best-effort flush of buffered OTLP rows on shutdown; do not block
		// shutdown — the sinks' own rotation already bounds data loss.
		defer rcv.Flush(ctx)

		return bootstrap.RunServer(engine, addr)
	}
	return cmd
}

// legacyObsSignalSinkKeys are the deprecated per-signal sink keys, superseded
// by obs_<signal>_exporter. See global-constraints.md "设置 key 迁移策略".
var legacyObsSignalSinkKeys = map[string]string{
	"logs":    "obs_logs_sink",
	"metrics": "obs_metrics_sink",
	"traces":  "obs_traces_sink",
}

// legacyObsDeadKeys are deprecated keys that never map to any new key and are
// simply dropped once migration is done (obs_metrics_path/obs_traces_protocol
// are handled specially below; the rest — obs_sink/obs_export_interval/
// obs_otlp_endpoint — have no new-key destination other than the per-signal
// copy already performed).
var legacyObsDeadKeys = []string{
	"obs_logs_sink",
	"obs_metrics_sink",
	"obs_traces_sink",
	"obs_otlp_endpoint",
	"obs_metrics_path",
	"obs_traces_protocol",
	"obs_sink",
	"obs_export_interval",
}

// migrateLegacyObsSettings performs a one-time, best-effort migration of the
// deprecated global/shared observability settings keys into the new
// per-signal obs_<signal>_exporter / obs_<signal>_<kind>_<field> keys, then
// deletes the old keys. This is a destructive, pre-release migration (no
// runtime fallback compatibility — see global-constraints.md).
//
// It is idempotent: once the old keys are gone (e.g. migrated on a prior
// boot), every Get below returns "" and no writes happen.
//
// The storage.SettingsStore interface has no Delete method, so "deleting" a
// legacy key means Set(key, "") — functionally identical to absence for
// every reader in this codebase (Get returns "" for both a missing key and
// one explicitly set to "").
//
// Failures are logged and never block startup.
func migrateLegacyObsSettings(s storage.SettingsStore) {
	get := func(key string) string {
		v, err := s.Get(key)
		if err != nil {
			slog.Warn("obs settings migration: read failed", "key", key, "error", err)
			return ""
		}
		return v
	}
	set := func(key, value string) {
		if err := s.Set(key, value); err != nil {
			slog.Error("obs settings migration: write failed", "key", key, "error", err)
		}
	}

	legacyEndpoint := get("obs_otlp_endpoint")
	legacyTracesProtocol := get("obs_traces_protocol")

	for signal, sinkKey := range legacyObsSignalSinkKeys {
		sink := get(sinkKey)
		if sink == "" || sink == "none" {
			continue
		}
		set(fmt.Sprintf("obs_%s_exporter", signal), sink)
		if sink == "otlp" && legacyEndpoint != "" {
			set(fmt.Sprintf("obs_%s_otlp_endpoint", signal), legacyEndpoint)
		}
		if signal == "traces" && sink == "otlp" && legacyTracesProtocol != "" {
			set("obs_traces_otlp_protocol", legacyTracesProtocol)
		}
	}

	// obs_metrics_path has no destination (prometheus, the only engine that
	// would use it, never existed as a legacy sink value) — it is dropped
	// below along with the rest of the dead keys.
	for _, key := range legacyObsDeadKeys {
		existing, err := s.Get(key)
		if err != nil || existing == "" {
			continue // not set (or unreadable) — nothing to delete.
		}
		set(key, "")
	}
}

// obsSeedSignals lists the three independent signals seeding operates over.
var obsSeedSignals = []string{"logs", "metrics", "traces"}

// seedDefaultObsEndpoint runs after migration and, only if all three signals'
// obs_<signal>_otlp_endpoint keys are still empty (fresh install, or a
// migration that found nothing to copy), seeds every signal's OTLP endpoint
// to point at this admin instance's own --addr, and defaults any still-empty
// obs_<signal>_exporter to "otlp". This is a real, editable setting written
// to storage — not an in-memory/runtime override of ObsConfig — so the user
// can change it later via the WebUI.
//
// The seeded value must be a valid absolute URL: provider.go's OTLP builders
// (otlploghttp/otlpmetrichttp/otlptracehttp) call WithEndpointURL, which
// url.Parses the string — a schemeless "host:port" value (e.g. what --addr
// takes, "127.0.0.1:19531") fails to parse as an absolute URL and causes the
// OTel SDK to silently fall back to its own built-in default
// ("localhost:4318") instead of this admin's real address. addrToOTLPURL
// below normalizes --addr into "http://host:port" (leaving an
// already-schemed value untouched).
//
// Idempotent: if any endpoint is already set (user-configured or seeded on a
// prior boot), this is a no-op.
func seedDefaultObsEndpoint(s storage.SettingsStore, addr string) {
	get := func(key string) string {
		v, err := s.Get(key)
		if err != nil {
			slog.Warn("obs default-endpoint seed: read failed", "key", key, "error", err)
			return ""
		}
		return v
	}

	for _, signal := range obsSeedSignals {
		if get(fmt.Sprintf("obs_%s_otlp_endpoint", signal)) != "" {
			return // at least one signal already configured — leave everything alone.
		}
	}

	endpoint := addrToOTLPURL(addr)
	for _, signal := range obsSeedSignals {
		if err := s.Set(fmt.Sprintf("obs_%s_otlp_endpoint", signal), endpoint); err != nil {
			slog.Error("obs default-endpoint seed: write failed", "signal", signal, "error", err)
			continue
		}
		if get(fmt.Sprintf("obs_%s_exporter", signal)) == "" {
			if err := s.Set(fmt.Sprintf("obs_%s_exporter", signal), "otlp"); err != nil {
				slog.Error("obs default-endpoint seed: write failed", "signal", signal, "error", err)
			}
		}
	}
}

// addrToOTLPURL normalizes a --addr-style value into an absolute URL suitable
// for otlploghttp/otlpmetrichttp/otlptracehttp's WithEndpointURL, which
// url.Parses its argument and requires a scheme. --addr is documented and
// used elsewhere (bootstrap.RunServer) as a bare "host:port" listen address
// (e.g. "127.0.0.1:19531", or ":8080"), so the common case just gets
// "http://" prepended. If addr already carries an "http://" or "https://"
// scheme (e.g. a user hand-editing the seeded value, or a future --addr that
// accepts a full URL), it is returned unchanged rather than double-prefixed.
func addrToOTLPURL(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}
