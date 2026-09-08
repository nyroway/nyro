# Experimental Rust file-config proxy

[中文](rust-proxy_CN.md)

The root `nyro` binary includes an experimental, source-built LLM data plane. It is separate from the released `nyro-server` standalone mode documented in [README.md](README.md), and its YAML format is not compatible with that legacy entry.

## Run from source

Copy [rust-proxy.yaml](rust-proxy.yaml), replace the example provider URL, model names, and secrets, then run:

```sh
cargo run -p nyro -- proxy --config docs/standalone/rust-proxy.yaml
```

The file is read and validated once before the listener binds. The default address is `127.0.0.1:19530`.

Send a protected Chat Completions request with the client credential configured under `security.api_keys`:

```sh
curl http://127.0.0.1:19530/v1/chat/completions \
  -H 'Authorization: Bearer replace-with-client-secret' \
  -H 'Content-Type: application/json' \
  -d '{"model":"chat-default","messages":[{"role":"user","content":"Hello"}]}'
```

The current ingress implements a typed subset of `POST /v1/chat/completions`, including OpenAI-compatible SSE when `stream: true`, and `POST /v1/embeddings`. It is not a promise of full vendor API compatibility. Unknown or unsupported request fields are rejected with `400` instead of being forwarded. Unknown or unsupported upstream response fields produce `502` before streaming begins, or terminate an SSE stream if they arrive after its validated first frame. Model names are public aliases: Nyro replaces them with `upstream_model` on the upstream request and restores the public name in supported responses.

## Configuration

All structures reject unknown fields. `kind` is required and currently accepts only `openai`. Provider URLs must use HTTP or HTTPS, have a host, and contain no user information, query, or fragment. Nyro appends `chat/completions` or `embeddings` to the configured base path. Upstream requests use only the provider's optional `api_key`; caller authorization headers are not forwarded. Redirects and environment-configured HTTP proxies are disabled for upstream calls.

Each model declares:

- `provider`: an ID from `llm.providers`.
- `upstream_model`: the model name sent upstream.
- `workloads`: a nonempty, duplicate-free list containing `chat`, `embedding`, or both.
- `allow_anonymous`: optional, default `false`.
- `subjects`: client credential IDs allowed to invoke a protected model; optional only for anonymous models.

For a protected model, `subjects` must be nonempty and every value must match an `id` in `security.api_keys`. A request without a Bearer credential, or with an unknown credential, receives `401`; a known subject not granted that model receives `403`. For an anonymous model, a missing credential is accepted. If a credential is supplied, it must still authenticate, but any known credential may use the anonymous model.

Credential IDs and secrets must be unique and nonempty; IDs cannot be blank. Client secrets must contain only visible ASCII without whitespace so they can be presented as HTTP Bearer credentials. Configuration errors and debug output redact secrets.

| Setting | Default | Effect |
|---|---:|---|
| `server.listen` | `127.0.0.1:19530` | Listener address |
| `server.request_timeout_ms` | `120000` | Whole-request deadline, including a successful response body or SSE stream |
| `server.max_body_bytes` | `1048576` | Maximum buffered downstream request body |
| `server.max_response_bytes` | `16777216` | Maximum buffered non-stream upstream response; it is not a cumulative SSE limit |
| `server.max_frame_bytes` | `1048576` | Maximum upstream SSE frame bytes, including framing |
| `limit.concurrency` | `64` | Shared in-flight request cap; excess requests receive `429` |

All numeric limits must be greater than zero. Concurrency must also fit Tokio's supported semaphore capacity. A concurrency permit remains held until the response body completes or is dropped. SSE has a per-frame cap and the whole-request deadline, but no cumulative stream byte cap.

## Health and current scope

- `GET /healthz` returns `200` while the HTTP process is serving.
- `GET /readyz` returns `200` while the kernel host accepts new generation leases and `503` once it does not. This source-built proxy has no database readiness check.

This slice has one OpenAI-compatible upstream attempt. It does not implement retries, failover, rate limits, quotas, a control plane, Admin API, WebUI, `nyro serve`, or `nyro tool`. It does not expand environment variables, accept the legacy standalone YAML format, watch or hot-reload the file, or fetch configuration remotely. Restart the process to apply edits.

Contributors can exercise the root process, probes, authentication, Chat, Embedding, SSE, redaction, and graceful termination without a real provider:

```sh
cargo build -p nyro
python3 tests/proxy_smoke.py
```
