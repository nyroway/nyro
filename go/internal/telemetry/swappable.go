package telemetry

import (
	"sync/atomic"

	"go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/trace"
)

// activeSet is the immutable bundle of signal handles the telemetry Phase
// reads on every request: the tracer that starts the per-request span, the
// logger that emits the audit record, and the metric instruments. prov is the
// Provider these were derived from (nil when the set was assembled from raw
// parts, e.g. in tests) — retained so a Swap can hand the displaced provider
// back to the caller for a graceful Shutdown.
type activeSet struct {
	tracer  trace.Tracer
	logger  log.Logger
	handles *Handles
	prov    *Provider
}

// SwappableProvider is the indirection that makes observability hot-reloadable.
// The telemetry Phase holds a *SwappableProvider rather than a concrete
// tracer/logger/handles; each request reads the current activeSet via load().
// A config-sync push that changes the resolved Config calls Swap to install
// a freshly built Provider atomically, so in-flight requests keep using the
// set they already loaded while new requests pick up the new pipeline — no
// rebuild of the Runner and no lock on the hot path.
type SwappableProvider struct {
	cur atomic.Pointer[activeSet]
}

// activeSetFromProvider derives the hook-facing handle bundle from p. The metric
// instruments are (re)created from p.Meter so they bind to the new provider's
// MeterProvider — reusing an old Handles would keep emitting into the previous,
// possibly shut-down, meter.
func activeSetFromProvider(p *Provider) *activeSet {
	return &activeSet{
		tracer:  p.Tracer,
		logger:  p.Logger,
		handles: NewHandles(p.Meter),
		prov:    p,
	}
}

// NewSwappableProvider builds a SwappableProvider whose initial active set is
// derived from p (its tracer/logger and a fresh Handles from p.Meter).
func NewSwappableProvider(p *Provider) *SwappableProvider {
	s := &SwappableProvider{}
	s.cur.Store(activeSetFromProvider(p))
	return s
}

// NewSwappableProviderFromSignals binds one generation-local active set to
// independently lifecycle-managed signal providers.
func NewSwappableProviderFromSignals(logs, metrics, traces *Provider) *SwappableProvider {
	s := &SwappableProvider{}
	s.cur.Store(&activeSet{
		logger:  logs.Logger,
		tracer:  traces.Tracer,
		handles: NewHandles(metrics.Meter),
	})
	return s
}

// newSwappableFromParts builds a SwappableProvider directly from raw handles,
// bypassing a Provider. Used by tests that assemble a tracer/logger/handles
// harness without a full provider; its active set carries no prov, so Swap
// against a real provider later returns nil for the displaced one.
func newSwappableFromParts(tracer trace.Tracer, logger log.Logger, handles *Handles) *SwappableProvider {
	s := &SwappableProvider{}
	s.cur.Store(&activeSet{tracer: tracer, logger: logger, handles: handles})
	return s
}

// load returns the current active set (never nil once constructed).
func (s *SwappableProvider) load() *activeSet { return s.cur.Load() }

// Swap atomically installs a new active set derived from p and returns the
// Provider that was previously active, or nil if there was none (or the
// previous set was assembled from raw parts). The caller owns Shutdown of the
// returned provider and should defer it past in-flight requests that may still
// hold the displaced set.
func (s *SwappableProvider) Swap(p *Provider) *Provider {
	old := s.cur.Swap(activeSetFromProvider(p))
	if old == nil {
		return nil
	}
	return old.prov
}
