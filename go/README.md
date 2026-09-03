# Nyro Gateway (Go)

Go implementation of the Nyro AI protocol gateway. It is being developed
alongside the Rust implementation, which remains available until the Go
cutover is complete.

> **Decision record — why not Ollama as a base?** Ollama was evaluated and
> **rejected**. It is a *local inference engine* (≈90% of its code is inference
> — scheduler, llama.cpp subprocess, GGUF/model management), structurally the
> inverse of a *gateway*. It lacks Gemini, multi-upstream routing, per-request
> key selection, the admin/control plane, the trusted request pipeline
> framework, OAuth-for-upstream-credentials, and multi-backend storage. We
> therefore **port Nyro's own Rust architecture** and use Ollama's
> `openai/` + `anthropic/` converters only as a *wire-format reference*
> (streaming SSE state machines, tool-call normalization, thinking blocks).
> Gemini has no Ollama equivalent and is ported from Nyro's Rust codec.

## Layout

| Path | Responsibility |
|---|---|
| `internal/kernel/` | workload-neutral lifecycle graph, atomic typed generations, request leases, readiness, and status |
| `internal/llm/` | Canonical LLM IR for chat and embedding workloads |
| `internal/llm/ingress/http/` | LLM HTTP routing, ingress decoding, response encoding, and streaming sink |
| `internal/llm/pipeline/` | fixed trusted phase order, terminal delivery hook, and reverse finalizers |
| `internal/llm/protocol/` | endpoint identity, ingress/egress codec contracts, and immutable protocol catalog |
| `internal/llm/provider/` | immutable provider catalog, transport-neutral drivers, definitions, and generic fallback |
| `internal/llm/provider/httptransport/` | outbound HTTP transport, proxy handling, and connection policy |
| `internal/llm/routing/` | weighted, priority, cooldown, and latency target selection plus health state |
| `internal/llm/runtime/` | trusted LLM orchestration, retry/failover, errors, streaming, and client delivery |
| `internal/transport/httpserver/` | generic process-level HTTP server, health/readiness, recovery, and shutdown |
| `internal/config/` | standalone YAML parsing, defaults, validation, and Snapshot construction |
| `internal/config/snapshot/` | immutable runtime configuration and deterministic fingerprinting |
| `internal/configsync/` | authenticated full-Snapshot distribution and hot-update source |
| `internal/security/` | transport-neutral authentication and authorization contracts |
| `internal/quota/` | backend-neutral request/token windows and concurrency leases |
| `internal/telemetry/` | logs, metrics, traces, and request-pipeline observation |
| `internal/storage/` | control-plane storage contracts, migrations, and database/memory implementations |
| `internal/admin/` | management API for keys, quotas, models, routes, providers, settings, and observability |
| `internal/bootstrap/` | explicit catalogs, runtime candidate construction, reconciliation, and process wiring |
| `nyro.go`, `cmd/` | unified CLI: `nyro proxy`, `nyro serve`, and operator tools |

The implemented data-plane architecture is described in
[docs/design/architecture.md](docs/design/architecture.md).

Nyro-owned runtime infrastructure lives under `internal/platform/`:

| Path | Responsibility |
|---|---|
| `internal/platform/database/sqlite/` | caller-owned, pure-Go SQLite connection policy; no application schema or migration ownership |
| `internal/platform/state/` | binary-safe String/TTL semantic API, SQLite persistence, and a limited RESP2/RESP3 Redis-compatible server |
| `internal/platform/observe/` | lossless OTLP batch persistence, indexed SQLite queries, retention, and an OTLP/HTTP protobuf receiver |

State and Observe remain separate internal platform packages assembled by
`nyro serve`; they use dedicated `state.db` and `observe.db` files when
embedded.

## Library mapping (Rust → Go)

| Rust | Go | Notes |
|---|---|---|
| axum | `net/http` + **Chi** (`github.com/go-chi/chi/v5`) | generic server infrastructure plus explicitly mounted LLM, Admin, and WebUI handlers |
| tokio | goroutines + channels | native |
| reqwest | `net/http` | |
| sqlx (sqlite/pg/mysql) | **GORM** (`gorm.io/gorm`) + sqlite/postgres drivers | Go models and explicit migration logic are the schema sources of truth; automatic migration is opt-in |
| serde | `encoding/json` + struct tags | |
| tracing | `log/slog` | stdlib |
| `inventory` | explicit constructors + immutable catalogs | Protocol codecs and provider drivers are enumerated by Bootstrap; imports have no Nyro registration side effects |

