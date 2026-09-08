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

The current ingress implements typed subsets of OpenAI Chat/Embedding, stateless Responses, Anthropic Messages, and Gemini generateContent. Chat supports cross-protocol text, function calls/results and SSE across the four supported Chat API formats. It is not a promise of full vendor API compatibility. Unknown or unsupported request fields are rejected with `400` instead of being forwarded. Unsupported upstream response semantics produce `502` before streaming begins, or terminate an SSE stream if they arrive after its validated first frame. Model names are public aliases: Nyro replaces them with the selected backend's `upstream_model` on the upstream request and restores the public name in supported responses.

## Configuration

All structures reject unknown fields. `kind` is required and accepts `openai`, `anthropic`, or `gemini`. OpenAI providers optionally select `api: chat_completions` (default) or `api: responses`; other kinds reject the `api` field. Provider URLs must use HTTP or HTTPS, have a host, and contain no user information, query, or fragment. Nyro appends the native endpoint to the configured base path. Upstream requests use only the provider's optional `api_key`; caller credentials are not forwarded. Redirects, environment-configured HTTP proxies, and Reqwest automatic protocol retries are disabled for upstream calls; the LLM runtime owns the retry budget.

| Provider kind | Example base URL | Appended endpoint | Upstream credential |
|---|---|---|---|
| `openai`, default API | `https://api.example.com/v1` | `chat/completions` or `embeddings` | Bearer Authorization |
| `openai`, `api: responses` | `https://api.example.com/v1` | `responses` | Bearer Authorization |
| `anthropic` | `https://api.anthropic.com/v1` | `messages` | `x-api-key`; fixed `anthropic-version: 2023-06-01` |
| `gemini` | `https://generativelanguage.googleapis.com/v1beta` | `models/{upstream_model}:generateContent` or `:streamGenerateContent?alt=sse` | `x-goog-api-key` |

Gemini upstream model names accept a single ASCII letter/digit/`-_.` segment, optionally prefixed with `models/`; `.` and `..` path segments are rejected.

Each model declares:

- `backends`: a nonempty list of upstream backends. Each has a model-scoped, unique, nonblank `id`, a `provider` ID from `llm.providers`, an `upstream_model`, and an optional integer `weight` (default `100`), and `priority` (default `0`, smaller values preferred).
- `max_attempts`: positive integer, default `1`, including the first upstream send. Each backend ID is attempted at most once per request.
- `health`: optional passive breaker policy, disabled when omitted. An empty object enables `failure_threshold: 3` and `cooldown_ms: 30000`; both must be positive.
- `workloads`: a nonempty, duplicate-free list containing `chat`, `embedding`, or both when every backend uses OpenAI Chat Completions; Responses, Anthropic, and Gemini support only `chat`. Every backend, including disabled entries, must support the model's declared workloads. Invalid combinations fail startup.
- `allow_anonymous`: optional, default `false`.
- `subjects`: client credential IDs allowed to invoke a protected model; optional only for anonymous models.

Backend weights range from `0` to `4294967295`. Zero disables selection; a model with no positive weight fails startup. Backend IDs are independent of provider/model names and unique within their public model. Changing an ID changes the effective configuration identity. Backend list order does not express priority and does not change the configuration fingerprint.

Legacy models using top-level `provider` and `upstream_model` remain accepted. They normalize in memory to one backend with `id: default` and `weight: 100`; an equivalent explicit backend has the same fingerprint. Mixing the legacy fields with `backends`, incomplete legacy pairs, explicit null routing fields, or unknown fields is rejected. Config serialization emits the normalized `backends` form; source files are not rewritten.

```yaml
chat-default:
  backends:
    - id: primary
      provider: example
      upstream_model: example-chat-model
      weight: 80
    - id: secondary
      provider: responses-example
      upstream_model: example-responses-model
      weight: 20
  workloads: [chat]
  subjects: [local-client]
```

Authentication, authorization, and shared admission apply once to the public model. Nyro then prepares each enabled backend locally with its own protocol codec, excludes backends that cannot represent the request, and selects from the lowest available `priority`, randomly in proportion to weight within that priority. The weights apply to the eligible subset; they are not per-batch traffic guarantees. Preparation sends no network requests. If none can represent the request, the response is `400` and no upstream is called. For example, without a token limit an Anthropic backend is ineligible while a compatible OpenAI backend can still be selected. Preparation cannot determine actual provider availability or whether its eventual response is convertible.

