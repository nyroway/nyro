# nyro-mcp

Authenticated MCP 2026-07-28 tools over Streamable HTTP. Owns MCP configuration,
protocol adaptation, tool allowlists and upstream calls. Depends on the shared
security/concurrency primitives, not the LLM application, kernel or database.

`Runtime::new` validates and allocates local HTTP clients without contacting
upstreams. `Runtime::handle` takes an HTTP request and cancellation token; its
response owns the concurrency permit and cancellation/deadline cleanup. A call
uses request-local SDK services, closes the upstream service after completion,
and never retries the tool. The SDK owns its stateless server tasks and cancels
them when their response closes. Hosts must retain their configuration generation
until the response body completes or is dropped.

The SDK can log protocol payloads. Exclude `rmcp` and `rmcp::*` targets with an
independent tracing filter, as the Nyro binary does. An EnvFilter directive alone
can be overridden by more specific user directives. Keep this filtering when
embedding the crate in another server. See the [English guide](../../docs/standalone/rust-mcp.md) or
[中文指南](../../docs/standalone/rust-mcp_CN.md) for configuration and limitations.
