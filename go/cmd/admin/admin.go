// Package admin implements the `nyro admin` subcommand: the control plane
// (management API + WebUI + OAuth session lifecycle + config-sync push).
package admin

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/spf13/cobra"

	"github.com/nyroway/nyro/go/internal/admin"
	"github.com/nyroway/nyro/go/internal/bootstrap"
	"github.com/nyroway/nyro/go/internal/configsync"
	"github.com/nyroway/nyro/go/internal/configsync/pki"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/webui"
)

// nyroHomeDir returns ~/.nyro, the default home for admin-local state
// (sqlite DB, observability parquet data) when the user hasn't pointed
// --dsn/--obs-data-dir elsewhere. Falls back to "./.nyro" (relative to
// the working directory) if the OS user home directory can't be resolved —
// best-effort, never fatal, so admin still starts.
func nyroHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".nyro"
	}
	return filepath.Join(home, ".nyro")
}

// defaultDSN is the --dsn value used when the flag is left empty: a sqlite
// file under the admin-managed ~/.nyro home.
func defaultDSN() string {
	return "sqlite://" + filepath.Join(nyroHomeDir(), "nyro.db")
}

// NewCmd builds the admin (control-plane) subcommand.
//
// In addition to the REST API + WebUI, the admin optionally runs a gRPC
// ConfigService server (--config-listen) that pushes the full config snapshot
// to every connected gateway. When enabled, every config write (providers,
// models, api keys, settings) triggers an immediate push to all gateways.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Run the control plane (management API + WebUI)",
	}
	cmd.Flags().String("listen", "127.0.0.1:19531", "listen address for the control plane")
	// Bound to loopback by default. This stream carries every upstream's
	// credentials_json, so plaintext mode logs a security warning; operators
	// can configure mTLS with --config-tls-ca/-cert/-key.
	cmd.Flags().String("config-listen", "127.0.0.1:19532", "listen address for the config-sync gRPC server (empty disables it)")
	cmd.Flags().String("config-tls-ca", "", "config-sync mTLS: path to the CA certificate that signs admin/gateway leaf certs (see `nyro ca`); must be set together with --config-tls-cert/-key")
	cmd.Flags().String("config-tls-cert", "", "config-sync mTLS: path to admin's server certificate")
	cmd.Flags().String("config-tls-key", "", "config-sync mTLS: path to admin's server private key")
	cmd.Flags().String("token", "", "Bearer token protecting /api/v1 admin routes")
	cmd.Flags().String("webui-dir", "", "path to the built WebUI (serves the SPA at /)")
	cmd.Flags().String("dsn", "", fmt.Sprintf("database DSN: sqlite://<path> (default %s), postgres://..., or mysql://...", defaultDSN()))
	cmd.Flags().String("obs-data-dir", filepath.Join(nyroHomeDir(), "obs"), "directory for admin-local observability parquet data (logs/metrics/traces)")
	cmd.Flags().Duration("config-poll-interval", 0, "how often to poll the shared config_epoch setting for changes made by other admin replicas (0 disables polling)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		addr, _ := cmd.Flags().GetString("listen")
		grpcAddr, _ := cmd.Flags().GetString("config-listen")
		tlsCA, _ := cmd.Flags().GetString("config-tls-ca")
		tlsCert, _ := cmd.Flags().GetString("config-tls-cert")
		tlsKey, _ := cmd.Flags().GetString("config-tls-key")
		adminToken, _ := cmd.Flags().GetString("token")
		webuiDir, _ := cmd.Flags().GetString("webui-dir")
		dsn, _ := cmd.Flags().GetString("dsn")
		obsDataDir, _ := cmd.Flags().GetString("obs-data-dir")
		configPollInterval, _ := cmd.Flags().GetDuration("config-poll-interval")
		if configPollInterval < 0 {
			return fmt.Errorf("--config-poll-interval must not be negative")
		}
		if grpcAddr == "" {
			if cmd.Flags().Changed("config-poll-interval") {
				return fmt.Errorf("--config-poll-interval requires --config-listen")
			}
			for _, name := range []string{"config-tls-ca", "config-tls-cert", "config-tls-key"} {
				if cmd.Flags().Changed(name) {
					return fmt.Errorf("--%s requires --config-listen", name)
				}
			}
		}
		if adminToken == "" && !isLoopbackListenAddress(addr) {
			slog.Warn("admin API is exposed without --token; unauthenticated clients can access control-plane routes", "listen", addr)
		}

		var configTLS *tls.Config
		if grpcAddr != "" {
			var err error
			configTLS, err = resolveConfigSyncServerTLS(tlsCA, tlsCert, tlsKey)
			if err != nil {
				return err
			}
		}

		usingDefaultDSN := dsn == ""
		if usingDefaultDSN {
			dsn = defaultDSN()
		}

		backend, driverDSN, err := bootstrap.ParseDSN(dsn)
		if err != nil {
			return err
		}
		if backend == "sqlite" {
			// The sqlite driver opens/creates the DB file itself but never its
			// parent directory. For the ~/.nyro default that's our own managed
			// space, so auto-creating it (like ~/.aws, ~/.docker, ~/.kube) is
			// expected. For an explicit --dsn, silently creating a missing
			// directory risks masking a typo'd path with a fresh empty DB
			// instead of the one the operator meant to open — fail loudly
			// instead, matching how `postgres initdb -D <dir>` and friends
			// treat an explicitly-named data directory.
			if dir := filepath.Dir(driverDSN); dir != "" && dir != "." {
				if usingDefaultDSN {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						return fmt.Errorf("create --dsn directory %q: %w", dir, err)
					}
				} else if info, err := os.Stat(dir); err != nil || !info.IsDir() {
					return fmt.Errorf("--dsn directory %q does not exist (create it first, or leave --dsn unset to use the default under ~/.nyro)", dir)
				}
			}
		}

		// Same reasoning as --dsn above: the ~/.nyro default is auto-created
		// (parquet.NewSink MkdirAll's it on demand below), but an explicit
		// --obs-data-dir naming a missing directory fails loudly instead of
		// silently starting a fresh, empty observability store there.
		if cmd.Flags().Changed("obs-data-dir") {
			if info, err := os.Stat(obsDataDir); err != nil || !info.IsDir() {
				return fmt.Errorf("--obs-data-dir %q does not exist (create it first, or leave --obs-data-dir unset to use the default under ~/.nyro)", obsDataDir)
			}
		}

		st, err := bootstrap.OpenStorageFromDSN(dsn)
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
			shutdown, err := configsync.ServeGRPC(ctx, grpcAddr, srv, configTLS)
			if err != nil {
				return err
			}
			defer shutdown()
			admin.SetBroadcaster(srv)
			admin.SetNodeLister(srv)
			pki.WatchExpiry(ctx, configTLS, configExpiryCheckInterval, func(notAfter time.Time) {
				slog.Warn("config-sync server certificate expiring soon — run `nyro ca sign-admin` and redistribute before it lapses",
					"not_after", notAfter, "remaining", time.Until(notAfter).Round(time.Hour))
			})

			watcher, err := startEpochWatcher(ctx, configPollInterval, st.Settings(), srv)
			if err != nil {
				return err
			}
			admin.SetEpochWatcher(watcher)
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

func isLoopbackListenAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// configExpiryCheckInterval is how often WatchExpiry re-checks the loaded
// config-sync certificate once running (it always checks once immediately
// at startup too). Daily is frequent enough given the ExpiryWarningWindow is
// 30 days — see pki.WatchExpiry.
const configExpiryCheckInterval = 24 * time.Hour

func startEpochWatcher(
	ctx context.Context,
	interval time.Duration,
	store configsync.EpochStore,
	notifier configsync.Notifier,
) (admin.EpochObserver, error) {
	if interval < 0 {
		return nil, fmt.Errorf("--config-poll-interval must not be negative")
	}
	if interval == 0 {
		return nil, nil
	}

	watcher := configsync.NewEpochWatcher(store, notifier)
	if err := watcher.Seed(ctx); err != nil {
		return nil, fmt.Errorf("seed config epoch watcher: %w", err)
	}
	go watcher.Run(ctx, interval)
	return watcher, nil
}

// resolveConfigSyncServerTLS turns the --config-tls-ca/-cert/-key flags into
// a *tls.Config for the config-sync gRPC server, or nil for plaintext.
//
//   - All three tls paths set: mTLS is used.
//   - Some but not all three set: a partial/likely-typo'd configuration —
//     fail fast rather than silently falling back to plaintext or guessing
//     which file is missing.
//   - None set: plaintext is used and a security warning is logged.
func resolveConfigSyncServerTLS(caPath, certPath, keyPath string) (*tls.Config, error) {
	set := 0
	for _, p := range []string{caPath, certPath, keyPath} {
		if p != "" {
			set++
		}
	}
	switch {
	case set == 3:
		return pki.LoadServerTLS(caPath, certPath, keyPath)
	case set > 0:
		return nil, fmt.Errorf("--config-tls-ca, --config-tls-cert, and --config-tls-key must be set together (got %d of 3)", set)
	default:
		slog.Warn("config-sync gRPC server running in plaintext: no transport encryption or client authentication — the stream carries upstream credentials in the clear to any client that connects")
		return nil, nil
	}
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
// to point at this admin instance's own --listen, and defaults any still-empty
// obs_<signal>_exporter to "otlp". This is a real, editable setting written
// to storage — not an in-memory/runtime override of ObsConfig — so the user
// can change it later via the WebUI.
//
// The seeded value must be a valid absolute URL: provider.go's OTLP builders
// (otlploghttp/otlpmetrichttp/otlptracehttp) call WithEndpointURL, which
// url.Parses the string — a schemeless "host:port" value (e.g. what --listen
// takes, "127.0.0.1:19531") fails to parse as an absolute URL and causes the
// OTel SDK to silently fall back to its own built-in default
// ("localhost:4318") instead of this admin's real address. addrToOTLPURL
// below normalizes --listen into "http://host:port" (leaving an
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

// addrToOTLPURL normalizes a --listen-style value into an absolute URL suitable
// for otlploghttp/otlpmetrichttp/otlptracehttp's WithEndpointURL, which
// url.Parses its argument and requires a scheme. --listen is documented and
// used elsewhere (bootstrap.RunServer) as a bare "host:port" listen address
// (e.g. "127.0.0.1:19531", or ":8080"), so the common case just gets
// "http://" prepended. If addr already carries an "http://" or "https://"
// scheme (e.g. a user hand-editing the seeded value, or a future --listen that
// accepts a full URL), it is returned unchanged rather than double-prefixed.
func addrToOTLPURL(addr string) string {
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return addr
	}
	return "http://" + addr
}