By default, each invocation makes one upstream attempt. Setting `max_attempts` above `1` enables failover to another eligible backend after a connection-establishment failure or an upstream HTTP `429`, `500`, `502`, `503`, `504`, or `529`. Remaining backends at the same priority are tried before larger priorities. Skipped incompatible, disabled, or unhealthy backends do not consume the budget. There is no backoff or separate attempt timeout: all attempts share one whole-request deadline and concurrency permit. A slow attempt can exhaust that deadline before failover is possible. Cancellation or dropping the request/body stops owned work.

Other HTTP statuses and ambiguous transport failures do not trigger failover. Once an upstream returns `2xx`, the backend is fixed: a malformed JSON response, wrong content type, invalid first SSE frame, or later broken stream fails without another attempt. Failed-attempt headers and bodies are discarded. Opting into retries can still result in work at multiple upstreams; it does not guarantee exactly-once provider execution. Request logs include the public model, last selected backend ID, and number of attempts.

For example, this model prefers `primary` and can fall back to `secondary`:

```yaml
chat-failover:
  backends:
    - {id: primary, provider: example, upstream_model: example-chat-model, priority: 0}
    - {id: secondary, provider: responses-example, upstream_model: example-responses-model, priority: 1}
  max_attempts: 2
  health: {failure_threshold: 3, cooldown_ms: 30000}
  workloads: [chat]
  subjects: [local-client]
```

With `health` enabled, observed network errors, the transient statuses above, and invalid upstream responses count toward the failure threshold. A fully validated response resets the counter; for SSE this requires protocol completion, not headers or the first frame. Other statuses, client cancellation, body drop, and the overall deadline are neutral. After the threshold is reached, the backend is skipped for the cooldown. The first eligible request afterward claims a single recovery probe; concurrent requests use other available backends or receive `503`. A successful probe restores service; a failed probe starts another cooldown. A cancelled/dropped probe releases its claim without declaring recovery. If all compatible backends are blocked before an attempt, the response is `503`; after an attempted failure, the last sanitized upstream error is returned.

Health is scoped to a public model and backend identity. The root composition shares it across generations for unchanged provider URL/API/credentials, upstream model, and health policy; weight or priority changes retain it. A changed binding gets fresh health state. Retired bindings are released after their generations and requests drop. There is no background probe or persistence across process restarts.

Local routing regressions: `cargo test -p nyro-llm --test routing_runtime --test failover_runtime`.

For a protected model, `subjects` must be nonempty and every value must match an `id` in `security.api_keys`. A request without a supported header credential, or with an unknown credential, receives `401`; a known subject not granted that model receives `403`. For an anonymous model, a missing credential is accepted. If a credential is supplied, it must still authenticate, but any known credential may use the anonymous model.

Credential IDs and secrets must be unique and nonempty; IDs cannot be blank. Client secrets must contain only visible ASCII without whitespace so they can be presented as HTTP Bearer credentials. Configuration errors and debug output redact secrets.

| Setting | Default | Effect |
|---|---:|---|
| `server.listen` | `127.0.0.1:19530` | Listener address |
| `server.request_timeout_ms` | `120000` | Whole-request deadline, including a successful response body or SSE stream |
| `server.max_body_bytes` | `1048576` | Maximum buffered downstream request body |
| `server.max_response_bytes` | `16777216` | Maximum buffered non-stream upstream response; it is not a cumulative SSE limit |
| `server.max_frame_bytes` | `1048576` | Maximum upstream SSE frame, emitted conversion batch, and accumulated tool/Responses snapshot state bytes |
| `limit.concurrency` | `64` | Shared in-flight request cap; excess requests receive `429` |

All numeric limits must be greater than zero. Concurrency must also fit Tokio's supported semaphore capacity. A concurrency permit remains held until the response body completes or is dropped. SSE has a per-frame cap and the whole-request deadline, but no cumulative wire byte cap. Responses additionally retains the full output snapshot within `max_frame_bytes`, including generated text; large Responses streams can therefore hit this limit even with small deltas. Tool fragments may be buffered until complete; accumulation is bounded by `max_frame_bytes`. A protocol terminal must be validated before a successful terminal is emitted; malformed/truncated streams fail without retry.

## Native clients and conversion limits

| Client API | Endpoint | Credential |
|---|---|---|
| OpenAI | `POST /v1/chat/completions`, `POST /v1/responses`, `POST /v1/embeddings` | `Authorization: Bearer …` |
| Anthropic | `POST /v1/messages` | `x-api-key: …` or Bearer |
| Gemini | `POST /v1beta/models/{alias}:generateContent`, `:streamGenerateContent?alt=sse` | `x-goog-api-key: …` or Bearer |

Gemini also accepts `/v1/models/…`. Only one credential header may be supplied; duplicate or conflicting sources return `401`. Query-string credentials are rejected, and the only supported query option is `alt=sse` on Gemini streaming requests. Gemini aliases must use a single ASCII letter/digit/`-_.` segment.

