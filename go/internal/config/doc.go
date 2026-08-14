// Package config loads the standalone YAML configuration and seeds it into a
// storage backend. Used by `nyro proxy --config` to run without an
// admin/DB.
//
// The YAML shape mirrors the config-schema plan's final config.yaml: version +
// settings (server/proxy/state/telemetry) + upstreams + routes + consumers
// (nested keys/routes/quotas).
//
// Layer: 1 (data) — may import internal/config/snapshot,
// internal/protocol/llm, layer 0, and storage.
//
// It imports platform/state and telemetry/schema (layer 0) to validate the
// settings.state and settings.telemetry YAML blocks. It does not import either
// runtime.
//
// Must not import layer 3 (proxy, router, admin, dataplane, bootstrap).
package config
