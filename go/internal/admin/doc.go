// Package admin mounts the management REST API (under /api/v1) consumed by
// the React WebUI and the CLI. Handlers are thin wrappers over
// storage.Storage (config-schema: upstreams/routes/consumers/settings), plus
// the parquet-backed observability read paths (/logs, /stats/*).
//
// Layer: 3 (serve) — the control-plane HTTP surface; may import any lower
// layer. Nothing below layer 3 may import it.
package admin
