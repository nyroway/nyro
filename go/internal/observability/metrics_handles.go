package observability

import "go.opentelemetry.io/otel/metric"

// Handles owns the named OTel counter/histogram instruments the gateway emits
// per request. They are created once from a metric.Meter (the one assembled by
// ObsProvider in T3.1) and referenced by the OnLog phase hook.
//
// Intentionally no global/init: Handles is constructed by the caller via
// NewHandles and passed to RegisterHooks, so instrumentation is inert until the
// dispatcher/cmd wires it (T3.3/T3.4) — no process-wide side effects from
// importing this package.
type Handles struct {
	requests metric.Int64Counter       // nyro_requests_total: +1 per request, attr model/provider/apikey/status_class
	tokens   metric.Int64Counter       // nyro_tokens_total: prompt+completion, attr model/apikey/direction
	latency  metric.Float64Histogram   // nyro_request_latency_ms: total latency, attr model/provider
	inFlight metric.Int64UpDownCounter // nyro_in_flight: current concurrent requests (currently unused by the OnLog hook; reserved for OnRequest/OnLog pairing)
}

// NewHandles creates the named instruments from m. Errors are intentionally
// ignored (matching the OTel "non-fatal" contract: a duplicate-name instrument
// returns the existing one with a logged conflict, never a hard failure). A nil
// meter yields nil handles; the Add/Record calls below are nil-safe no-ops on a
// noop meter.
func NewHandles(m metric.Meter) *Handles {
	h := &Handles{}
	h.requests, _ = m.Int64Counter("nyro_requests_total")
	h.tokens, _ = m.Int64Counter("nyro_tokens_total")
	h.latency, _ = m.Float64Histogram("nyro_request_latency_ms", metric.WithUnit("ms"))
	h.inFlight, _ = m.Int64UpDownCounter("nyro_in_flight")
	return h
}
