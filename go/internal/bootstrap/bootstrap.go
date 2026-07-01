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
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
	"github.com/nyroway/nyro/go/internal/storage/sqlite"
)

// OpenStorage selects and opens the storage backend. "memory" (the default) is
// ephemeral; "sqlite"/"postgres"/"mysql" open a persistent DB and apply the
// schema via Migrate.
func OpenStorage(backend, dsn string) (storage.Storage, error) {
	switch backend {
	case "", "memory":
		return memory.New().Storage(), nil
	case "sqlite":
		b, err := sqlite.New(dsn)
		if err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
		}
		return bootstrapSQL(b.Storage())
	case "postgres":
		b, err := sqlite.NewPostgres(dsn)
		if err != nil {
			return nil, fmt.Errorf("open postgres: %w", err)
		}
		return bootstrapSQL(b.Storage())
	case "mysql":
		b, err := sqlite.NewMySQL(dsn)
		if err != nil {
			return nil, fmt.Errorf("open mysql: %w", err)
		}
		return bootstrapSQL(b.Storage())
	default:
		return nil, fmt.Errorf("unknown storage backend %q (want memory|sqlite|postgres|mysql)", backend)
	}
}

func bootstrapSQL(st storage.Storage) (storage.Storage, error) {
	if err := st.Bootstrap().Init(); err != nil {
		return nil, fmt.Errorf("storage init: %w", err)
	}
	if err := st.Bootstrap().Migrate(); err != nil {
		return nil, fmt.Errorf("storage migrate: %w", err)
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
