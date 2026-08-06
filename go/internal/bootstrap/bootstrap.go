// Package bootstrap holds the shared startup wiring used by the nyro data plane
// and admin commands: storage backend selection, OAuth driver registration,
// and the signal-driven HTTP server runner.
//
// Layer: 3 (serve) — grouped by responsibility (process startup), not by
// dependency count: it imports only storage today but belongs with the other
// wiring packages. May import any lower layer; nothing below layer 3 may
// import it.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/database"
)

// ParseDSN parses a --dsn value into a storage backend name and the
// driver-native DSN that backend's constructor (NewSQLite/NewPostgres)
// expects. Recognized schemes:
//   - "sqlite://<path>": path is everything after "sqlite://" verbatim, so
//     an absolute path is "sqlite:///abs/x.db", a relative path is
//     "sqlite://./x.db", and an in-memory DB is "sqlite://:memory:".
//   - "postgres://...": returned unchanged (with scheme) — gorm's postgres
//     driver (pgx) accepts the URL form natively. "postgresql://" (the other
//     libpq-recognized alias) is deliberately not accepted, to keep exactly
//     one spelling per backend.
//
// Any other scheme (including "mysql://", "memory://" and "postgresql://") is
// a hard error — there is no ephemeral backend reachable through --dsn.
func ParseDSN(dsn string) (string, string, error) {
	switch {
	case strings.HasPrefix(dsn, "sqlite://"):
		return "sqlite", strings.TrimPrefix(dsn, "sqlite://"), nil
	case strings.HasPrefix(dsn, "postgres://"):
		return "postgres", dsn, nil
	default:
		return "", "", fmt.Errorf("unrecognized --dsn scheme %q (want sqlite:// or postgres://)", dsn)
	}
}

// OpenStorageFromDSN parses dsn via ParseDSN and opens the resulting
// backend.
//
// autoMigrate controls whether the config-schema tables are created/altered
// via GORM AutoMigrate (DDL). Its default is false regardless of backend —
// whether the connecting account has DDL rights is a deployment-posture
// decision the operator makes explicitly, not something inferred from the
// database engine. When false, the backend instead gets a read-only schema
// check (Backend.CheckSchema): it confirms the canonical tables already exist
// (all backends), without doing any DDL.
//
// plaintextKeys, when true, makes the backend store the recoverable raw API
// key alongside its hash on creation, so keys can be retrieved after creation
// (e.g. to display/copy a full key in the UI). Default false (hash-only); it
// never affects the inbound auth path, which always compares hashes.
func OpenStorageFromDSN(dsn string, autoMigrate, plaintextKeys bool) (storage.Storage, error) {
	backend, driverDSN, err := ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	switch backend {
	case "sqlite":
		b, err := database.NewSQLite(driverDSN)
		if err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
		}
		b.SetPlaintextKeys(plaintextKeys)
		return bootstrapSQL(b, autoMigrate)
	case "postgres":
		b, err := database.NewPostgres(driverDSN)
		if err != nil {
			return nil, fmt.Errorf("open postgres: %w", err)
		}
		b.SetPlaintextKeys(plaintextKeys)
		return bootstrapSQL(b, autoMigrate)
	default:
		return nil, fmt.Errorf("unknown storage backend %q", backend)
	}
}

func bootstrapSQL(st storage.Storage, autoMigrate bool) (storage.Storage, error) {
	if err := st.Migrator().Init(); err != nil {
		return nil, fmt.Errorf("storage init: %w", err)
	}
	if autoMigrate {
		if err := st.Migrator().Migrate(); err != nil {
			return nil, fmt.Errorf("storage migrate: %w", err)
		}
		return st, nil
	}
	checker, ok := st.(interface{ CheckSchema() error })
	if !ok {
		// Backend has no schema concept (e.g. the in-memory test backend) —
		// nothing to check.
		return st, nil
	}
	if err := checker.CheckSchema(); err != nil {
		return nil, fmt.Errorf("schema check failed (pass --auto-migrate to let nyro create/update the schema itself): %w", err)
	}
	return st, nil
}

// RunServer serves handler on addr until SIGINT/SIGTERM, then graceful-shutdown.
func RunServer(handler http.Handler, addr string) error {
	return RunServers(HTTPServer{Role: "nyro", Addr: addr, Handler: handler})
}

// HTTPServer pairs a handler with the address it listens on and the role name
// used in its startup log line.
type HTTPServer struct {
	Role    string
	Addr    string
	Handler http.Handler

	// AfterShutdown, if set, runs once this server has stopped accepting but
	// before any earlier-registered server is shut down. Use it for drain work
	// that still needs an earlier server to be up — see RunServers.
	AfterShutdown func()
}

// ManagedServer is a blocking server loop plus its graceful shutdown hook.
// Register dependencies before their consumers; shutdown runs in reverse.
type ManagedServer struct {
	Role          string
	Serve         func() error
	Shutdown      func(context.Context) error
	AfterShutdown func()
}

// RunServers starts every server and blocks until SIGINT/SIGTERM or the first
// listen error, then gracefully shuts all of them down. `nyro serve` uses it
// for the control plane and its independently enabled data plane, Redis state
// server, and OTLP receiver. All listeners share one signal handler so a
// single Ctrl-C drains the process rather than leaving one service running.
//
// Shutdown runs in REVERSE registration order, each server's AfterShutdown
// firing before the next one is stopped. Register dependencies first: in `nyro
// serve` the control plane is also the embedded data plane's OTLP sink, so
// stopping it first would make the data plane's final telemetry flush fail with
// connection-refused on every clean exit.
//
// A listen error on ANY server aborts the whole process: a half-up `nyro
// serve` (management API reachable, data plane port already taken) is a
// confusing state to debug, and failing fast surfaces the port conflict
// immediately.
func RunServers(servers ...HTTPServer) error {
	managed := make([]ManagedServer, 0, len(servers))
	for _, s := range servers {
		srv := &http.Server{Addr: s.Addr, Handler: s.Handler, ReadHeaderTimeout: 10 * time.Second}
		managed = append(managed, ManagedServer{
			Role: s.Role,
			Serve: func() error {
				if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					return err
				}
				return nil
			},
			Shutdown:      srv.Shutdown,
			AfterShutdown: s.AfterShutdown,
		})
	}
	return RunManagedServers(managed...)
}

// RunManagedServers starts all service loops and blocks until a signal or the
// first service error, then shuts services down in reverse registration order.
func RunManagedServers(servers ...ManagedServer) error {
	errCh := make(chan error, len(servers))
	for _, server := range servers {
		server := server
		go func() {
			slog.Info("nyro starting", "role", server.Role)
			if err := server.Serve(); err != nil {
				errCh <- fmt.Errorf("%s listener: %w", server.Role, err)
			}
		}()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	var runErr error
	select {
	case runErr = <-errCh:
	case <-stop:
		slog.Info("shutdown signal received")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i := len(servers) - 1; i >= 0; i-- {
		if err := servers[i].Shutdown(ctx); err != nil && runErr == nil {
			runErr = err
		}
		if fn := servers[i].AfterShutdown; fn != nil {
			fn()
		}
	}
	return runErr
}
