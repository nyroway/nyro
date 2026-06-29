// Command nyro-server runs the Nyro gateway: the proxy surface (OpenAI /
// Anthropic / Gemini-compatible endpoints forwarding to upstream providers)
// and the admin surface (management API consumed by the WebUI).
//
// P1 wires the OpenAI-compatible /v1/chat/completions path end-to-end against
// a single configured upstream. Admin surface + multi-provider routing land in
// later phases.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/nyroway/nyro/go/internal/admin"
	"github.com/nyroway/nyro/go/internal/auth"
	"github.com/nyroway/nyro/go/internal/auth/drivers"
	"github.com/nyroway/nyro/go/internal/proxy"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
	"github.com/nyroway/nyro/go/internal/storage/sqlite"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:19530", "listen address for the proxy/admin surface")
	upstreamURL := flag.String("upstream-url", "", "upstream base URL, e.g. https://api.openai.com")
	upstreamKey := flag.String("upstream-key", "", "upstream API key")
	alias := flag.String("alias", "gpt-4o", "client-facing model alias")
	upstreamModel := flag.String("upstream-model", "gpt-4o", "actual upstream model name")
	adminToken := flag.String("admin-token", "", "Bearer token protecting /api/v1 admin routes")
	webuiDir := flag.String("webui-dir", "", "path to the built WebUI (serves the SPA at /)")
	storageBackend := flag.String("storage", "memory", "storage backend: memory|sqlite|postgres|mysql")
	dbDSN := flag.String("db-dsn", "", "database path/DSN for persistent backends (sqlite file or postgres/mysql DSN)")
	flag.Parse()

	st, err := newStorage(*storageBackend, *dbDSN)
	if err != nil {
		slog.Error("storage init failed", "backend", *storageBackend, "error", err)
		os.Exit(1)
	}
	if *upstreamURL != "" {
		prov, err := st.Providers().Create(storage.CreateProvider{
			Name: "default", Protocol: "openai-compatible", BaseURL: *upstreamURL, APIKey: *upstreamKey,
		})
		if err == nil {
			_, _ = st.Models().Create(storage.CreateModel{
				Name:    *alias,
				Targets: []storage.CreateModelBackend{{ProviderID: prov.ID, Model: *upstreamModel}},
			})
		}
		slog.Info("configured upstream route", "alias", *alias, "model", *upstreamModel, "base_url", *upstreamURL)
	} else {
		slog.Warn("no upstream configured; chat endpoints return 404 until --upstream-url is set")
	}

	// OAuth driver registry + session store.
	authReg := auth.NewRegistry()
	registerDrivers(authReg)
	sessions := auth.NewSessionStore()
	// Periodic cleanup of expired sessions.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			sessions.Cleanup(10 * time.Minute)
		}
	}()

	// Periodic log retention cleanup (default 7 days, configurable via the
	// log_retention_days setting). Prevents unbounded storage growth.
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			retentionDays := 7
			if v, _ := st.Settings().Get("log_retention_days"); v != "" {
				if d, err := strconv.Atoi(v); err == nil && d > 0 {
					retentionDays = d
				}
			}
			cutoff := time.Now().UnixMilli() - int64(retentionDays)*24*60*60*1000
			if n, err := st.Logs().DeleteBefore(cutoff); err != nil {
				slog.Warn("log retention cleanup failed", "error", err)
			} else if n > 0 {
				slog.Info("log retention cleanup", "deleted", n, "retention_days", retentionDays)
			}
		}
	}()

	gw := proxy.NewGateway(st)
	gw.SetDriverRegistry(authReg)
	gw.StartOAuthRefreshLoop(context.Background())
	engine := proxy.NewRouter(gw)
	admin.Mount(engine, gw.Storage, *adminToken)
	admin.MountOAuth(engine, gw.Storage, authReg, sessions)
	proxy.MountWebui(engine, *webuiDir)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           engine,
		ReadHeaderTimeout: 10 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("nyro-server starting", "addr", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		slog.Error("server failed", "error", err)
		os.Exit(1)
	case <-stop:
		slog.Info("shutdown signal received")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
}

// registerDrivers wires the outbound OAuth drivers into the registry: Claude
// (PKCE auth-code), Codex (device-code), and Vertex (service-account JSON).
func registerDrivers(reg *auth.Registry) {
	reg.Register("claude-code", drivers.NewClaudeDriver())
	reg.Register("codex", drivers.NewCodexDriver())
	reg.Register("vertexai", drivers.NewVertexDriver())
}

// newStorage selects and opens the storage backend. "memory" (the default) is
// ephemeral — fine for dev/desktop; "sqlite"/"postgres"/"mysql" open a
// persistent DB and apply the schema via Migrate so OAuth credentials, config,
// and logs survive restarts (cutover blocker B1).
func newStorage(backend, dsn string) (storage.Storage, error) {
	switch backend {
	case "", "memory":
		return memory.New().Storage(), nil
	case "sqlite":
		b, err := sqlite.New(dsn)
		if err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
		}
		return bootstrapStorage(b.Storage())
	case "postgres":
		b, err := sqlite.NewPostgres(dsn)
		if err != nil {
			return nil, fmt.Errorf("open postgres: %w", err)
		}
		return bootstrapStorage(b.Storage())
	case "mysql":
		b, err := sqlite.NewMySQL(dsn)
		if err != nil {
			return nil, fmt.Errorf("open mysql: %w", err)
		}
		return bootstrapStorage(b.Storage())
	default:
		return nil, fmt.Errorf("unknown storage backend %q (want memory|sqlite|postgres|mysql)", backend)
	}
}

// bootstrapStorage runs Init + Migrate on a persistent backend and returns it.
func bootstrapStorage(st storage.Storage) (storage.Storage, error) {
	if err := st.Bootstrap().Init(); err != nil {
		return nil, fmt.Errorf("storage init: %w", err)
	}
	if err := st.Bootstrap().Migrate(); err != nil {
		return nil, fmt.Errorf("storage migrate: %w", err)
	}
	return st, nil
}
