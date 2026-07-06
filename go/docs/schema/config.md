# Standalone `config.yaml`

The user-facing configuration for the standalone Go gateway. `${VAR}` references
anywhere in the file are expanded from the process environment before parsing.

## Full Example

```yaml
version: 1

settings:
  server:
    listen: "127.0.0.1:19530"
    base_url: "http://127.0.0.1:19530"

  proxy:
    request_timeout: "120s"
    connect_timeout: "30s"
    max_retries: 2
    retry_on_status: [429, 500, 502, 503, 504]

  observability:
    logs:
      exporter: "stdout"
    metrics:
      exporter: "prometheus"
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
    keys:
      - name: "primary"
        api_key: "${NYRO_API_KEY}"   # empty = auto-generate
        enabled: true
        expires_at: null
    routes:
      - "gpt-4o"
    quotas:
      requests:
        - limit: 60
          window: "1m"
        - limit: 10000
          window: "1d"
      tokens:
        - limit: 100000
          window: "1m"
      concurrency:
        max_requests: 10
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

## Field Reference

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
  - `keys[]`: `name`, `api_key` (empty = auto-generate), `enabled`, `expires_at`.
  - `routes[]`: granted client-visible route model names.
  - `quotas`: `requests[]` / `tokens[]` (`limit` + `window`) and
    `concurrency.max_requests`.
