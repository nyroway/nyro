# Rust MCP gateway

[中文](rust-mcp_CN.md)

LLM and MCP are composed by the same root `nyro` host. Use the [MCP YAML](rust-mcp.yaml) with `nyro proxy --config`, or save the same resource objects through the [control API](rust-serve.md).

An MCP resource `id: tools` exposes **`/mcp/tools`**. IDs are 1–64 ASCII letters, digits, hyphens or underscores. There is no configurable endpoint path. `upstream` references a `kind: mcp` pool; target `url` is the full MCP endpoint and `transport` is `streamable-http`. Pools support `weighted-roundrobin` and `weighted-random`. Selection is shared by MCP resources referencing the same pool.

Consumers, `key-auth`, access modes and rolling request limits follow the [shared resource contract](rust-proxy.md). Each resource lists nonempty, exact `allowed_tools`; wildcards are rejected. Consumer grants reference MCP IDs. A resource's limits and Consumer `limits.mcp` both apply. There is no token limit for MCP.

The current gateway uses the pinned SDK's stateless MCP protocol revision `2026-07-28` and supports `server/discover`, `tools/list`, and `tools/call`. Both headers and JSON-RPC metadata must meet that SDK contract. It is not a legacy session proxy: session IDs, resume IDs, browser origins, tasks and continuation responses are rejected. There is no tool aggregation across targets.

The selected target is fixed for the operation. Upstream credentials are applied separately from caller credentials. A tool call is never automatically replayed or failed over; even a connection/status failure does not justify replaying a side effect. Responses and SSE frames are bounded, cancellation closes upstream work, and the generation lease spans response delivery.

Execution defaults: 30-second request timeout, 1 MiB body, 16 MiB response and 1 MiB frame. Override these under resource `execution` using numeric seconds for `request_timeout`; `max_attempts` is not accepted for MCP.
