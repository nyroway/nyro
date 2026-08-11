// Package snapshot owns the data plane's immutable configuration read model,
// its builder, and the atomic cache used to publish complete replacements.
// Configuration sources such as standalone YAML and config-sync project into
// this package; request-serving code reads it without depending on a source or
// transport.
//
// Layer: 1 (data) — may import the Go standard library and internal/storage.
// It must not import config-sync transports, telemetry runtime, or layer 3.
package snapshot
