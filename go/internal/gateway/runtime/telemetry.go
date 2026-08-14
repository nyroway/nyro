package runtime

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"reflect"
	"sync"
	"time"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/telemetry"
)

// obsReloadGrace is how long a displaced telemetry.Provider is kept alive
// after a hot-reload before being shut down. In-flight requests that already
// loaded the old SwappableProvider set (e.g. a span started under the previous
// tracer) finish emitting through it during this window; a request longer than
// the grace may lose its final span/log export, which is acceptable for the
// rare event of an operator changing observability config mid-flight.
const obsReloadGrace = 30 * time.Second

// ObsManager owns the data plane's hot-reloadable observability pipeline. The OTel
// provider is built once at startup (in --server mode, from the still-empty
// cache, so it resolves to the fixed stdout default) and rebuilt whenever a
// config-sync snapshot changes the resolved telemetry.Config — this is what lets the
// control-plane-seeded otlp settings, which only arrive AFTER startup over the
// config stream, actually take effect. The telemetry Stage reads the current
// pipeline through sp (SwappableProvider) so a rebuild never re-registers it.
type ObsManager struct {
	ctx   context.Context
	cache *configsnapshot.Cache
	sp    *telemetry.SwappableProvider

	mu         sync.Mutex
	lastCfg    telemetry.Config
	current    *telemetry.Provider
	metricsSrv *http.Server // running prometheus scrape server, nil if none
}

// newObsManager wires the manager around an already-built initial provider and
// its resolved config, and starts the prometheus scrape server if that initial
// config selected the prometheus metrics exporter. It does NOT register the
// SetOnSwap callback — the caller does that (only --server mode needs it),
// after ensuring no snapshot can be published before the callback is in place.
func newObsManager(ctx context.Context, cache *configsnapshot.Cache, sp *telemetry.SwappableProvider, initial *telemetry.Provider, initialCfg telemetry.Config) *ObsManager {
	m := &ObsManager{
		ctx:     ctx,
		cache:   cache,
		sp:      sp,
		lastCfg: initialCfg,
		current: initial,
	}
	m.mu.Lock()
	m.startMetricsLocked(initial, initialCfg.Metrics.Params["path"])
	m.mu.Unlock()
	return m
}

// rebuild re-resolves observability config from the current cache snapshot and,
// if it changed, builds a fresh provider, swaps it into sp (so hooks pick it up),
// reconciles the prometheus scrape server, and schedules the displaced provider
// for shutdown after a grace period. Manager invokes it from the single
// SetOnSwap callback after each snapshot.
//
// A build failure is non-fatal: the current provider keeps serving and the error
// is logged, so a malformed obs setting pushed by the control plane never breaks
// telemetry (or, worse, request serving) on the data plane.
func (m *ObsManager) rebuild() {
	newCfg := resolveObsConfig(m.cache)

	m.mu.Lock()
	if reflect.DeepEqual(newCfg, m.lastCfg) {
		m.mu.Unlock()
		return
	}
	newProv, err := telemetry.NewProvider(m.ctx, newCfg)
	if err != nil {
		m.mu.Unlock()
		slog.Error("observability hot-reload: building new provider failed; keeping current pipeline", "error", err)
		return
	}
	old := m.current
	m.sp.Swap(newProv)
	m.current = newProv
	m.lastCfg = newCfg
	m.stopMetricsLocked()
	m.startMetricsLocked(newProv, newCfg.Metrics.Params["path"])
	m.mu.Unlock()

	slog.Info("observability hot-reload: applied new config from control plane",
		"logs", newCfg.Logs.Kind, "metrics", newCfg.Metrics.Kind, "traces", newCfg.Traces.Kind)

	// Shut the displaced provider down after a grace period so in-flight
	// requests holding the old set can flush. Skipped when old==newProv (never)
	// or old is nil.
	if old != nil && old != newProv {
		time.AfterFunc(obsReloadGrace, func() {
			shutCtx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
			defer cancel()
			if err := old.Shutdown(shutCtx); err != nil {
				slog.Warn("observability hot-reload: displaced provider shutdown failed", "error", err)
			}
		})
	}
}

// startMetricsLocked starts the prometheus scrape server for prov when its
// metrics exporter is prometheus (PromHandler != nil); a no-op otherwise. The
// caller must hold m.mu. Best-effort: a bind/serve failure is logged but never
// fatal, matching the original startMetricsServer contract.
func (m *ObsManager) startMetricsLocked(prov *telemetry.Provider, path string) {
	if prov == nil || prov.PromHandler == nil {
		return
	}
	srv := newMetricsServer(prov, path)
	m.metricsSrv = srv
	go func() {
		slog.Info("prometheus metrics server starting", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("prometheus metrics server failed", "error", err)
		}
	}()
}

// stopMetricsLocked synchronously shuts down the running scrape server (if any).
// Synchronous (rather than ctx-cancel + async) so a same-address restart during
// reconciliation cannot race the old server for the port. The caller must hold
// m.mu.
func (m *ObsManager) stopMetricsLocked() {
	if m.metricsSrv == nil {
		return
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), ShutdownTimeout)
	defer cancel()
	if err := m.metricsSrv.Shutdown(shutCtx); err != nil {
		slog.Warn("prometheus metrics server shutdown failed", "error", err)
	}
	m.metricsSrv = nil
}

// Shutdown stops the scrape server and flushes the current provider. Called via
// the data plane's graceful-exit defer. Displaced providers scheduled by a prior
// rebuild are torn down by process exit if their grace timer has not yet fired.
func (m *ObsManager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopMetricsLocked()
	if m.current != nil {
		return m.current.Shutdown(ctx)
	}
	return nil
}

// ShutdownTimeout bounds the OTel provider flush on graceful exit. Exported so
// the commands that own the process lifetime can budget the same window.
const ShutdownTimeout = 5 * time.Second
