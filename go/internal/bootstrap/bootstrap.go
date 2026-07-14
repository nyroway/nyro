// Package bootstrap holds the shared startup wiring used by the nyro gateway
// and admin commands: storage backend selection, OAuth driver registration,
// and the signal-driven HTTP server runner.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/database"
	"github.com/nyroway/nyro/go/migrations"
)

// ParseDSN parses a --dsn value into a storage backend name and the
// driver-native DSN that backend's constructor (NewSQLite/NewPostgres/
// NewMySQL) expects. Recognized schemes:
//   - "sqlite://<path>": path is everything after "sqlite://" verbatim, so
//     an absolute path is "sqlite:///abs/x.db", a relative path is
//     "sqlite://./x.db", and an in-memory DB is "sqlite://:memory:".
//   - "postgres://...": returned unchanged (with scheme) — gorm's postgres
//     driver (pgx) accepts the URL form natively. "postgresql://" (the other
//     libpq-recognized alias) is deliberately not accepted, to keep exactly
//     one spelling per backend.
//   - "mysql://user:pass@host:port/db?params": converted to gorm's mysql
//     DSN form "user:pass@tcp(host:port)/db?params" (defaulting the port to
//     3306 when omitted).
//
// Any other scheme (including "memory://" and "postgresql://") is a hard
// error — there is no ephemeral backend reachable through --dsn.
func ParseDSN(dsn string) (string, string, error) {
	switch {
	case strings.HasPrefix(dsn, "sqlite://"):
		return "sqlite", strings.TrimPrefix(dsn, "sqlite://"), nil
	case strings.HasPrefix(dsn, "postgres://"):
		return "postgres", dsn, nil
	case strings.HasPrefix(dsn, "mysql://"):
		driverDSN, err := mysqlURLToGormDSN(dsn)
		if err != nil {
			return "", "", fmt.Errorf("parse mysql dsn: %w", err)
		}
		return "mysql", driverDSN, nil
	default:
		return "", "", fmt.Errorf("unrecognized --dsn scheme %q (want sqlite://, postgres://, or mysql://)", dsn)
	}
}

// mysqlURLToGormDSN converts a "mysql://user:pass@host:port/db?params" URL
// into gorm's mysql driver DSN form "user:pass@tcp(host:port)/db?params",
// defaulting the port to 3306 when the URL omits it.
func mysqlURLToGormDSN(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "3306"
	}
	var userinfo string
	if u.User != nil {
		userinfo = u.User.String()
	}
	db := strings.TrimPrefix(u.Path, "/")
	driverDSN := fmt.Sprintf("%s@tcp(%s:%s)/%s", userinfo, host, port, db)
	if u.RawQuery != "" {
		driverDSN += "?" + u.RawQuery
	}
	return driverDSN, nil
}

// OpenStorageFromDSN parses dsn via ParseDSN and opens the resulting
// backend.
//
// autoMigrate controls whether the config-schema tables are created/altered
// via GORM AutoMigrate (DDL). Its default is false regardless of backend —
// whether the connecting account has DDL rights is a deployment-posture
// decision the operator makes explicitly, not something inferred from the
// database engine. When false, the backend instead gets a read-only schema
// version check: mysql/postgres compare against the versioned migrations
// under go/migrations/<dialect>/ (see the migrations package and
// go/atlas.hcl); sqlite just confirms its tables already exist.
func OpenStorageFromDSN(dsn string, autoMigrate bool) (storage.Storage, error) {
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
		return bootstrapSQL(b, "sqlite", autoMigrate)
	case "postgres":
		b, err := database.NewPostgres(driverDSN)
		if err != nil {
			return nil, fmt.Errorf("open postgres: %w", err)
		}
		return bootstrapSQL(b, "postgres", autoMigrate)
	case "mysql":
		b, err := database.NewMySQL(driverDSN)
		if err != nil {
			return nil, fmt.Errorf("open mysql: %w", err)
		}
		return bootstrapSQL(b, "mysql", autoMigrate)
	default:
		return nil, fmt.Errorf("unknown storage backend %q", backend)
	}
}

func bootstrapSQL(st storage.Storage, backend string, autoMigrate bool) (storage.Storage, error) {
	if err := st.Migrator().Init(); err != nil {
		return nil, fmt.Errorf("storage init: %w", err)
	}
	if autoMigrate {
		if err := st.Migrator().Migrate(); err != nil {
			return nil, fmt.Errorf("storage migrate: %w", err)
		}
		return st, nil
	}
	checker, ok := st.(interface{ CheckSchemaVersion(expected string) error })
	if !ok {
		// Backend has no versioned-schema concept (e.g. the in-memory test
		// backend) — nothing to check.
		return st, nil
	}
	var expected string
	if backend != "sqlite" {
		v, err := migrations.LatestVersion(backend)
		if err != nil {
			return nil, fmt.Errorf("determine expected schema version: %w", err)
		}
		expected = v
	}
	if err := checker.CheckSchemaVersion(expected); err != nil {
		return nil, fmt.Errorf("schema check failed (pass --auto-migrate to let nyro create/update the schema itself): %w", err)
	}
	return st, nil
}

// RunServer serves handler on addr until SIGINT/SIGTERM, then graceful-shutdown.
func RunServer(handler http.Handler, addr string) error {
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		slog.Info("nyro starting", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case <-stop:
		slog.Info("shutdown signal received")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(ctx)
}
