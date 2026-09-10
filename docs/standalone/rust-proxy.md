# Experimental Rust file-config proxy

[中文](rust-proxy_CN.md)

The root `nyro` binary includes an experimental, source-built LLM data plane. It is separate from the released `nyro-server` standalone mode documented in [README.md](README.md), and its YAML format is not compatible with that legacy entry.

## Run from source

Copy [rust-proxy.yaml](rust-proxy.yaml), replace the example provider URL, model names, and secrets, then run:

```sh
cargo run -p nyro -- proxy --config docs/standalone/rust-proxy.yaml
```

The file is read and validated before the listener binds. The default address is `127.0.0.1:19530`. On Unix, `SIGHUP` reloads the same file as described below.

Send a protected Chat Completions request with the client credential configured under `security.api_keys`:

```sh
curl http://127.0.0.1:19530/v1/chat/completions \
  -H 'Authorization: Bearer replace-with-client-secret' \
  -H 'Content-Type: application/json' \
  -d '{"model":"chat-default","messages":[{"role":"user","content":"Hello"}]}'
```

The current ingress implements typed subsets of OpenAI Chat/Embedding, stateless Responses, Anthropic Messages, and Gemini generateContent. Chat supports cross-protocol text, function calls/results and SSE across the four supported Chat API formats. It is not a promise of full vendor API compatibility. By default, unknown or unsupported request fields are rejected with `400` instead of being forwarded. Unsupported upstream response semantics produce `502` before streaming begins, or terminate an SSE stream if they arrive after its validated first frame. The opt-in OpenAI Chat/Responses, Anthropic Messages and Gemini generateContent native modes below preserve vendor JSON fields on matching endpoints. Model names are public aliases: Nyro replaces them with the selected backend's `upstream_model` on the upstream request and restores the public name in typed responses and OpenAI/Anthropic native responses. Gemini native responses preserve `modelVersion` as upstream version metadata.

## List available models

`GET /v1/models` lists the configured public model aliases visible to the caller, in ascending alias order:

```sh
curl http://127.0.0.1:19530/v1/models \
  -H 'Authorization: Bearer replace-with-client-secret'
```

The response has the OpenAI list shape: `{"object":"list","data":[{"id":"public-alias","object":"model","created":0,"owned_by":"Nyro"}]}`. `created: 0` is a fixed placeholder, not a provider timestamp. Without credentials, only models with `allow_anonymous: true` are listed. A valid Bearer key also sees models granting its subject access; a caller with no visible models receives `200` with an empty `data` array. Invalid, duplicate or conflicting credentials return `401`, even when public models exist. This OpenAI-format endpoint accepts Bearer credentials only, not `x-api-key` or `x-goog-api-key`. Query credentials are rejected; pagination and other query parameters are unsupported. Only `GET` is supported.

The catalog comes from one active runtime generation and contains no upstream model names, provider addresses or secrets. Successful reloads update the list and credentials; failed reloads retain the old catalog. Listing does not contact providers or consume inference concurrency, rate or quota. Models remain discoverable when their inference budgets are exhausted or their backends are unhealthy: visibility is an authorization decision, not an availability promise. Responses use `Cache-Control: no-store`; normal request IDs, deadlines and response-body cleanup still apply. Model discovery emits a request observation with `protocol=openai_models`, `workload=none`, zero attempts and `usage_state=not_attempted`.

Regression: `cargo test -p nyro-llm --test models_runtime` and, after building the root binary, `python3 tests/proxy_reload_smoke.py`.

## Reload the file

On Unix, send `SIGHUP` to the running **nyro process** after saving a complete configuration. For example, with a source-built binary:

```sh
target/debug/nyro proxy --config docs/standalone/rust-proxy.yaml &
nyro_pid=$!
# Save the updated configuration, preferably by atomically replacing the file.
kill -HUP "$nyro_pid"
```

The signal handler is registered before readiness. Each reload reads the original `--config` path again, including files replaced by rename. Reload accepts regular files (including symlinks to regular files); directories and special files such as FIFOs are rejected. Reloads are serial; signals may coalesce, so they are not a queue of configuration versions. Nyro does not watch files automatically. On Windows, restart to load edits.

A reload validates the whole file, checks settings that require restart, then compares the effective configuration fingerprint. Equivalent configurations skip candidate construction and keep the same generation. Valid changes build a candidate and publish it atomically: new requests use the new generation, while in-flight requests retain their original routing, credentials, response limits and deadline through body cleanup. Removing a model or rotating a key does not revoke already admitted work. Failed reads, validation, candidate construction or activation leave the current generation serving traffic. Shutdown cancels pending reload activation before kernel cleanup; reload publication uses a ten-second deadline.

