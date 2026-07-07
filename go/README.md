# Nyro Gateway (Go)

Go rewrite of the Nyro AI protocol gateway core (`crates/nyro-core`). This is
the target architecture; the Rust implementation keeps running in parallel
until parity is reached (P0–P6 migration plan).

> **Decision record — why not Ollama as a base?** Ollama was evaluated and
> **rejected**. It is a *local inference engine* (≈90% of its code is inference
> — scheduler, llama.cpp subprocess, GGUF/model management), structurally the
> inverse of a *gateway*. It lacks Gemini, multi-upstream routing, per-request
> key selection, the admin/control plane, the request-lifecycle/plugin
> framework, OAuth-for-upstream-credentials, and multi-backend storage. We
> therefore **port Nyro's own Rust architecture** and use Ollama's
> `openai/` + `anthropic/` converters only as a *wire-format reference*
> (streaming SSE state machines, tool-call normalization, thinking blocks).
> Gemini has no Ollama equivalent and is ported from Nyro's Rust codec.

## Layout

| Path | Rust source | Responsibility |
|---|---|---|
| `internal/config/` | `config.rs` | bootstrap config only (live config lives in storage) |
| `internal/protocol/` | `protocol/` | canonical IR (`AiRequest`/`AiResponse`/`AiStreamDelta`), six codec interfaces, endpoint registry, negotiation (Native/Transform) |
| `internal/provider/` | `provider/` | `Vendor` interface, 7-step build/parse pipeline, vendor registry |
| `internal/proxy/` | `proxy/` | single `dispatch_pipeline`, ingress shells, streaming dual-path (passthrough + IR round-trip) |
| `internal/router/` | `router/` | model matching, `Model`→`ModelBackend` fan-out, selectors, health |
| `internal/plugin/` | `plugin/` | five-phase lifecycle (`OnRequest`/`OnAccess`/`OnUpstream`/`OnResponse`/`OnLog`) |
| `internal/admin/` | `admin/` | control plane: keys+quotas, models/routing, providers, OAuth, logs/stats, import/export |
| `internal/auth/` | `auth/` + `admin/oauth.rs` | inbound API key + quotas; outbound OAuth drivers (Claude/Codex/Vertex) |
| `internal/storage/` | `db/` + `storage/` | `Storage` interface, idempotent migrations, sqlite/pg/mysql/memory |
| `internal/logging/` | `logging/` | async request-log collector + retention |
| `nyro.go` | `src-server/` + `crates/nyro-tools/` | unified CLI: `nyro gateway` (data plane) / `nyro admin` (control plane) / `nyro tool` (utilities) |

## Library mapping (Rust → Go)

| Rust | Go | Notes |
|---|---|---|
| axum | **Gin** (`github.com/gin-gonic/gin`) | full-featured router/middleware; Ollama's `middleware/` is Gin, so a direct cross-reference for protocol-conversion patterns |
| tokio | goroutines + channels | native |
| reqwest | `net/http` | |
| sqlx (sqlite/pg/mysql) | **GORM** (`gorm.io/gorm`) + per-backend drivers | **AutoMigrate OFF** — schema source-of-truth is the migration layer (`deploy/schema/*.sql`); GORM is used for CRUD/struct mapping only |
| serde | `encoding/json` + struct tags | |
| tracing | `log/slog` | stdlib |
| `inventory` | `init()` + global registry | Go has no link-time registration; codecs/vendors/hooks register at init |

**SQLite without CGO:** GORM's default sqlite driver (`gorm.io/driver/sqlite`)
pulls `mattn/go-sqlite3` (CGO). To keep the build pure-Go (no C toolchain,
clean cross-compile), use `github.com/glebarez/sqlite` — a GORM driver backed
by `modernc.org/sqlite`. Added with the storage layer in P3.

## Build & run

```bash
go build ./...
go run . gateway                    # data plane, listens on 127.0.0.1:19530
go test ./...
go vet ./...
```

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
go run . admin --webui-dir ./webui/dist
```

**Release-embedding workflow** — bake the built assets into the `nyro`
binary so no external `--webui-dir` is needed at deploy time:

```bash
cd go/webui && npm install && npm run build   # produces go/webui/dist
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
