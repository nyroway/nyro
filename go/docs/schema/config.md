# Standalone `config.yaml`

The user-facing configuration for the standalone Go gateway. `${VAR}` references
anywhere in the file are expanded from the process environment before parsing.

## Full Example

```yaml
version: 1

settings:
  proxy:
    request_timeout: "120s"
    connect_timeout: "30s"
    max_retries: 2
    retry_on_status: [429, 500, 502, 503, 504]
    max_body_bytes: 33554432

  # Optional. Omit this block to use process-local memory.
  state:
    type: "redis"
    url: "${NYRO_REDIS_URL}"

  telemetry:
    logs:
      exporter: "stdout"
    metrics:
      exporter: "prometheus"
      listen: ":9464"
      path: "/metrics"
    traces:
      exporter: "otlp"
      endpoint: "http://127.0.0.1:4317"
      protocol: "grpc"

upstreams:
  # Manual model list (persisted)
  - name: "deepseek-manual"
    provider: "deepseek"
    credentials:
      api_key: "${DEEPSEEK_API_KEY}"
    models:
      - "deepseek-chat"
      - "deepseek-reasoner"
    enabled: true

  # Model discovery (fetched live, not persisted). Known provider: models_url
  # may be omitted, falling back to the provider preset's default.
  - name: "openai-main"
    provider: "openai"
    credentials:
      api_key: "${OPENAI_API_KEY}"
    proxy:
      url: "http://127.0.0.1:7890"
    enabled: true

  # Custom provider with an explicit discovery URL (required for custom)
  - name: "local-vllm"
    provider: "custom"
    base_url: "http://127.0.0.1:8000/v1"
    credentials:
      api_key: "${LOCAL_API_KEY}"
    models_url: "http://127.0.0.1:8000/v1/models"
    enabled: true

routes:
  - model: "gpt-4o"           # client-visible model name (unique)
    balance: "weighted"       # weighted | priority | cooldown | latency
    enable_auth: true
    enable_payload: false
    enabled: true
    upstreams:
      - name: "openai-main"   # references upstreams[].name
        model: "gpt-4o"       # the model actually sent to this upstream
        weight: 100
        priority: 1
        enabled: true

consumers:
  - name: "default-app"
    enabled: true
    metadata:
      team: "growth"
    keys:
      - name: "primary"
        api_key: "${NYRO_API_KEY}"   # empty = auto-generate
        enabled: true
        expires_at: null
    access:
      models: ["gpt-4o"]             # empty/omitted = allow all models
      protocols: ["openai-chatcompletions"]     # empty/omitted = allow all protocols
      ip_allowlist: ["10.0.0.0/8"]   # empty/omitted = allow all source IPs
    quotas:
      concurrency:
        limit: 10                    # max concurrently in-flight requests
      requests:
        - limit: 60
          window: "1m"
        - limit: 10000
          window: "1d"
      tokens:
        - limit: 100000
          window: "1m"
      budgets:                       # persisted only; not enforced yet
        - limit: 100
          window: "1mo"               # s | m | h | d | Nmo (N natural months)
          currency: "USD"
    limits:
      max_input_tokens: 4000
      max_output_tokens: 2000
      max_request_body_bytes: 1048576
```

## Upstream Model Declaration

An upstream declares its models in exactly one of two mutually exclusive ways.
Setting both `models` and `models_url` on the same upstream is a validation
error.

- `models`: static, manually curated list. Persisted in `upstreams.models_json`.
  Use for `custom` providers or when you want a fixed, curated set.
- `models_url`: a discovery endpoint URL. Only the URL is persisted
  (`upstreams.models_url`); the model list itself is fetched live at
  control-plane request time (route dropdown, health check) with a short
  in-memory TTL cache and is never written to the database.
  - Known provider: `models_url` may be omitted, in which case the provider
    preset's default discovery URL is used.
  - `custom` provider: `models_url` is required (no preset to fall back to).

Neither `models` nor `models_url` affects data-plane routing; routing always
uses `routes[].upstreams[].model`.

## Runtime activation

Standalone YAML and ConfigSync both produce a complete immutable runtime
Snapshot. The reconciler compares its deterministic fingerprint with the active
generation, treats an equal fingerprint as a no-op, and otherwise builds an
inactive typed candidate. The kernel validates and starts that candidate's
resource dependencies before atomically publishing it. Candidate construction
or startup failure closes the candidate and preserves the last-known-good
generation.

