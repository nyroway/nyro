// Package admin mounts the management REST API (under /api/v1) consumed by
// the React WebUI and the CLI. Handlers are thin wrappers over
// storage.Storage (config-schema: upstreams/routes/consumers/settings), plus
// the parquet-backed observability read paths (/logs, /stats/*) and the
// loaded-extensions view.
package admin