```sh
curl http://127.0.0.1:19530/v1/messages \
  -H 'x-api-key: replace-with-client-secret' \
  -H 'Content-Type: application/json' \
  -d '{"model":"claude-default","max_tokens":256,"messages":[{"role":"user","content":"Hello"}]}'

curl http://127.0.0.1:19530/v1beta/models/gemini-default:generateContent \
  -H 'x-goog-api-key: replace-with-client-secret' \
  -H 'Content-Type: application/json' \
  -d '{"contents":[{"role":"user","parts":[{"text":"Hello"}]}],"generationConfig":{"maxOutputTokens":256}}'
```

An alias selects the configured upstream independently of the client protocol. An Anthropic backend is eligible only with an explicit token limit (`max_tokens`, or a representable equivalent); Nyro does not invent one. Native clients routed to an OpenAI stream request upstream usage automatically. OpenAI Chat Completions clients receive usage only when `stream_options.include_usage` is true.

This is an experimental text/function Chat subset. New native codecs reject image/audio/video, thinking/signature blocks, unrepresentable content ordering, multiple candidates, and unsupported vendor options or diagnostics. Examples include Anthropic caching/usage details and matched stop-sequence responses; Gemini safety settings, safety ratings, grounding/citations, prompt feedback and structured-output settings; and OpenAI response fingerprint/service-tier/logprob metadata when it cannot be represented by a native output. Supported fields in one protocol are not automatically representable in another: unsupported requests fail before dispatch; unsupported upstream responses fail with `502` or a terminated SSE stream. Native Embedding APIs and full SDK/vendor feature parity remain future work.

Protocol reference: [Anthropic streaming](https://platform.claude.com/docs/en/build-with-claude/streaming), [Gemini generateContent](https://ai.google.dev/api/generate-content). Local matrix regression: `cargo test -p nyro-llm --test protocol_matrix`.

## Responses subset

`POST /v1/responses` accepts string input or typed message/function items, instructions, common generation controls, and client function tools/results. Native Responses function definitions must explicitly set `strict`: use `false` for the portable non-strict subset; `true` requires an upstream that can preserve it. It can use any configured Chat upstream. Conversely, all supported Chat ingress APIs can use a provider with `api: responses`. For example:

```sh
curl http://127.0.0.1:19530/v1/responses \
  -H 'Authorization: Bearer replace-with-client-secret' \
  -H 'Content-Type: application/json' \
  -d '{"model":"responses-default","input":"Hello","max_output_tokens":256,"store":false,"stream":true}'
```

Function results currently require a string `function_call_output.output`; array results are rejected.

This implementation is stateless: outbound Responses always sets `store:false`, and Responses ingress translated to Chat Completions also explicitly disables storage. Server conversation state (`conversation`, `previous_response_id`), item references, `store:true`, `background:true`, hosted tools, reasoning items, media, and response retrieval/deletion/cancellation are unsupported. Meaningful options or output items that the current Chat IR cannot preserve are rejected. Responses envelope echoes and item IDs are normalized; exact upstream IDs and request echoes are not preserved. Function `call_id`, content order, finish status, and representable usage remain part of the conversion contract.

SSE uses named Responses lifecycle events, stable item IDs, ordered sequence numbers, and a full terminal snapshot, without a `[DONE]` marker. Token-limit/content-filter endings produce `response.incomplete`. Failed, contradictory, malformed, or truncated upstream streams terminate without a fabricated successful completion. Native upstream streaming disables obfuscation. This subset does not establish full Responses SDK or Codex CLI compatibility.

References: [OpenAI Responses migration](https://developers.openai.com/api/docs/guides/migrate-to-responses), [Responses streaming](https://developers.openai.com/api/docs/guides/streaming-responses). Local regressions: `cargo test -p nyro-llm --test responses_codec --test responses_runtime`.

## Health and current scope

- `GET /healthz` returns `200` while the HTTP process is serving.
- `GET /readyz` returns `200` while the kernel host accepts new generation leases and `503` once it does not. This source-built proxy has no database or upstream-backend readiness check.

Retries and passive health are opt-in as described above. It does not implement rate limits, quotas, a control plane, Admin API, WebUI, `nyro serve`, or `nyro tool`. It does not expand environment variables, accept the legacy standalone YAML format, watch or hot-reload the file, or fetch configuration remotely. Restart the process to apply edits.

Contributors can exercise the root process, probes, authentication, Chat, Embedding, SSE, redaction, and graceful termination without a real provider:

```sh
cargo build -p nyro
python3 tests/proxy_smoke.py
```