Standalone mode performs the initial activation synchronously and fails startup
when the first candidate cannot be built or started. ConfigSync starts live but
not ready until one candidate activates; a rejected hot candidate does not stop
the stream and does not replace the last-known-good generation. Each request
lease pins its full Snapshot, LLM Runtime, and generation-owned resources until
the request releases it.

## Field Reference

- `settings.proxy`
  - `request_timeout`, `connect_timeout`: Go duration strings; invalid or
    omitted values use the gateway defaults (`120s` and `30s`).
  - `max_retries`: retry attempts per backend (default `2`).
  - `retry_on_status`: upstream HTTP statuses that trigger retry/failover.
  - `max_body_bytes`: gateway-wide request-body cap in bytes (default
    `33554432`, 32 MiB). This is distinct from
    `consumers[].limits.max_request_body_bytes`, which is a per-consumer cap.
- `settings.state`: selects the backend used by runtime State consumers. The
  current consumer is quota enforcement; this block deliberately has no
  quota-specific or other business sub-blocks.
  - If the whole block is absent, the gateway uses process-local memory.
    `state: {type: memory}` is the explicit equivalent and must not include
    `url`. A present but empty block is rejected.
  - `type: redis` requires `url`, which must be an absolute `redis://` URL.
    Standard URL user information and path syntax carry the
    Redis username, password, and database, for example
    `redis://user:password@redis.internal:6379/2`. Prefer an environment
    variable so credentials are not committed to YAML. `redis://` is
    plaintext; use loopback, a trusted private network, or an independently
    encrypted tunnel. External services must
    be Redis 7.0+ because concurrency leases use conditional `EXPIRE NX/GT`.
    Before installation, the gateway probes the complete quota path and
    removes its isolated probe keys. The Redis account must permit `PING`,
    `WATCH`, `UNWATCH`, `MULTI`, `EXEC`, `TYPE`, `MGET`, `INCRBY`, `EXPIRE`,
    `DEL`, `ZREMRANGEBYSCORE`, `ZADD`, `ZCARD`, and `ZREM`, plus the connection
    setup commands required by its URL (`AUTH`, `HELLO`, `SELECT`, and `CLIENT`
    where applicable).
  - Standalone YAML validates and connects to the selected backend during
    startup; an unavailable Redis backend fails startup instead of silently
    falling back to memory. With config-sync, `state.type` and `state.url` are
    pushed to every data-plane node. A hot-update candidate is retried in the
    background while the last-known-good backend continues serving.
  - One pushed Redis URL must be reachable from every subscribed data-plane
    node. Use shared Redis when quotas must be enforced across replicas;
    process-local memory gives each node an independent quota view.
- `upstreams[]`
  - `name` (required, unique): upstream instance name, referenced by routes.
  - `provider` (required): a provider preset id (e.g. `openai`, `deepseek`,
    `anthropic`, `gemini`, `openrouter`) or `custom`. Persisted so the UI can
    re-anchor the selected preset and so discovery URL fallback can look up the
    preset.
  - `protocol` (optional): defaults to the provider preset's default protocol.
  - `base_url` (optional): defaults to the preset's protocol base URL; required
    for `custom`.
  - `credentials` (map): provider-specific credential object (e.g. `api_key`).
  - `proxy.url` (optional): outbound HTTP proxy for this upstream.
  - `models` xor `models_url`: see above.
  - `enabled` (optional, default true).
- `routes[]`
  - `model` (required, unique): client-visible model name.
  - `balance`: `weighted` | `priority` | `cooldown` | `latency`.
  - `enable_auth`, `enable_payload`, `enabled`.
  - `upstreams[]`: `name` (upstream ref), `model` (upstream-side model),
    `weight`, `priority`, `enabled`.
