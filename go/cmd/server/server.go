// Package server implements the `nyro serve` subcommand: the control plane
// (management API + WebUI + OAuth session lifecycle + config-sync push).
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/spf13/cobra"
	"google.golang.org/grpc"

	dbsqlite "github.com/nyroway/nyro/go/infra/database/sqlite"
	infraobserve "github.com/nyroway/nyro/go/infra/observe"
	"github.com/nyroway/nyro/go/infra/observe/otlphttp"
	observesqlite "github.com/nyroway/nyro/go/infra/observe/sqlite"
	stateredis "github.com/nyroway/nyro/go/infra/state/redis"
	statesqlite "github.com/nyroway/nyro/go/infra/state/sqlite"
	"github.com/nyroway/nyro/go/internal/admin"
	"github.com/nyroway/nyro/go/internal/bootstrap"
	"github.com/nyroway/nyro/go/internal/configsync"
	"github.com/nyroway/nyro/go/internal/configsync/pki"
	"github.com/nyroway/nyro/go/internal/dataplane"
	"github.com/nyroway/nyro/go/internal/observability"
	"github.com/nyroway/nyro/go/internal/proxy"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/webui"
)

// nyroHomeDir returns ~/.nyro, the default home for local state. Falls back to
// "./.nyro" (relative to
// the working directory) if the OS user home directory can't be resolved —
// best-effort, never fatal, so admin still starts.
func nyroHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".nyro"
	}
	return filepath.Join(home, ".nyro")
}

func defaultDataDir() string { return filepath.Join(nyroHomeDir(), "data") }

// defaultDSN is the --dsn value used when the flag is left empty: a sqlite
// file under the admin-managed ~/.nyro home.
func defaultDSN() string {
	return "sqlite://" + filepath.Join(defaultDataDir(), "config.db")
}

