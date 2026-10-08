# Rust data plane

[中文](rust-proxy_CN.md)

Build the root binary with `cargo build -p nyro`. This source-built entry is separate from the released desktop and `nyro-server` applications.

```sh
export UPSTREAM_MODEL=your-backend-model
export UPSTREAM_TOKEN=your-upstream-secret
export NYRO_CLIENT_TOKEN=your-client-secret
cargo run -p nyro -- proxy --config docs/standalone/rust-proxy.yaml
```

The [example](rust-proxy.yaml) defines four runtime resource collections: `upstreams`, `models`, `mcps`, and `consumers`, plus `version: 1`. Omitted collections are empty. Startup settings use CLI/environment: `--listen` (`NYRO_LISTEN`, default `127.0.0.1:19530`), `--concurrency` (`NYRO_CONCURRENCY`, default 64), and `--config` (`NYRO_CONFIG`). The file is read once. There is no file watcher or SIGHUP reload. Restart to apply file changes.

Every scalar value can contain `${VARIABLE}`. Missing variables are errors; `$${VARIABLE}` produces a literal `${VARIABLE}`. Expansion happens within parsed scalar values, so an environment value cannot inject YAML objects or lists. Values are not recursively expanded. Numeric settings accept numeric environment strings. Configuration and sync payloads are bounded to 1 MiB.

## Resources

A model's `id` is the client-visible model name. Its `name` is an optional display label. `capability` is `chat` or `embedding`; `upstream` references an LLM pool. Each target has its own backend `model`, `base_url`, `protocol`, optional `auth`, `weight` (default 1), and `priority` (default 0, lower wins). LLM base URLs are API roots; adapters append paths. Supported protocols:

- `openai/chat-completions`
- `openai/responses`
- `openai/embeddings`
- `anthropic/messages`
- `gemini/generate-content`

Matching native Chat APIs preserve supported wire fields; cross-protocol requests use typed conversion and reject representations that cannot be preserved. See the existing protocol tests for operation-specific boundaries. Gemini Interactions, image/audio/video generation and OAuth are not added by this resource migration.

Pool `balance` defaults to **`weighted-roundrobin`**, a smooth weighted round-robin algorithm. LLM pools also accept `weighted-random`, `least-recent`, and `latency-aware`. Aliases referencing the same pool share selection history. Weight zero disables selection. LLM `execution.max_attempts` includes the first attempt; replay boundaries remain governed by the protocol runtime.

MCP resources use the same upstream and consumer concepts. See [MCP](rust-mcp.md).

## Authentication and authorization

`access.mode` defaults to `restricted`:

| Mode | Behavior |
|---|---|
| `anonymous` | Ignores incoming credentials, including incorrect credentials. Consumer limits do not apply. |
| `authenticated` | Requires a valid consumer credential. |
| `restricted` | Requires a valid credential and a matching consumer grant. |

Consumers can have multiple `{id, type: key-auth, secret}` credentials; rotation shares the same consumer identity and limits. `grants.models` and `grants.mcps` reference public resource IDs. An empty grant list authorizes no restricted resources. There are no `enabled` flags or `api-key`/`apikey` authentication aliases.

Outgoing authentication is independent:

```yaml
auth:
  type: key-auth
  in: header
  name: Authorization
  prefix: Bearer
  secret: ${UPSTREAM_TOKEN}
```

`prefix` is optional; a nonempty prefix is followed by exactly one space. For query credentials use `in: query`, a parameter `name`, and `secret`; omit `prefix`. The adapter encodes the value and replaces existing parameters with the same name. Caller credentials and arbitrary incoming headers are not forwarded upstream.

## Execution and limits

`execution.request_timeout` is a positive number of seconds, including fractions such as `1.5`. Defaults are 120 seconds for LLM and 30 for MCP. The model is resolved from the parsed request; uploads before that point use the application-wide body/deadline envelope. Once resolved, the resource deadline is measured from request arrival, and its body bound is checked before dispatch. Optional positive `max_body_bytes`, `max_response_bytes`, and `max_frame_bytes` default to 1 MiB, 16 MiB, and 1 MiB respectively.

`limits.request` and LLM `limits.token` are lists of `{limit, window}`. Windows accept `ms`, `s`, `m`, `h`, `d`. Resource limits and `consumers[].limits.llm` / `.mcp` all apply together. MCP supports request limits only.

Requests count once per admitted logical request, including retries and subsequent failures. Tokens count actual known usage for every upstream attempt; there is no reservation or estimate. Repeated cumulative usage is charged once. Missing usage charges zero; interrupted streams charge the last valid known usage and report it as incomplete. In-flight work can exceed token limits.

Counters are node-local memory: they are not shared across replicas or persisted across restarts. Control-plane renames and key rotation preserve identity and history. Changing a threshold for the same window preserves history; a new window starts counting from activation. Monetary quotas, resource-level rate buckets and concurrency policies are not supported. The process-wide concurrency guard remains.

## Remote configuration

```sh
nyro proxy --server https://control.example.com \
  --sync-token-file /run/secrets/nyro-sync --node-id edge-1
```

Use `--config` or `--server`, never both. Remote proxies do not open a database. `NYRO_SERVER`, `NYRO_SYNC_TOKEN_FILE`, and `NYRO_NODE_ID` are equivalent environment settings. Sync uses authenticated full-snapshot HTTP long polling. Remote URLs require HTTPS; HTTP is allowed only for IP loopback tests.

`/healthz` reports process availability. `/readyz` stays unavailable until the first valid snapshot activates. A valid empty snapshot is ready but has no business routes. Disconnection retains the last active in-memory generation; invalid updates leave it running. There is no offline disk cache. See [control-plane deployment](rust-serve.md).
