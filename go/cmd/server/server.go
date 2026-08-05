// Package server implements the `nyro serve` subcommand: the control plane
// (management API + WebUI + OAuth session lifecycle + config-sync push).
package server

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	"github.com/nyroway/nyro/go/internal/admin"
	"github.com/nyroway/nyro/go/internal/bootstrap"
	"github.com/nyroway/nyro/go/internal/defaults"
	"github.com/nyroway/nyro/go/internal/configsync"
	"github.com/nyroway/nyro/go/internal/configsync/pki"
	"github.com/nyroway/nyro/go/internal/dataplane"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/observability/parquet"
	"github.com/nyroway/nyro/go/internal/proxy"
	"github.com/nyroway/nyro/go/internal/state"
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

// NewCmd builds the server (control-plane) subcommand.
//
// `nyro serve` is the single-command deployment: the REST API + WebUI on
// --listen, and — unless --proxy-listen is empty — an embedded data plane on
// --proxy-listen, so one process is a complete, usable nyro. The embedded data
// plane is assembled by internal/dataplane over an in-process config-sync
// channel, i.e. the exact code path a remote `nyro proxy` uses; it never reads
// storage directly.
//
// It optionally also serves config-sync over TCP (--sync-listen) so additional
// `nyro proxy` nodes can subscribe. Every config write (providers, models, api
// keys, settings) triggers an immediate push to all connected data planes,
// embedded and remote alike.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the control plane, with an embedded data plane by default",
	}
	cmd.Flags().String("listen", defaults.ControlPlaneAddr, "listen address for the control plane")
	// Loopback, unlike a standalone `nyro proxy` (0.0.0.0): the single-binary
	// default is a local-first workstation install, so the embedded data plane
	// should not be reachable off-host until an operator says so. A deployment
	// that fronts nyro with nginx/envoy sets this explicitly, and a container
	// deployment must (loopback inside a container is unreachable from outside).
	cmd.Flags().String("proxy-listen", defaults.DataPlaneAddr, "listen address for the embedded data plane (empty disables it, leaving a control-plane-only node)")
	// Empty by default: with an embedded data plane the single-node deployment
	// needs no config-sync port at all, so opening one is an opt-in taken only
	// when additional `nyro proxy` nodes must subscribe. This stream carries
	// every upstream's credentials_json, so plaintext mode logs a security
	// warning; operators can configure mTLS with --sync-tls-ca/-cert/-key.
	cmd.Flags().String("sync-listen", "", "listen address for the config-sync gRPC server so remote `nyro proxy` nodes can subscribe (empty disables it)")
	// Repeatable so a token can be rotated without downtime: add the new one,
	// roll the proxies onto it, then drop the old one. Prefer the env var —
	// a token passed as a flag is visible in `ps`.
	cmd.Flags().StringArray("sync-token", nil, "join token a remote `nyro proxy` must present to subscribe to config-sync; repeatable so tokens can be rotated without downtime. A join credential, NOT an identity: mTLS is what gives each node a verifiable identity. Prefer NYRO_SERVE_SYNC_TOKEN over the flag, which exposes the value in `ps`")
	cmd.Flags().String("sync-tls-ca", "", "config-sync mTLS: path to the CA certificate that signs server/proxy leaf certs (see `nyro tool ca`); must be set together with --sync-tls-cert/-key")
	cmd.Flags().String("sync-tls-cert", "", "config-sync mTLS: path to the server's config-sync server certificate")
	cmd.Flags().String("sync-tls-key", "", "config-sync mTLS: path to the server's config-sync server private key")
	cmd.Flags().String("token", "", "Bearer token protecting the /api/v1 management routes")
	cmd.Flags().String("webui-dir", "", "path to the built WebUI (serves the SPA at /)")
	cmd.Flags().String("dsn", "", fmt.Sprintf("database DSN: sqlite://<path> (default %s) or postgres://...", defaultDSN()))
	cmd.Flags().Bool("auto-migrate", false, "let nyro create/alter the schema itself via GORM AutoMigrate (requires DDL rights on --dsn); default false regardless of backend — without it, nyro only verifies the canonical tables exist, and a DBA applies the DDL from `nyro tool migrate dump`/`diff` (see go/docs/schema/migrations.md)")
	cmd.Flags().Bool("raw-api-keys", false, "store API keys in a recoverable form so they can be re-copied from the WebUI after creation. The raw key is written to the database in plaintext: anyone with read access to the DB obtains working credentials. Default false (hash-only; keys are shown once at creation). Never affects inbound auth (always hash-compared) and is never sent to proxies over config-sync")
	cmd.Flags().String("obs-data-dir", filepath.Join(nyroHomeDir(), "obs"), "directory for control-plane-local observability parquet data (logs/metrics/traces)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		addr, _ := cmd.Flags().GetString("listen")
		proxyAddr, _ := cmd.Flags().GetString("proxy-listen")
		grpcAddr, _ := cmd.Flags().GetString("sync-listen")
		syncTokens, _ := cmd.Flags().GetStringArray("sync-token")
		tlsCA, _ := cmd.Flags().GetString("sync-tls-ca")
		tlsCert, _ := cmd.Flags().GetString("sync-tls-cert")
		tlsKey, _ := cmd.Flags().GetString("sync-tls-key")
		adminToken, _ := cmd.Flags().GetString("token")
		webuiDir, _ := cmd.Flags().GetString("webui-dir")
		dsn, _ := cmd.Flags().GetString("dsn")
		autoMigrate, _ := cmd.Flags().GetBool("auto-migrate")
		plaintextKeys, _ := cmd.Flags().GetBool("raw-api-keys")
		obsDataDir, _ := cmd.Flags().GetString("obs-data-dir")
		if grpcAddr == "" {
			// The in-process channel used by the embedded data plane needs
			// neither, so these only make sense with a TCP listener.
			for _, name := range []string{"sync-tls-ca", "sync-tls-cert", "sync-tls-key", "sync-token"} {
				if cmd.Flags().Changed(name) {
					return fmt.Errorf("--%s requires --sync-listen", name)
				}
			}
		}
		if adminToken == "" && !configsync.IsLoopbackListenAddress(addr) {
			slog.Warn("management API is exposed without --token; unauthenticated clients can access control-plane routes", "listen", addr)
		}

		// Record resolved listen addresses so CLI commands (nyro status,
		// nyro provider ls, …) can discover this control plane without a
		// hard-coded port. Write failures are non-fatal.
		serverState := state.ServerState{
			PID:         os.Getpid(),
			Listen:      addr,
			ProxyListen: proxyAddr,
			SyncListen:  grpcAddr,
			StartedAt:   time.Now(),
			AdminToken:  adminToken,
		}
		if err := state.Write(serverState); err != nil {
			slog.Warn("failed to write server state", "err", err)
		}
		defer state.Remove()

		var configTLS *tls.Config
		if grpcAddr != "" {
			var err error
			configTLS, err = resolveConfigSyncServerTLS(tlsCA, tlsCert, tlsKey)
			if err != nil {
				return err
			}
			// Fail closed before anything is opened: an unauthenticated
			// plaintext config-sync port on a routable address publishes every
			// upstream credential to whoever reaches it.
			if err := configsync.GuardPlaintextListen(grpcAddr, configTLS != nil, len(syncTokens) > 0); err != nil {
				return err
			}
			if configTLS == nil && len(syncTokens) > 0 {
				slog.Warn("config-sync is authenticated by join token over an unencrypted connection; the token crosses the network in the clear and can be replayed — prefer mTLS (`nyro tool ca`) off-host",
					"listen", grpcAddr)
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

		st, err := bootstrap.OpenStorageFromDSN(dsn, autoMigrate, plaintextKeys)
		if err != nil {
			return err
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		// ── First-boot settings seed (best-effort) ──
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

		// Time-trigger flush: the sinks flush on their size trigger (maxRows) or
		// on shutdown, so without this a low-traffic deployment would never fill a
		// buffer and /logs + /stats would stay empty until restart. The cadence is
		// the per-signal obs_<signal>_flush_interval settings (resolved into obsCfg
		// by LoadConfig, each defaulting to DefaultFlushInterval) — siblings of the
		// retention settings, edited in the WebUI's Local Telemetry card and applied
		// at boot. Whichever trigger fires first wins.
		rcv.StartFlusher(ctx, observability.SignalFlush{
			Logs:    obsCfg.LogsFlushInterval,
			Metrics: obsCfg.MetricsFlushInterval,
			Traces:  obsCfg.TracesFlushInterval,
		})

		// Janitor sweeps aged parquet files per signal on an hourly tick; exits
		// when ctx is cancelled (server shutdown).
		observability.StartJanitor(ctx, obsCfg.DataDir, observability.SignalRetention{
			Logs:    obsCfg.LogsRetentionDays,
			Metrics: obsCfg.MetricsRetentionDays,
			Traces:  obsCfg.TracesRetentionDays,
		}, time.Hour)

		// The config-sync server is needed whenever anything subscribes: a
		// remote proxy over TCP (--sync-listen), the embedded data plane over
		// the in-process pipe (--proxy-listen), or both. It is the single
		// config-push target, so a write reaches embedded and remote data
		// planes through exactly the same broadcast.
		var inProcDialOpts []grpc.DialOption
		if grpcAddr != "" || proxyAddr != "" {
			srv := configsync.NewConfigServer(st)
			admin.SetBroadcaster(srv)
			admin.SetNodeLister(srv)

			// Cross-replica epoch polling is only meaningful when another
			// replica can write to the same database, which the DSN scheme
			// already tells us — see epochPollInterval.
			watcher, err := startEpochWatcher(ctx, epochPollInterval(backend), st.Settings(), srv)
			if err != nil {
				return err
			}
			admin.SetEpochWatcher(watcher)

			if grpcAddr != "" {
				shutdown, err := configsync.ServeGRPC(ctx, grpcAddr, srv, configTLS, configsync.StreamTokenAuth(syncTokens))
				if err != nil {
					return err
				}
				defer shutdown()
				pki.WatchExpiry(ctx, configTLS, configExpiryCheckInterval, func(notAfter time.Time) {
					slog.Warn("config-sync server certificate expiring soon — run `nyro tool ca sign-server` and redistribute before it lapses",
						"not_after", notAfter, "remaining", time.Until(notAfter).Round(time.Hour))
				})
			}

			if proxyAddr != "" {
				var shutdown func()
				inProcDialOpts, shutdown = configsync.ServeInProcess(ctx, srv)
				defer shutdown()
			}
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

		servers := []bootstrap.HTTPServer{{Role: "control plane", Addr: addr, Handler: engine}}

		// ── Embedded data plane ──
		// Assembled through internal/dataplane over the in-process config-sync
		// pipe, so it runs the same client/cache/router path as a remote proxy.
		// The pipe has no listening socket, so the config-sync transport rules
		// (TLS, non-loopback plaintext) do not apply to it.
		if proxyAddr != "" {
			gw, obsMgr, err := dataplane.Build(ctx, dataplane.Options{
				SyncTarget:   configsync.InProcessTarget,
				SyncDialOpts: inProcDialOpts,
				ListenAddr:   proxyAddr,
			})
			if err != nil {
				return fmt.Errorf("embedded data plane: %w", err)
			}
			servers = append(servers, bootstrap.HTTPServer{
				Role:    "data plane",
				Addr:    proxyAddr,
				Handler: proxy.NewRouter(gw),
				// Flush telemetry while the control plane is still listening:
				// by default the embedded data plane exports OTLP to this same
				// process's receiver (see seedDefaultObsEndpoint), so a flush
				// after the control plane stops would fail connection-refused
				// on every clean shutdown.
				AfterShutdown: func() {
					shutCtx, shutCancel := context.WithTimeout(context.Background(), dataplane.ShutdownTimeout)
					defer shutCancel()
					if err := obsMgr.Shutdown(shutCtx); err != nil {
						slog.Warn("embedded data plane observability shutdown failed", "error", err)
					}
				},
			})
		}

		return bootstrap.RunServers(servers...)
	}
	return cmd
}

// configExpiryCheckInterval is how often WatchExpiry re-checks the loaded
// config-sync certificate once running (it always checks once immediately
// at startup too). Daily is frequent enough given the ExpiryWarningWindow is
// 30 days — see pki.WatchExpiry.
const configExpiryCheckInterval = 24 * time.Hour

// postgresEpochPollInterval is how often a server replica re-reads the shared
// config_epoch setting to notice writes handled by a sibling replica. It bounds
// cross-replica config propagation; a control plane runs a handful of replicas
// at most, so one indexed single-row read every 5s is not a meaningful load.
const postgresEpochPollInterval = 5 * time.Second

// epochPollInterval derives the cross-replica poll cadence from the storage
// backend instead of exposing it as a flag.
//
// The epoch watcher only exists to notice writes made by ANOTHER replica
// sharing the same database. Whether that is possible is already stated by the
// DSN: sqlite is a single-node file, so there is no sibling to hear from and
// polling is pure waste; postgres is the multi-replica backend. A flag whose
// only correct value is a deterministic function of another flag is not a knob,
// it is an opportunity to get it wrong — so the former --config-poll-interval
// was removed rather than kept as an override. If this cadence is ever wrong,
// the derivation is wrong and belongs fixed here, not guessed per deployment.
func epochPollInterval(backend string) time.Duration {
	if backend == "postgres" {
		return postgresEpochPollInterval
	}
	return 0
}

// startEpochWatcher seeds and runs the cross-replica epoch watcher. A
// non-positive interval disables polling and yields a nil observer, which the
// admin layer treats as "this replica is the only writer".
func startEpochWatcher(
	ctx context.Context,
	interval time.Duration,
	store configsync.EpochStore,
	notifier configsync.Notifier,
) (admin.EpochObserver, error) {
	if interval <= 0 {
		return nil, nil
	}

	watcher := configsync.NewEpochWatcher(store, notifier)
	if err := watcher.Seed(ctx); err != nil {
		return nil, fmt.Errorf("seed config epoch watcher: %w", err)
	}
	go watcher.Run(ctx, interval)
	return watcher, nil
}

// resolveConfigSyncServerTLS turns the --sync-tls-ca/-cert/-key flags into
// a *tls.Config for the config-sync gRPC server, or nil for plaintext.
//
//   - All three tls paths set: mTLS is used.
//   - Some but not all three set: a partial/likely-typo'd configuration —
//     fail fast rather than silently falling back to plaintext or guessing
//     which file is missing.
//   - None set: plaintext is used, silently. Whether that is acceptable is
//     decided by the bind address, not here: configsync.GuardPlaintextListen
//     refuses a non-loopback plaintext listener that has no join token either.
//     Warning unconditionally would fire on every loopback single-node start —
//     a warning nobody can act on, seen so often it trains operators to skip
//     the one that matters.
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
		return nil, fmt.Errorf("--sync-tls-ca, --sync-tls-cert, and --sync-tls-key must be set together (got %d of 3)", set)
	default:
		return nil, nil
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