- `consumers[]`
  - `name`, `enabled`.
  - `metadata`: free-form string map, informational only (not used for access
    decisions).
  - `keys[]`: `name`, `api_key` (empty = auto-generate), `enabled`, `expires_at`.
  - `access`: `models[]` (granted client-visible route model names),
    `protocols[]`, `ip_allowlist[]`. The whole `access` block, or any one of
    its sub-fields, being empty/omitted means default-allow for that
    dimension — `models`, `protocols`, and `ip_allowlist` are each judged
    independently.
  - `quotas`: `concurrency.limit` (max concurrently in-flight requests, no
    window), `requests[]` / `tokens[]` (`limit` + `window`), and `budgets[]`
    (`limit` + `window` + `currency`). Window units are `s`/`m`/`h`/`d`, plus
    `Nmo` (N natural calendar months, e.g. `1mo`, `3mo`) for budgets. Budgets
    are validated and persisted but not enforced by the proxy in this version
    (enforcement requires a pricing table, planned for a later version).
    `concurrency` maps internally to `consumer_quotas.quota_type = "concurrency"`.
    Request and token windows use one-minute buckets and retain at most 24
    hours of effective history. Request admission checks all configured
    request windows and increments them as one atomic decision. Replicas that
    use the same Redis therefore enforce one shared request boundary. A clean
    request denial returns HTTP `429` and does not consume quota; once admitted,
    a request consumes one count even if its upstream exchange fails. Token
    quotas remain response-settled historical limits, so concurrent requests
    can cross a strict token boundary before their responses are recorded.
    An unavailable State backend returns HTTP `503` so infrastructure failure
    is not misreported as client overuse. Extreme `WATCH` contention can also
    fail one request closed with `503`, but does not mark the State backend
    unhealthy.
  - `limits`: `max_input_tokens`, `max_output_tokens`,
    `max_request_body_bytes` — per-request caps; omitted/zero means no limit.
- `settings.telemetry`: three independent signal blocks — `logs`,
  `metrics`, `traces`. Each signal picks its own exporter and owns a flat set
  of engine-specific fields; there is no shared/global exporter, endpoint, or
  export interval. The authoritative field schema (per exporter kind, per
  signal) lives in the schema definition tables in
  `go/internal/telemetry/schema/exporter.go` (exposed by `ExportersFor`); this
  section summarizes it.
  - A signal block that is **absent** means that signal is disabled (no-op
    provider) — this is the normal way to turn a signal off.
  - A signal block that is **present** (even `logs: {}` or bare `logs:`)
    **must** set `exporter`, or `LoadConfig` rejects it — an empty block is
    treated as a mistake, not "disabled".
  - `exporter` must be one of the kinds listed for that signal (below); an
    unknown kind is a validation error. There is no `"none"`
    sentinel value.
  - Valid exporters per signal:
    - `logs`: `stdout` | `otlp`
    - `metrics`: `stdout` | `otlp` | `prometheus`
    - `traces`: `stdout` | `otlp`
  - `stdout`: no fields.
  - `otlp` (all three signals): `endpoint` (required, no default — e.g.
    `http://127.0.0.1:4317`, an admin OTLP receiver address or an external
    collector), `protocol` (`http` | `grpc`, default `http`), `interval`
    (export batch interval, default `5s`).
  - `prometheus` (`metrics` only): `listen` (address the gateway's second,
    independent `/metrics` HTTP server binds, default `:9464`), `path`
    (default `/metrics`).
  - Fields not belonging to the selected `exporter` on a block are a
    validation error (e.g. `logs: {exporter: stdout, endpoint: "..."}` is
    rejected — `stdout` takes no fields).
  - `retention` and `data_dir` are **not** part of this gateway YAML layer —
    they are admin-side control-plane configuration and never appear under
    `settings.telemetry` here.

## Admin-only settings

These settings live in the admin DB and are edited through the Go WebUI. They
are not part of standalone `config.yaml` and are not sent to data-plane
gateways through config-sync.

- `gateway.public_url`: optional client-facing gateway root URL, normally the
  LB or Ingress address in front of one or more data-plane nodes. It must be
  an absolute `http` or `https` root URL without a path, query, fragment, or
  credentials (for example, `https://ai.example.com`). It is metadata for
  control-plane connection guidance; it does not bind a listener, route a
  request, or identify an individual node.
- `obs_<signal>_retention_days`: retention for the embedded Observe SQLite
  store (`logs` default `7`, `metrics` `30`, `traces` `3`). Changes take
  effect when the server restarts.
- `--observe-data-dir`: directory containing `observe.db`; default
  `~/.nyro/data`.
- `--state-data-dir`: directory containing `state.db`; default
  `~/.nyro/data`.
- `--otlp-listen` and `--redis-listen`: loopback listeners for the embedded
  OTLP/HTTP and Redis-compatible servers. Disable them explicitly with
  `--disable-otlp` and `--disable-redis`.
- Serving the embedded Redis-compatible endpoint and selecting a gateway
  `settings.state` backend are separate decisions. Starting `nyro serve` does
  not implicitly rewrite or select `state.url`; configure the embedded
  listener's reachable URL explicitly when a proxy should use it.
- The embedded Redis listener defaults to loopback. A non-loopback
  `--redis-listen` requires `--redis-password` and still carries credentials
  and State traffic without TLS. Restrict it to loopback or a trusted private
  network, or use an independently encrypted tunnel.
