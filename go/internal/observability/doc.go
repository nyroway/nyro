// Package observability implements nyro's three-signal telemetry model.
//
// Layering: the data plane (gateway) only ever EMITS signals (via the OTel SDK
// and configurable exporters); it never persists them. The control plane
// (admin) is the self-hosted backend: it receives OTLP/HTTP, writes parquet,
// and serves /logs + /stats queries. parquet sinks are therefore instantiated
// only in the admin process.
package observability
