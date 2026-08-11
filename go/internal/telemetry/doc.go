// Package telemetry implements Nyro's runtime logs, metrics, and traces
// pipeline, including provider lifecycle, request instrumentation, OTLP
// projections, and statistics aggregation.
//
// Layer: 2 (observability runtime) — may import layers 0 and 1. Configuration
// consumers that only need exporter metadata must import telemetry/schema.
package telemetry