// NewCmd builds the server (control-plane) subcommand.
//
// `nyro serve` is the single-command deployment: the REST API + WebUI on
// --listen, and — unless --disable-proxy is set — an embedded data plane on
// --proxy-listen, so one process is a complete, usable nyro. The embedded data
// plane is assembled by internal/dataplane over an in-process config-sync
// channel, i.e. the exact code path a remote `nyro proxy` uses; it never reads
// storage directly.
//
// It optionally also serves config-sync over TCP (--sync-listen) so additional
// `nyro proxy` nodes can subscribe. Every config write (upstreams, routes,
// consumers, settings) triggers an immediate push to all connected data planes,
// embedded and remote alike.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start Nyro with embedded services",
	}
	cmd.Flags().String("listen", "127.0.0.1:19531", "Control plane listen address")
	// Loopback, unlike a standalone `nyro proxy` (0.0.0.0): the single-binary
	// default is a local-first workstation install, so the embedded data plane
	// should not be reachable off-host until an operator says so. A deployment
	// that fronts nyro with nginx/envoy sets this explicitly, and a container
	// deployment must (loopback inside a container is unreachable from outside).
	cmd.Flags().String("proxy-listen", "127.0.0.1:19530", "Embedded data plane listen address")
	cmd.Flags().Bool("disable-proxy", false, "Disable the embedded data plane")
	cmd.Flags().String("redis-listen", "127.0.0.1:16379", "Embedded Redis listen address")
	cmd.Flags().String("redis-password", "", "Password for the embedded Redis server")
	cmd.Flags().String("state-data-dir", defaultDataDir(), "Directory containing state.db")
	cmd.Flags().Bool("disable-redis", false, "Disable the embedded Redis server")
	cmd.Flags().String("otlp-listen", "127.0.0.1:14318", "Embedded OTLP/HTTP listen address")
	cmd.Flags().String("observe-data-dir", defaultDataDir(), "Directory containing observe.db")
	cmd.Flags().Bool("disable-otlp", false, "Disable the embedded OTLP/HTTP receiver")
	// Empty by default: with an embedded data plane the single-node deployment
	// needs no config-sync port at all, so opening one is an opt-in taken only
	// when additional `nyro proxy` nodes must subscribe. This stream carries
	// every upstream's credentials_json, so plaintext mode logs a security
	// warning; operators can configure mTLS with --sync-tls-ca/-cert/-key.
	cmd.Flags().String("sync-listen", "", "Config sync server listen address")
	// Repeatable so a token can be rotated without downtime: add the new one,
	// roll the proxies onto it, then drop the old one. Prefer the env var —
	// a token passed as a flag is visible in `ps`.
	cmd.Flags().StringArray("sync-token", nil, "Token used by proxies to join config sync")
	cmd.Flags().String("sync-tls-ca", "", "CA certificate for config sync")
	cmd.Flags().String("sync-tls-cert", "", "Server certificate for config sync")
	cmd.Flags().String("sync-tls-key", "", "Server private key for config sync")
	cmd.Flags().String("token", "", "Bearer token for the management API")
	cmd.Flags().String("webui-dir", "", "Directory containing the built WebUI")
	cmd.Flags().String("dsn", "", "Configuration database DSN (default ~/.nyro/data/config.db)")
	cmd.Flags().Bool("auto-migrate", false, "Create or update database tables on startup")
	cmd.Flags().Bool("raw-api-keys", false, "Store recoverable API keys")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		addr, _ := cmd.Flags().GetString("listen")
		proxyAddr, _ := cmd.Flags().GetString("proxy-listen")
		disableProxy, _ := cmd.Flags().GetBool("disable-proxy")
		redisAddr, _ := cmd.Flags().GetString("redis-listen")
		redisPassword, _ := cmd.Flags().GetString("redis-password")
		stateDataDir, _ := cmd.Flags().GetString("state-data-dir")
		disableRedis, _ := cmd.Flags().GetBool("disable-redis")
		otlpAddr, _ := cmd.Flags().GetString("otlp-listen")
		observeDataDir, _ := cmd.Flags().GetString("observe-data-dir")
		disableOTLP, _ := cmd.Flags().GetBool("disable-otlp")
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
		if addr == "" {
			return errors.New("--listen must not be empty")
		}
		if !disableProxy && proxyAddr == "" {
			return errors.New("--proxy-listen must not be empty unless --disable-proxy is set")
		}
		if !disableRedis {
			if redisAddr == "" {
				return errors.New("--redis-listen must not be empty unless --disable-redis is set")
			}
			if !configsync.IsLoopbackListenAddress(redisAddr) {
				return fmt.Errorf("--redis-listen must use a loopback address: %q", redisAddr)
			}
		}
		if !disableOTLP {
			if otlpAddr == "" {
				return errors.New("--otlp-listen must not be empty unless --disable-otlp is set")
			}
			if !configsync.IsLoopbackListenAddress(otlpAddr) {
				return fmt.Errorf("--otlp-listen must use a loopback address: %q", otlpAddr)
			}
		}
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

		if !disableRedis {
			if err := prepareDataDir(cmd, "state-data-dir", stateDataDir); err != nil {
				return err
			}
		}
		if err := prepareDataDir(cmd, "observe-data-dir", observeDataDir); err != nil {
			return err
		}

		st, err := bootstrap.OpenStorageFromDSN(dsn, autoMigrate, plaintextKeys)
		if err != nil {
			return err
		}

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		if !disableOTLP {
			seedDefaultObsEndpoint(st.Settings(), otlpAddr)
		}

		obsCfg, err := observability.LoadConfig(st.Settings().Get)
		if err != nil {
			return fmt.Errorf("load observability config: %w", err)
		}
		observeDB, err := dbsqlite.Open(ctx, dbsqlite.Options{Path: filepath.Join(observeDataDir, "observe.db")})
		if err != nil {
			return fmt.Errorf("open observe database: %w", err)
		}
		defer func() { _ = observeDB.Close() }()
		observeStore, err := observesqlite.New(ctx, observeDB, observesqlite.Options{
			LogsRetention:    time.Duration(obsCfg.LogsRetentionDays) * 24 * time.Hour,
			MetricsRetention: time.Duration(obsCfg.MetricsRetentionDays) * 24 * time.Hour,
			TracesRetention:  time.Duration(obsCfg.TracesRetentionDays) * 24 * time.Hour,
			IndexedLogAttributes: []infraobserve.AttributeIndex{
				{Key: "nyro.log.id", Type: infraobserve.AttributeString},
				{Key: "nyro.upstream.id", Type: infraobserve.AttributeString},
				{Key: "nyro.route.id", Type: infraobserve.AttributeString},
				{Key: "nyro.route.model", Type: infraobserve.AttributeString},
				{Key: "nyro.consumer.id", Type: infraobserve.AttributeString},
				{Key: "http.response.status_code", Type: infraobserve.AttributeInt64},
			},
		})
		if err != nil {
			return fmt.Errorf("open observe store: %w", err)
		}
		defer func() {
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer shutdownCancel()
			if err := observeStore.Shutdown(shutdownCtx); err != nil {
				slog.Warn("observe store shutdown failed", "error", err)
			}
		}()
		var otlpReceiver *otlphttp.Receiver
		if !disableOTLP {
			otlpReceiver, err = otlphttp.New(otlphttp.Options{
				Store: observeStore,
				OnPersistError: func(err error) {
					slog.Error("OTLP persistence failed after acknowledgement", "error", err)
				},
			})
			if err != nil {
				return fmt.Errorf("create OTLP receiver: %w", err)
			}
			defer func() {
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer shutdownCancel()
				if err := otlpReceiver.Shutdown(shutdownCtx); err != nil {
					slog.Warn("OTLP receiver shutdown failed", "error", err)
				}
			}()
		}

		var redisServer *stateredis.Server
		if !disableRedis {
			stateDB, err := dbsqlite.Open(ctx, dbsqlite.Options{Path: filepath.Join(stateDataDir, "state.db")})
			if err != nil {
				return fmt.Errorf("open state database: %w", err)
			}
			defer func() { _ = stateDB.Close() }()
			stateStore, err := statesqlite.New(ctx, stateDB, statesqlite.Options{})
			if err != nil {
				return fmt.Errorf("open state store: %w", err)
			}
			defer func() {
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer shutdownCancel()
				if err := stateStore.Shutdown(shutdownCtx); err != nil {
					slog.Warn("state store shutdown failed", "error", err)
				}
			}()
			redisServer, err = stateredis.New(stateredis.Options{Store: stateStore, Password: redisPassword})
			if err != nil {
				return fmt.Errorf("create Redis server: %w", err)
			}
		}

		// The config-sync server is needed whenever anything subscribes: a
		// remote proxy over TCP (--sync-listen), the embedded data plane over
		// the in-process pipe (--proxy-listen), or both. It is the single
		// config-push target, so a write reaches embedded and remote data
		// planes through exactly the same broadcast.
		var inProcDialOpts []grpc.DialOption
		if grpcAddr != "" || !disableProxy {
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

			if !disableProxy {
				var shutdown func()
				inProcDialOpts, shutdown = configsync.ServeInProcess(ctx, srv)
				defer shutdown()
			}
		}

		engine := chi.NewRouter()
		engine.Use(middleware.Recoverer)
		observeSource := admin.NewObserveSource(observeStore)
		admin.Mount(engine, st, adminToken, observeSource, observeSource)
		webui.Mount(engine, webuiDir)

		var dataPlaneHandler http.Handler
		var dataPlaneAfterShutdown func()
		if !disableProxy {
			gw, obsMgr, err := dataplane.Build(ctx, dataplane.Options{
				SyncTarget:   configsync.InProcessTarget,
				SyncDialOpts: inProcDialOpts,
				ListenAddr:   proxyAddr,
			})
			if err != nil {
				return fmt.Errorf("embedded data plane: %w", err)
			}
			dataPlaneHandler = proxy.NewRouter(gw)
			var shutdownOnce sync.Once
			dataPlaneAfterShutdown = func() {
				shutdownOnce.Do(func() {
					shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), dataplane.ShutdownTimeout)
					defer shutdownCancel()
					if err := obsMgr.Shutdown(shutdownCtx); err != nil {
						slog.Warn("embedded data plane observability shutdown failed", "error", err)
					}
				})
			}
			defer dataPlaneAfterShutdown()
		}

		var listeners []net.Listener
		defer func() {
			for _, listener := range listeners {
				_ = listener.Close()
			}
		}()
		managed := make([]bootstrap.ManagedServer, 0, 4)
		addHTTP := func(role, address string, handler http.Handler, after func()) error {
			listener, err := net.Listen("tcp", address)
			if err != nil {
				return fmt.Errorf("%s listener %q: %w", role, address, err)
			}
			listeners = append(listeners, listener)
			server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
			managed = append(managed, bootstrap.ManagedServer{
				Role: role,
				Serve: func() error {
					if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
						return err
					}
					return nil
				},
				Shutdown:      server.Shutdown,
				AfterShutdown: after,
			})
			return nil
		}

		// Dependencies are registered before consumers because shutdown is
		// reversed: the data plane flushes while OTLP is still accepting work.
		if otlpReceiver != nil {
			if err := addHTTP("OTLP receiver", otlpAddr, otlpReceiver.Handler(), func() {
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer shutdownCancel()
				if err := otlpReceiver.Shutdown(shutdownCtx); err != nil {
					slog.Warn("OTLP receiver shutdown failed", "error", err)
				}
			}); err != nil {
				return err
			}
		}
		if redisServer != nil {
			listener, err := net.Listen("tcp", redisAddr)
			if err != nil {
				return fmt.Errorf("redis listener %q: %w", redisAddr, err)
			}
			listeners = append(listeners, listener)
			managed = append(managed, bootstrap.ManagedServer{
				Role: "Redis state server", Serve: func() error { return redisServer.Serve(listener) }, Shutdown: redisServer.Shutdown,
			})
		}
		if err := addHTTP("control plane", addr, engine, nil); err != nil {
			return err
		}
		if dataPlaneHandler != nil {
			if err := addHTTP("data plane", proxyAddr, dataPlaneHandler, dataPlaneAfterShutdown); err != nil {
				return err
			}
		}
		return bootstrap.RunManagedServers(managed...)
	}
	return cmd
}

func prepareDataDir(cmd *cobra.Command, flag, dir string) error {
	if cmd.Flags().Changed(flag) {
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			return fmt.Errorf("--%s %q does not exist (create it first, or leave --%s unset to use the default under ~/.nyro/data)", flag, dir, flag)
		}
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create --%s directory %q: %w", flag, dir, err)
	}
	return nil
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
// to point at this process's own --otlp-listen, and defaults any still-empty
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