**SQLite without CGO:** GORM's default sqlite driver (`gorm.io/driver/sqlite`)
pulls `mattn/go-sqlite3` (CGO). To keep the build pure-Go (no C toolchain,
clean cross-compile), use `github.com/glebarez/sqlite` — a GORM driver backed
by `modernc.org/sqlite`. Reusable infra modules use the same pure-Go
`modernc.org/sqlite` driver directly through `database/sql` and do not depend
on GORM.

## Build & run

```bash
go build ./...
go run . serve --auto-migrate       # everything: control plane + embedded data plane
go test ./...
go vet ./...
```

`nyro serve` alone is a complete, usable nyro: the management API and WebUI on
`127.0.0.1:19531`, an embedded data plane on `127.0.0.1:19530`, a
Redis-compatible State Engine on `127.0.0.1:16379`, and an OTLP/HTTP Observe
Engine on `127.0.0.1:14318`. Local databases default to
`~/.nyro/data/{config,state,observe}.db`. The
embedded data plane subscribes to config over an in-process pipe — the same
ConfigSync client used by a remote node feeds snapshots through the Reconciler,
Kernel Host generation, and trusted LLM Runtime. This preserves one activation
path without opening a config-sync TCP port for the embedded data plane.

The two roles can also be split, for horizontal scaling or to keep the node
holding credentials out of the traffic path:

```bash
go run . serve --disable-proxy --sync-listen 127.0.0.1:19532    # control plane only
go run . proxy --server 127.0.0.1:19532                         # extra data plane node
go run . proxy --config ./nyro.yaml                             # standalone: no server, no DB
```

Quota State defaults to process-local memory. To share request, token, and
concurrency quota state across gateway replicas, configure the same reachable
Redis URL on every node (directly in standalone YAML or through config-sync):

```yaml
settings:
  state:
    type: redis
    url: "${NYRO_REDIS_URL}"
```

Only `redis://` URLs are currently supported; they may include username,
password, and database. Because the connection is plaintext, use loopback, a
trusted private network, or an independently encrypted tunnel. External Redis
must be version 7.0 or newer. Startup probes the
request, token, and concurrency quota paths before installing the backend;
the Redis account must permit the commands documented in the standalone
configuration reference. An unavailable or incapable standalone backend fails
startup. A failed ConfigSync hot update is rejected while the last-known-good
backend continues serving; later pushed snapshots remain eligible for
activation.

Request quotas are admitted atomically across replicas that share Redis. A
clean `429` denial does not consume quota, while an admitted request counts
even if its upstream exchange fails. Token quotas remain response-settled, so
concurrent requests can exceed a strict token boundary before usage arrives.
Extreme Redis `WATCH` contention can fail one request closed with `503`
without marking State unhealthy. The embedded Redis-compatible listener is
separately controlled by `nyro serve` flags and is intentionally limited to
Nyro's State needs. See
[standalone configuration](docs/schema/config.md) and
[embedded infrastructure databases](docs/schema/database.md).

Off-host config-sync needs a join token (`--sync-token`) or mTLS — nyro refuses
to serve upstream credentials over an unauthenticated plaintext network port.
See [config-sync transport and mTLS](docs/security/config-sync-mtls.md).

## Go WebUI

The Go admin control plane has its own management console at `go/webui/`,
targeting the Go admin API schema directly (upstreams, routes, consumers,
settings, logs, stats). It is a separate app from the root `webui/` (Rust
implementation, kept running in parallel until cutover — see
`go/docs/cutover.md`).

**Dev workflow** — run the frontend against a live admin API with hot reload:

```bash
cd go/webui
npm install
npm run lint
npm run build
cd ..
go run . serve --webui-dir ./webui/dist
```

**Release-embedding workflow** — bake the built assets into the `nyro`
binary so no external `--webui-dir` is needed at deploy time:

```bash
cd go/webui && pnpm install && pnpm run build # produces go/webui/dist
cd ..
rm -rf internal/webui/dist
mkdir -p internal/webui/dist
cp -R webui/dist/. internal/webui/dist/
go build -tags webui_embed -o bin/nyro .
```

The embed build tag is `webui_embed` (see `internal/webui/embed_enabled.go`
and `internal/webui/embed_disabled.go`). The equivalent `make` targets are
`go-webui-build`, `go-webui-embed-assets`, `go-webui-embed-build`, and
`go-webui-embed-run` (see the repo-root `Makefile`).
