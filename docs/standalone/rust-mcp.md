# Experimental Rust MCP tools gateway

[中文](rust-mcp_CN.md)

The source-built root `nyro` binary can serve LLM and MCP requests on the same listener. Copy [rust-mcp.yaml](rust-mcp.yaml), replace the example endpoints and credentials, and run:

```sh
cargo run -p nyro -- proxy --config docs/standalone/rust-mcp.yaml
```

Both LLM and MCP runtimes load at startup. Either configuration section can be omitted: its application starts with no resources. Configure LLM only, MCP only, both, or neither. To use only MCP, remove `llm` from the example and retain `mcp` and its referenced `security.api_keys`. Existing LLM-only files remain valid.

A configuration file containing `{}` starts an empty gateway on the default listener. You can also set the listener explicitly:

```yaml
server:
  listen: 127.0.0.1:19530
```

Valid requests for unconfigured model aliases or MCP services return 404; `/v1/models` returns an empty list subject to its normal authentication rules. `/readyz` remains ready. Add or remove resources through SIGHUP reload or control-plane publication without restarting. `nyro serve` accepts this empty configuration as its initial seed; subsequent changes use its [draft/save/publish API](rust-serve.md). Initial database creation still requires `--config PATH`; an existing database reopens without it.

## Supported protocol

Both hops use MCP **2026-07-28**, pinned to the official Rust SDK `rmcp 3.4.1`. Each configured server has its own endpoint, `/mcp/{server_id}`. Supported methods are `server/discover`, `tools/list` and `tools/call`. Discovery advertises only tools. JSON responses and request-scoped SSE, including progress notifications, are supported. Tool schemas, descriptions, annotations, structured content and `isError` remain MCP values; they do not pass through LLM IR.

This release does not support legacy `initialize` sessions, GET streams, DELETE, stream resumption, stdio, OAuth, resources, prompts, subscriptions, tasks, MRTR, automatic LLM tool execution or an aggregated tool namespace. Unsupported continuation results fail explicitly. Clients must use POST with a Host header (or HTTP/2 authority), both JSON and SSE in `Accept`, and consistent protocol metadata. Requests containing `Origin` are rejected with 403; browser access is not enabled.

For example, list the tools visible to `client-a`:

```sh
curl http://127.0.0.1:19530/mcp/knowledge \
  -H 'Authorization: Bearer replace-with-client-secret' \
  -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: tools/list' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"example-client","version":"1"},"io.modelcontextprotocol/clientCapabilities":{}}}}'
```

For `tools/call`, use the same metadata, set `Mcp-Method: tools/call`, add `Mcp-Name` matching `params.name`, and supply the tool's `arguments`. Tools with `x-mcp-header` schema annotations require corresponding parameter headers as defined by the protocol. Use an SDK that supports this revision.

## Configuration and authorization

`llm.providers`, `llm.models` and `mcp.servers` default to empty maps. Empty maps are valid; all declared resources and request limits are still validated. A model must reference an existing provider, and a server must reference existing subjects. Server IDs contain 1–64 ASCII letters, digits, `-` or `_`. `transport: http` is required. `url` is the exact HTTP(S) endpoint, with no user information, query, fragment or whitespace; Nyro does not append a path. Local and private endpoints are allowed. Unknown fields and explicit null objects are rejected.

Each server requires nonempty `subjects` and `allowed_tools` arrays, without duplicates or wildcards. Subjects reference `security.api_keys[].id`. Disabled or expired keys remain valid configuration references but cannot authenticate. Every operation requires a unique Bearer Authorization header and service access. Query credentials are rejected. Both listing and direct calls enforce the exact tool allowlist.

A list request reads one upstream page, filters tools, and preserves `nextCursor`, even when the filtered page is empty. A call first discovers the upstream and reads schema pages until it finds the requested tool, so the SDK can construct required parameter headers. These reads share the call's deadline and response budget; repeated cursors fail. The tool itself is called at most once.

The optional upstream `bearer_token` is distinct from the incoming API key. Nyro never passes through the caller's Authorization header, takes no caller-supplied target URL, ignores system proxy settings, and follows no redirects. Ordinary admin queries replace the token with `has_bearer_token`; protected full exports retain credentials for re-import. Deleting an API key referenced by either LLM or MCP is rejected.

## Limits, reload and failure behavior

The example shows the default MCP limits. They are independent of the LLM `server` request limits. The MCP deadline includes upload, discovery, schema lookup, tool execution and response delivery. The response budget bounds raw bytes across upstream discovery and operation requests, and the frame limit applies before SSE parsing. MCP and LLM share `limit.concurrency`; MCP does not consume LLM RPM/TPM/token-quota counters.

No tool retry, version fallback, session recovery or failover is performed. Closing a request or reaching its deadline stops local waiting and closes associated streams. An upstream tool may already have performed a side effect; cancellation cannot guarantee rollback.

Candidate construction validates locally without connecting upstream. An unavailable MCP server does not prevent LLM startup. Unix SIGHUP reload and control-plane publication replace both applications as one generation. New requests see the new authorization snapshot; admitted requests retain their original configuration until completion or their deadline. Invalid candidates retain the active generation. `/readyz` reports admission readiness, not upstream reachability.

Nyro emits MCP request status and timing without tool arguments or results. The root binary disables the SDK's wire-payload logging even if `RUST_LOG` enables `rmcp`; projects embedding `nyro-mcp` should also exclude `rmcp` targets using an independent tracing filter, rather than relying only on an overridable EnvFilter directive.

Protocol reference: [MCP Streamable HTTP 2026-07-28](https://modelcontextprotocol.io/specification/2026-07-28/basic/transports/streamable-http). Local acceptance: `cargo test -p nyro-mcp`, then `cargo build -p nyro && python3 tests/mcp_gateway_smoke.py`.
