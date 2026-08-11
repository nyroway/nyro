// Package config loads the standalone YAML configuration and seeds it into a
// storage backend. Used by `nyro proxy --config` to run without an
// admin/DB.
//
// The YAML shape mirrors the config-schema plan's final config.yaml: version +
// settings (server/proxy/observability) + upstreams + routes + consumers
// (nested keys/routes/quotas).
//
// Layer: 1 (data) — may import internal/config/snapshot,
// internal/protocol/llm, layer 0, and storage.
//
// It imports telemetry/schema (layer 0) for the exporter registry used to
// validate the settings.observability.* YAML block. It does not import the
// telemetry runtime.
//
// Must not import layer 3 (proxy, router, admin, dataplane, bootstrap).
package config
