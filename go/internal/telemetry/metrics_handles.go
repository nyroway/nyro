package telemetry

import "go.opentelemetry.io/otel/metric"

// Handles owns the named OTel counter/histogram instruments the gateway emits
// per request. They are created once from a metric.Meter (the one assembled by
// Provider) and read by the telemetry Stage.
//
// Intentionally no global/init: Handles is constructed by the caller via
// NewHandles and reaches the Stage through a SwappableProvider, so
// instrumentation is inert until the data plane wires it — no process-wide
// side effects from importing this package.
type Handles struct {
	requests metric.Int64Counter     // nyro_requests_total: +1 per request, route/upstream/consumer/status attributes
	tokens   metric.Int64Counter     // nyro_tokens_total: prompt+completion, route/consumer/direction attributes
	latency  metric.Float64Histogram // nyro_request_latency_ms: total latency, route/upstream attributes
	// nyro_in_flight (Int64UpDownCounter) is intentionally NOT created here:
	// the telemetry Stage does not yet Inc/Dec a concurrency gauge, so
	// emitting it would publish a constant 0 (misleading). Reintroduce when
	// the Stage actually tracks in-flight requests around next().
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
	return h
}