`server.listen` and `limit.concurrency` require restart. Other supported request settings, routes, providers and credentials can reload. Unchanged rate/quota policies retain their counters; changing active rate policies or live/consumed quota policies still rejects the candidate as documented below. Health state is reused only for unchanged backend identities. Reload does not clear process-local budgets or force old streams to finish.

`nyro::reload` events report `outcome=applied` or `unchanged` with a numeric generation ID. Rejection events use `outcome=rejected` and a safe reason:

| Reason | Action |
|---|---|
| `read_failed` | Restore a readable configuration file |
| `invalid_config` | Correct YAML, unknown fields, references or invalid values |
| `restart_required` | Restore the listener/concurrency settings, or restart to change them |
| `candidate_rejected` | Check active rate/quota policy changes and runtime construction constraints |
| `activation_failed` / `interrupted` | Check shutdown/deadline conditions before retrying |

Reload logs exclude configuration contents, file paths, fingerprints and detailed error chains. A rejected file is not rewritten; correct it and send another signal. Regression: `cargo test -p nyro reload::tests` and `python3 tests/proxy_reload_smoke.py` after building the root binary.

## Configuration

All configuration structures reject unknown fields. `kind` is required and accepts `openai`, `anthropic`, or `gemini`. OpenAI providers optionally select `api: chat_completions` (default) or `api: responses`; other kinds reject the `api` field. Provider URLs must use HTTP or HTTPS, have a host, and contain no user information, query, or fragment. Nyro appends the native endpoint to the configured base path. Upstream requests use only the provider's optional `api_key`; caller credentials are not forwarded. Redirects, environment-configured HTTP proxies, and Reqwest automatic protocol retries are disabled for upstream calls; the LLM runtime owns the retry budget.

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
- `rate`: optional per-model request frequency; omitted means disabled. See [Request rate](#request-rate).
- `quota`: optional cumulative model token budget; omitted means disabled. See [Token quota](#token-quota).
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

Authentication, authorization, and shared admission apply once to the public model. Nyro then prepares each enabled backend locally using its strict codec or the native mode below, excludes backends that cannot represent the request, and selects from the lowest available `priority`, randomly in proportion to weight within that priority. The weights apply to the eligible subset; they are not per-batch traffic guarantees. Preparation sends no network requests. If none can represent the request, the response is `400` and no upstream is called. For example, without a token limit an Anthropic backend is ineligible while a compatible OpenAI backend can still be selected. Preparation cannot determine actual provider availability or whether its eventual response is convertible.

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

Health is scoped to a public model and backend identity. The root composition shares it across generations for unchanged provider URL/API/credentials/native mode, upstream model, and health policy; weight or priority changes retain it. A changed binding gets fresh health state. Retired bindings are released after their generations and requests drop. There is no background probe or persistence across process restarts.

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

## Request rate

Each public model can opt into an in-memory token bucket:

```yaml
rate:
  requests: 60
  period_ms: 60000
  burst: 5
```

`requests` and `period_ms` are required positive integers; `burst` defaults to `1` and must be positive. Counts and burst fit `u32`; the period must fit the platform's monotonic timer. This example starts with capacity for five immediate requests, then refills continuously at one request per second, up to five. It is a sustained average with a burst allowance, not a strict count in every rolling minute. No refill task or waiting queue is created.

All callers, including anonymous callers, and all supported workloads and ingress protocols share the bucket for that public model. Different aliases have separate buckets even if they route to the same provider or upstream model. Scope is process-local; replicas do not share counters. This limits logical requests, not generated tokens or concurrent streams.

Authentication, authorization, concurrency admission, compatible-backend preparation, and the initial cancellation/deadline check precede rate admission. Those earlier rejections do not consume rate capacity. Once admitted, a request consumes one unit regardless of upstream success, failure, unavailable backends, subsequent cancellation, or response-body drop. Internal retries and failover do not consume additional units, and completed or failed requests do not refund units.

Excess requests receive a sanitized `429` in their native error format and an integer `Retry-After` header rounded up to seconds. Nyro sends no upstream request and immediately releases the acquired concurrency permit, even if the error body is left unread. The delay is advisory: another caller may consume the next unit first. Other `429` causes, such as concurrency rejection or upstream errors, do not acquire this rate header.

The root composition shares rate state across configuration generations. An unchanged public-model rule retains its balance when routes, provider credentials, or other settings change. Changing a rule while its old binding is active rejects candidate construction; restart the process to change its parameters. Removing/disabling a rule lets existing generations finish with their binding, which is released after its last owner drops. A fresh binding or process restart starts with the configured burst capacity; no rate state is persisted. Use SIGHUP on Unix to reload other supported file changes.

The reusable primitive is `nyro_limit::rate::RateLimit`. It accepts counts, a `Duration`, and burst capacity, and returns either admission or a retry delay. It contains no LLM, authentication, HTTP, kernel, or database types; the application owns scope mapping and rejection formatting. Regression: `cargo test -p nyro-limit` and `cargo test -p nyro-llm --test rate_runtime`.

## Token quota

Add an optional `quota` to a public model:

```yaml
quota:
  total_tokens: 1000000
  reserve_tokens: 4096
```

Both fields are required positive integers; `reserve_tokens` must not exceed `total_tokens`. Explicit `null` and unknown fields are rejected. All callers, workloads and ingress APIs for that alias share one cumulative budget of input plus output tokens. Different aliases have independent budgets. This is process-local accounting with no periodic refill, persistence, pricing or replica coordination.

After authentication, authorization, concurrency and rate admission, Nyro reserves `reserve_tokens` immediately before each available upstream attempt. Retries each need their own reservation. No available backend means no reservation. Admission atomically checks settled usage plus pending reservations. Insufficient credit returns a native `429`: OpenAI APIs use `quota_exceeded`, Anthropic uses `rate_limit_error`, and Gemini uses `RESOURCE_EXHAUSTED`. There is no `Retry-After` or new upstream call, and the concurrency permit is released immediately. A prior rate admission remains charged even when quota rejects the request. A remaining balance below `reserve_tokens` cannot admit another attempt.

On a valid complete response, Nyro replaces the reservation with reported total usage, including explicit zero and usage above the reservation. For JSON this happens before downstream encoding; for SSE it happens at the validated upstream protocol terminal. Usage snapshots are cumulative, not additive, and must not decrease. Input plus output must equal total without overflow; embeddings require input to equal total. Invalid usage fails the response (`502` before streaming, otherwise stream termination). OpenAI Chat upstream requests ask for stream usage for observation even when the client has not requested usage output; the client's output preference is preserved.

Before settlement, missing usage, upstream HTTP errors, malformed responses, cancellation, deadlines and dropped/truncated streams charge the greater of the reservation and any valid observed usage. Once settled, later downstream failure or body drop does not change the charge. Only an observed connection-establishment failure releases the entire reservation. Each failed HTTP attempt is charged separately before failover. These conservative fallback charges may overcount actual usage. Conversely, `reserve_tokens` is an operator-selected amount, not a trusted upper bound on provider consumption: actual usage can exceed the configured budget, and the resulting debt blocks later admissions. This is not a hard cap on actual upstream tokens.

The root composition retains consumed and pending ledgers across generations, routing changes, and model removal/re-addition. Changing a rule while its binding is live or its ledger has consumed credit rejects candidate construction. Unused, unowned candidate ledgers can be discarded. Retained ledgers last for the registry's lifetime; process restart resets all balances and is required to change an established rule. Other supported file edits can reload with SIGHUP on Unix. Disabling quota stops accounting for new requests; it does not erase an existing ledger.

`nyro_limit::quota::Quota` provides generic atomic reservation, settlement and snapshots without LLM, HTTP, kernel or storage types. `nyro_llm::quota::QuotaRegistry` owns the model mapping and token semantics. Library hosts should reuse `runtime::SharedResources` with `Runtime::with_resources` across generations; `Runtime::new` creates fresh registries and `with_health` shares health only. Regression: `cargo test -p nyro-limit` and `cargo test -p nyro-llm --test quota_runtime`.

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

The strict conversion path is an experimental text/function Chat subset. Its codecs reject image/audio/video, thinking/signature blocks, unrepresentable content ordering, multiple candidates, and unsupported vendor options or diagnostics. Examples include Anthropic caching/usage details and matched stop-sequence responses; Gemini safety settings, safety ratings, grounding/citations, prompt feedback and structured-output settings; and OpenAI response fingerprint/service-tier/logprob metadata when it cannot be represented by a native output. Supported fields in one protocol are not automatically representable in another: unsupported requests fail before dispatch; unsupported upstream responses fail with `502` or a terminated SSE stream. Native Embedding APIs and full SDK/vendor feature parity remain future work.

Protocol reference: [Anthropic streaming](https://platform.claude.com/docs/en/build-with-claude/streaming), [Gemini generateContent](https://ai.google.dev/api/generate-content). Local matrix regression: `cargo test -p nyro-llm --test protocol_matrix`.

## Strict tool history and results

When strict conversion selects an Anthropic or Gemini upstream, all results for one assistant tool-call batch are placed in the immediately following single `user` message. Results keep their original order and `tool_call_id`/`tool_use_id` association, even when they return in a different order from the calls. User text immediately following the completed batch is appended after the result blocks. Tool names, parsed object arguments and result text are preserved; Gemini object results retain the existing JSON-as-text conversion.

Every call in the batch must have exactly one adjacent result. Duplicate call IDs within a batch, missing/unknown/duplicate results, and user or assistant messages inserted before the batch is complete make that backend ineligible before dispatch. Nyro does not fabricate calls, drop intermediate text or rearrange history to repair it. If no backend can represent the original request, the runtime returns `400`. These are destination-specific checks in the strict Anthropic and Gemini encoders; opt-in matching native forwarding keeps its existing contract.

This covers OpenAI Chat, stateless Responses, Anthropic Messages and Gemini generateContent ingress, with JSON or SSE responses. The strict result boundaries are:

| Result/history form | Supported conversion |
|---|---|
| Text result blocks, including an empty array | OpenAI Chat, Responses and Anthropic preserve block boundaries. Gemini accepts at most one text block; multiple blocks make that backend ineligible. |
| Anthropic omitted result content | Decodes as an empty result, without inventing text. |
| Anthropic `is_error:true` | Preserved by the typed IR and Anthropic output. OpenAI Chat, Responses and Gemini backends are ineligible because an explicit execution-error flag has no supported lossless mapping. |
| Responses result string or `input_text` array | Preserves the string or ordered blocks. Media, `output_text` and refusal result blocks remain unsupported. |
| Gemini result object | Preserved as an object for Gemini, or serialized as JSON text for other targets. An `error` key remains business data and does not create an execution-error flag. |
| Gemini missing result ID | Resolved only when the pending function name identifies exactly one call. Explicit IDs must also match the function name; generated missing call IDs avoid explicit IDs anywhere in the history. |
| Gemini text mixed with function results | Decoded in order into separate IR messages. Anthropic/Gemini targets reject text interrupting an unfinished result batch; compatible OpenAI Chat/Responses targets retain the order. |

No calls are synthesized or content discarded to repair a history. Cross-protocol thinking/signature mapping and media tool results remain unsupported. Function Schema conversion is described below. References: [Anthropic tool results](https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls), [Gemini FunctionResponse](https://ai.google.dev/api/generate-content). Local regressions: `cargo test -p nyro-llm --test anthropic_codec --test gemini_codec --test responses_codec --test responses_runtime --test protocol_matrix`.

## Function parameter schemas

The strict path preserves JSON Schema objects in OpenAI Chat `parameters`, Responses `parameters`, Anthropic `input_schema` and Gemini `parametersJsonSchema`. It does not remove `$ref`, `$defs`, `additionalProperties` or other constraints, resolve references, rewrite defaults, or repair schemas. Missing parameters keep the existing no-parameter conversion. Function names must be nonblank and supplied parameter schemas must be JSON objects; this is envelope validation, not a full JSON Schema validator or a guarantee that every model supports every keyword. Argument execution/validation remains the client's responsibility.

OpenAI Chat `strict:false` is accepted for all four destinations. `strict:true` is preserved for OpenAI Chat/Responses and makes Anthropic/Gemini destinations ineligible in the current strict codecs. Responses ingress still requires an explicit boolean `strict`; outgoing Responses always supplies it, defaulting to `false` when the source does not require strict enforcement. Nyro does not add `required` or `additionalProperties:false` to obtain strict-mode compatibility. See [OpenAI function strict mode](https://developers.openai.com/api/docs/guides/function-calling#strict-mode).

Gemini's native `parameters` uses a different Schema dialect and is converted to JSON Schema before destination selection:

| Native Schema field | Strict conversion |
|---|---|
| Known `type` names | Lowercase JSON Schema types; unknown types are rejected. |
| `properties`, `items`, `anyOf` | Recursively convert only schema positions; `anyOf` must be a nonempty array. |
| `nullable:true` | Add `null` to the explicit type. Combinations with `enum` or `anyOf` are rejected rather than assuming how the constraints interact. |
| `minItems`/`maxItems`, `minProperties`/`maxProperties`, `minLength`/`maxLength` | Convert nonnegative int64 decimal strings or integer values to JSON numbers, without rounding. |
| String `enum`, `required`, `minimum`/`maximum`, `title`, `description`, `format`, `pattern` | Validate field shapes and preserve values. Native enum is supported only for string types. |
| `default` | Preserve literal JSON data without recursively treating it as a schema. |
| Other native fields, including `propertyOrdering`, `example`, `$ref` and `additionalProperties` | Reject without pruning. Use `parametersJsonSchema` for JSON Schema objects; matching opt-in native forwarding retains its existing contract. |

These checks do not prove satisfiability, resolve references or emulate vendor enforcement. See [Gemini Schema and FunctionDeclaration](https://ai.google.dev/api/generate-content#Schema). Regressions: `cargo test -p nyro-llm --test tool_schema_codec --test responses_runtime`.

## Opt-in OpenAI Chat native compatibility

Set `native_chat: true` on an OpenAI Chat provider to preserve vendor JSON fields between `POST /v1/chat/completions` and an OpenAI Chat upstream:

```yaml
llm:
  providers:
    example:
      kind: openai
      api: chat_completions
      native_chat: true
      base_url: https://api.example.com/v1
      api_key: replace-with-provider-secret
```

This provider fragment extends your existing configuration. The default is `false`; `true` is supported for OpenAI Chat/Responses, Anthropic Messages and Gemini providers. An OpenAI Chat provider only uses this mode for matching OpenAI Chat ingress; other ingress APIs and Embedding continue through strict codecs.

Nyro preserves request JSON, non-streaming response JSON and SSE data JSON, including nested reasoning/tool history, vendor options and usage details. It rewrites the top-level model to the upstream model on requests and the public alias on responses/chunks. For streams it forces upstream `stream_options.include_usage: true`, preserving other options; downstream usage is included only when requested. When usage is hidden, usage-only chunks are omitted and accounting still observes them.

Native mode validates the routing/control envelope (model, message roles, stream controls), response/chunk envelope, numeric usage counters and bounded SSE framing with an explicit `[DONE]`. It delegates vendor field semantics to the selected upstream. It is JSON field preservation, not byte-for-byte forwarding: serialization and SSE framing may change, comments/IDs/retry fields are discarded, event names must be absent, empty or `message`, and arbitrary headers are not forwarded. Existing authentication, authorization, admission, quota settlement, retry limits, deadlines and cleanup remain mandatory. Failed upstream responses remain sanitized. Changing native mode changes the configuration fingerprint and resets the affected health binding.

For mixed backend pools, a native request can use a strict or different-protocol backend only if the **original** request passes the strict OpenAI codec and the destination can represent it. Nyro never strips extension fields to make failover eligible. Native mode does not promise cross-protocol reasoning/media conversion, complete SDK sessions, or native fidelity across different OpenAI APIs.

Local recorded regression: `python3 tests/proxy_native_replay.py` after `cargo build -p nyro`. It compares complete parsed request/response sequences for eight OpenAI Chat recordings from DeepSeek and Zhipu AI, also replays eight Anthropic Messages recordings, and classifies all sixteen recordings under the default strict mode. These historical fixtures are not live vendor certification.

## Opt-in Anthropic Messages native compatibility

An Anthropic provider can also enable `native_chat: true`. Only matching `POST /v1/messages` ingress and Anthropic upstreams use this mode. Keep `kind: anthropic`, the provider base URL and static API key; do not set the OpenAI-only `api` selector. OpenAI and Anthropic native providers in the same pool are not interchangeable: a request may cross protocols only when its original JSON passes the source strict codec and the destination can represent it.

Requests preserve system/cache options, thinking and signatures, tool history and vendor fields. Nyro validates the model, messages, positive `max_tokens` and stream controls, while the upstream validates vendor semantics. JSON responses preserve content blocks, stop details and usage extensions; only `model` changes to the public alias. SSE preserves event names and data JSON, including pings, thinking/signature deltas and tool fragments; the nested `message_start.message.model` changes to the alias. Framing, comments, IDs and retry fields are not preserved byte-for-byte.

The native stream validates matching event names/types and the lifecycle: one `message_start`, sequential indexed content blocks, message deltas, then `message_stop` after a stop reason and all blocks are closed. Usage-bearing message deltas may repeat. An omitted stop reason retains the earlier value; an explicit null clears it and requires a later nonempty reason before completion. Missing completion, malformed/out-of-order events, unknown top-level event types and upstream error events fail without retry after `2xx`; upstream error text is not forwarded. Content-block extensions remain opaque inside this lifecycle, with basic shape checks for known block/delta fields. Tool fragments are forwarded incrementally without assembling or repairing their final JSON, and thinking signatures are not cryptographically validated. These are upstream semantics, not cross-protocol conversion support.

For quota and observations, total input is `input_tokens + cache_creation_input_tokens + cache_read_input_tokens`; absent cache counters start at zero. `message_delta` counters are cumulative: each message delta must report `output_tokens`, omitted input/cache counters retain their earlier values, and repeated snapshots are not added again. Invalid numeric counters, decreases and overflow fail the response. Output tokens are added once to total input; nested cache-duration breakdowns, service-tier metadata and server-tool counters are preserved but do not add token charges. Settlement occurs at `message_stop`; failure/drop before then retains the existing conservative fallback charge. See the [Anthropic cache accounting](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#tracking-cache-performance) and [stream event contract](https://platform.claude.com/docs/en/build-with-claude/streaming).

Authentication, authorization, rate/concurrency admission, health, deadlines and generation ownership use the existing shared path. Upstream authentication remains the configured `x-api-key` with `anthropic-version: 2023-06-01`; caller credentials, arbitrary headers and `anthropic-beta` are not forwarded. This increment does not add OAuth/account channels or beta-header configuration.

## Opt-in Gemini generateContent native compatibility

Set `native_chat: true` on a `kind: gemini` provider (without `api`). It applies only to matching Gemini ingress and upstreams; strict source-codec validation still gates fallback to other formats. Both `/v1beta/models/{alias}:generateContent` and `/v1/models/{alias}:generateContent` are accepted, with corresponding `:streamGenerateContent` actions. Streaming permits only `alt=sse`; credentials belong in headers.

The URL determines the public model and streaming mode. Nyro replaces the upstream URL model with the configured `upstream_model`, and never injects `model` or `stream` into the JSON body; client bodies containing either field are rejected, even if null. `modelVersion` is preserved as upstream version metadata, rather than rewritten to an alias. Upstream requests use only the provider's `x-goog-api-key`; caller keys and arbitrary headers are not forwarded.

Native requests/responses preserve JSON fields, including thought signatures, function history, inline/file media data, caching, safety settings/ratings, grounding/citations and structured-output options. These payloads stay private to the runtime. Nyro validates the contents/parts and routing envelopes and basic known field types; vendor semantics and signature verification remain upstream responsibilities. Optional non-accounting fields accept `null` as unset and preserve it in JSON, matching ProtoJSON; this does not relax required usage counters or forbidden body routing fields. This increment retains the single-candidate scope: `candidateCount` must be unset or `1`, response candidates use index `0` or leave it unset, and multiple candidates are rejected. A blocked prompt without candidates is a valid native response when `promptFeedback.blockReason` is a nonempty, non-unspecified value; its JSON feedback is preserved with HTTP `200`.

Gemini SSE uses JSON data frames with no synthetic `[DONE]`. A nonempty, non-unspecified candidate finish reason or blocked-prompt reason establishes a terminal response, but success is settled only at clean EOF. Trailing usage-only frames remain visible and are accounted for. More candidates after a terminal response, mismatched framing, upstream error payloads, transport failures, or EOF without a terminal response fail without another attempt after `2xx`. Unknown nonempty finish/block enum values are retained for forward compatibility; event names must be absent, empty or `message`.

When `usageMetadata` is supplied, `promptTokenCount` and `totalTokenCount` must be nonnegative integers; omitted candidate/thought counts mean zero and must agree with the reported total. Accounting input is prompt tokens (which already include cached tokens), output is candidate plus thought tokens, and total is their checked sum. Cached counts cannot exceed prompt tokens and are not added again. Input, output and total snapshots cannot decrease; changes within the candidate/thought split or cache subset are preserved when those aggregate constraints hold. Nonzero `toolUsePromptTokenCount` is rejected in this increment because separate tool-prompt accounting is not implemented. Detail arrays and other metadata are preserved without adding charges. See the [Gemini response and usage reference](https://ai.google.dev/api/generate-content#UsageMetadata).

If usage is absent throughout, completion retains unknown-usage fallback accounting. If an early usage snapshot was reported, a complete snapshot must also arrive at or after the terminal response; otherwise EOF fails and keeps the conservative fallback charge. An early zero-output snapshot cannot become final usage merely because the connection ended. Deadlines, cancellation, rate/concurrency, health and generation cleanup use the existing shared path.

Run `python3 tests/proxy_gemini_native_smoke.py` after building `nyro`. This is a local mock contract test, not a recording or live SDK/vendor certification; the sixteen historical OpenAI/Anthropic recordings remain separate. Multiple candidates, separate tool-prompt accounting, Gemini Interactions and cross-protocol thinking/media conversion remain outside this increment.

## Opt-in Responses native compatibility

Set `kind: openai`, `api: responses` and `native_chat: true` on the provider to preserve JSON fields between `POST /v1/responses` and a matching Responses upstream. Other formats still require the **original** request to pass the strict source codec before fallback. The flag remains off by default; OpenAI Chat Completions and Responses are distinct formats even within the same provider family.

The native path accepts stateless string input or message, reasoning, function-call and function-result history; an empty input array is also accepted when `instructions` supplies a string prompt. It preserves encrypted reasoning, assistant `phase`, media content parts, function schemas, structured-output options, metadata, response IDs, output items, annotations, logprobs and vendor extensions. Function-result arrays and streaming obfuscation are preserved. Item payloads stay opaque: Nyro validates the routing and lifecycle envelope rather than reconstructing or certifying every vendor item or delta.

Only the request model, the top-level response model and the model inside lifecycle-event `response` snapshots are rewritten. Outbound requests also normalize omitted/null `store` to `false`. `store:true`, `background:true`, non-null `conversation`/`previous_response_id`, item references and non-function tool definitions/choices are rejected before dispatch. Retrieval, deletion, cancellation, hosted tools, background jobs, OAuth/account channels and cross-protocol reasoning/media conversion remain outside this mode. Caller headers are not forwarded; the configured static upstream API key is used.

Completed JSON must have status `completed`, or `incomplete` with a nonempty reason. Failed/nonterminal envelopes and upstream error bodies remain sanitized. If usage is present, `input_tokens`, `output_tokens` and `total_tokens` must be nonnegative integers with checked input-plus-output equality. Cached input and reasoning output detail counters must fit inside their respective totals and are not added again. Missing/null usage uses the existing unknown-usage/reservation fallback.

SSE must begin with `response.created`, keep response ID/model/creation time consistent across lifecycle snapshots and carry contiguous `sequence_number` values starting at zero. An explicit SSE event name must match JSON `type`; data-only events retain their original form. Intermediate `response.*` extension events are preserved, but only `response.completed` or `response.incomplete` establishes a successful protocol terminal. Only terminal usage is used for settlement, so an early snapshot cannot masquerade as final usage. The stream must then reach clean EOF; further events, failed/error events, identity/sequence mismatches or truncation fail without retry after an accepted upstream response. There is no synthetic `[DONE]`. SSE comments/IDs/retry fields and serialization whitespace are not preserved.

References: [OpenAI stateless conversation history](https://developers.openai.com/api/docs/guides/conversation-state), [Responses streaming](https://developers.openai.com/api/docs/guides/streaming-responses). Local checks: `cargo test -p nyro-llm --test native_responses_runtime` and `python3 tests/proxy_responses_native_smoke.py` after building the root binary. These are local mock checks, not recorded Responses traffic or full SDK/Codex CLI certification.

## Responses strict conversion subset

`POST /v1/responses` accepts string input or typed message/function items, instructions, common generation controls, and client function tools/results. In this strict conversion path, Responses function definitions must explicitly set `strict`: use `false` for the portable non-strict subset; `true` requires an upstream that can preserve it. It can use any configured Chat upstream. Conversely, all supported Chat ingress APIs can use a provider with `api: responses`. For example:

```sh
curl http://127.0.0.1:19530/v1/responses \
  -H 'Authorization: Bearer replace-with-client-secret' \
  -H 'Content-Type: application/json' \
  -d '{"model":"responses-default","input":"Hello","max_output_tokens":256,"store":false,"stream":true}'
```

Function results accept a string `function_call_output.output` or an array containing only `input_text` blocks, including an empty array. Conversion preserves block boundaries rather than concatenating them. Multiple result blocks are incompatible with a strict Gemini destination; media and other result block types are rejected.

This strict conversion path is stateless: outbound Responses always sets `store:false`, and Responses ingress translated to Chat Completions also explicitly disables storage. Server conversation state (`conversation`, `previous_response_id`), item references, `store:true`, `background:true`, hosted tools, reasoning items, media, and response retrieval/deletion/cancellation are unsupported. Meaningful options or output items that the current Chat IR cannot preserve are rejected. Responses envelope echoes and item IDs are normalized; exact upstream IDs and request echoes are not preserved. Function `call_id`, content order, finish status, and representable usage remain part of the conversion contract.

SSE uses named Responses lifecycle events, stable item IDs, ordered sequence numbers, and a full terminal snapshot, without a `[DONE]` marker. Token-limit/content-filter endings produce `response.incomplete`. Failed, contradictory, malformed, or truncated upstream streams terminate without a fabricated successful completion. Strict Responses upstream streaming disables obfuscation. This subset does not establish full Responses SDK or Codex CLI compatibility.

References: [OpenAI Responses migration](https://developers.openai.com/api/docs/guides/migrate-to-responses), [Responses streaming](https://developers.openai.com/api/docs/guides/streaming-responses). Local regressions: `cargo test -p nyro-llm --test responses_codec --test responses_runtime`.

## Request and usage observations

The LLM runtime emits structured `tracing` events at `INFO`: `nyro::attempt` once per dispatched upstream attempt and `nyro::request` once when a request finishes. The default root filter (`nyro=info`) includes both; `RUST_LOG` controls filtering. Each runtime request gets a random 128-bit `request_id`, returned as 32 hexadecimal characters in `x-request-id`. Supplied client IDs are not adopted or forwarded. Health probes and requests rejected before acquiring a runtime generation are outside this scope.

| Record | Fields and meaning |
|---|---|
| Both | `request_id`, configured public `model`, `backend`, API `protocol`, and `duration_ms` |
| Request | `workload`, `streaming`, `attempts`, HTTP `status` (`0` before a response), `outcome`, `delivery_outcome`, safe `error_code`, usage totals and quota charges |
| Attempt | One-based `attempt`, configured `provider` ID, `upstream_status` (`0` before headers), `outcome`, usage and quota settlement |

Request `outcome` is `complete`, `error`, `cancelled` or `timeout`. `delivery_outcome` separately describes response-body completion and is `none` if the handler future was dropped before handoff. Reading an error body to EOF does not make a failed request successful. Dropping a decoded JSON response can produce a cancelled request with a completed upstream attempt. Attempt outcomes distinguish `complete`, `http_error`, `connect_error`, `transport_error`, `protocol_error`, `cancelled` and `timeout`. Attempt completion means validated JSON or an SSE protocol terminal; it does not prove that the client received all bytes. Durations measure the corresponding owned lifetime, not network flush or first-token latency. `error_code` records execution failures before handoff; later body failures are represented by the outcome fields.

Usage collection runs even without quota. Streaming OpenAI Chat upstream requests always ask for usage, while downstream usage output follows the client's preference. Each attempt records the last valid cumulative `input_tokens`, `output_tokens` and `total_tokens`; absent fields mean unknown usage, while explicit zero remains known. `usage_state` is `complete`, `partial`, `missing` or `invalid`. Invalid sums or decreasing totals are marked invalid and excluded from observation updates; without quota this introduces no additional protocol rejection. Existing codec validation still applies. With quota, the existing strict accounting checks apply.

Request token totals sum valid observations across attempts, rather than adding repeated stream snapshots. A request with any unknown or incomplete attempt is not reported as complete usage; its totals contain only observed units, so inspect `usage_state` before interpreting them. No attempts yields `not_attempted`. Attempt `quota_charged_tokens` is absent when quota is disabled, and `quota_outcome` distinguishes `actual`, `fallback`, `released` and `disabled`. Request `quota_charged_tokens` sums actual ledger charges, including conservative fallback charges; it is not interchangeable with reported token usage.

Events contain no credentials, provider URLs, request paths, client-supplied IDs, prompts or response bodies. Unknown client model names are not logged. The current sink is the host's tracing subscriber: there is no durable event store, statistics query API, metrics exporter or distributed trace propagation. Crashes, forced termination, filtering and sink failures can lose records; quota settlement does not depend on a log consumer. Regression: `cargo test -p nyro-llm --test observation_runtime`.

## Health and current scope

- `GET /healthz` returns `200` while the HTTP process is serving.
- `GET /readyz` returns `200` while the kernel host accepts new generation leases and `503` once it does not. This source-built proxy has no database or upstream-backend readiness check.

Retries and passive health are opt-in as described above. It does not implement persistent/shared quota storage, a control plane, Admin API, WebUI, `nyro serve`, or `nyro tool`. It does not expand environment variables, accept the legacy standalone YAML format, watch the file automatically, or fetch configuration remotely. Unix supports explicit SIGHUP reload; unsupported setting changes require restart.

Contributors can exercise the root process, probes, authentication, Chat, Embedding, SSE, redaction, and graceful termination without a real provider:

```sh
cargo build -p nyro
python3 tests/proxy_smoke.py
```
