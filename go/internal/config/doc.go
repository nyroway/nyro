// Package config loads the standalone YAML configuration and seeds it into a
// storage backend. Used by `nyro gateway --config` to run without an
// admin/DB.
//
// The YAML shape mirrors the config-schema plan's final config.yaml: version +
// settings (server/proxy/observability) + upstreams + routes + consumers
// (nested keys/routes/quotas).
package config
