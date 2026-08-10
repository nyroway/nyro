// Package config loads the standalone YAML configuration and seeds it into a
// storage backend. Used by `nyro proxy --config` to run without an
// admin/DB.
//
// The YAML shape mirrors the config-schema plan's final config.yaml: version +
// settings (server/proxy/observability) + upstreams + routes + consumers
// (nested keys/routes/quotas).
//
// Layer: 1 (data) — may import internal/protocol/llm, layer 0, storage, and
// configsync.
//
// It also imports observability (layer 2), but only for the exporter-registry
// contract (Signal, ExporterDef, ExportersFor) used to validate the
// settings.observability.* YAML block — not for instrumentation. That
// contract is really layer-0 metadata living inside a layer-2 package; see
// the layering test's KnownUpwardEdges for the tracking note.
//
// Must not import layer 3 (proxy, router, admin, dataplane, bootstrap).
package config
