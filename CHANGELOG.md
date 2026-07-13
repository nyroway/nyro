# Changelog

All notable changes to Nyro will be documented in this file.

---

## v1.8.3

> Released on 2026-07-13

#### Features

- **Request lifecycle P0 scaffolding** (#227): deliver request-lifecycle P0 scaffolding with `RequestContext` end-to-end and typed extension bag
- **Request lifecycle & extension framework (P0 + P1)** (#229): add the request lifecycle and extension framework with admin extensions API and `PluginKernel` aggregation
- **OnResponse/OnLog lifecycle phases (P2 A1-A3)** (#230): wire `OnResponse` and `OnLog` lifecycle phases into the dispatch pipeline
- **API key-level observability** (#237): add API key-level observability to the admin UI and fix cache hit rate reporting
- **Logging & stats enhancements** (#239): improve logging and stats surfaces plus fix metrics-calculation issues

#### Improvements / Refactoring

- **Architecture documentation** (#232): refresh `architecture.md` to align with the current codebase
- **WebUI extensions page** (#260): remove the read-only extensions page that had no user-facing controls

#### Fixes

- **Anthropic to OpenAI function calling** (#233): fix function calling failures when converting Anthropic requests to OpenAI
- **Log page provider filter** (#234): fix the log page provider filter showing empty logs

---

## v1.8.2

> Released on 2026-06-18

#### Fixes

- **Codec extra field passthrough** (#225): skip cross-protocol internal keys when passing through the extra field
- **Access control toggle copy** (#224): clarify the access control toggle copy in the WebUI to describe the current state
- **WebUI build on pnpm 10+** (#223): allow the esbuild build script for pnpm 10+ and bump CI pnpm to 11 (Node 22)

---

## v1.8.1

> Released on 2026-06-15

#### Features

- **Server deployment modes** (#206): add `--mode` flag and `embed-webui` cargo feature for server distributed deployment
- **K8S external database migration** (#207): support K8S external database migration workflow for production deployments
- **Clear all logs** (#216): add clear all request logs functionality in the WebUI

#### Improvements / Refactoring

- **Makefile build tooling** (#210, #213): add `build nyro-tools` and `cargo test` commands to Makefile
- **Clippy warnings cleanup** (#218): fix all clippy warnings across nyro-core and nyro-server
- **Release documentation** (#219): add local release runbook and AGENTS release process documentation

#### Fixes

- **Gemini from_uri content** (#205): fix `from_uri` message content being skipped during Gemini processing
- **OpenAI responses tool call parsing** (#212): fix tool call done event parsing in OpenAI responses API
- **Desktop execute tests** (#214): fix import error in desktop execute tests
- **Anthropic inline system messages** (#217): support inline `system` role messages from Claude Code >=2.1.154

---

## v1.8.0

> Released on 2026-06-05

#### Features

- **MySQL storage backend** (#196): add complete MySQL storage implementation with infrastructure, config, connection pool, and documentation
- **Multi-replica production readiness** (#203): add config epoch sync, health probes, and webui-dir support for multi-replica deployments
- **Per-model payload logging control** (#195): add per-model payload logging toggle and unify `enable_*` naming convention
- **Graceful shutdown** (#192): add graceful shutdown handling for the server
- **Gemini API max request body** (#188): add configurable maximum request body setting for Gemini API proxy

#### Improvements / Refactoring

- **AI Gateway terminology unification** (#190): rename `routes` to `models` across the codebase for standard AI gateway terminology
- **Request logs field renaming** (#191): rename `route_id`/`route_name` → `model_id`/`model_name` and `strategy` → `balance` in `request_logs`
- **Virtual model field merge** (#194): merge `virtual_model` into the `name` field, simplifying the data model
- **Response cache removal** (#187): remove exact match and semantic similarity response cache modules
- **Unused crypto module cleanup** (#198): remove unused AES-256-GCM crypto module and stale sqlite-vec CI step
- **Unused dependency cleanup** (#199): remove 8 unused Cargo dependencies
- **WebUI model label style** (#197): use code label style for model name in model list

#### Fixes

- **Gemini proxy auth and streaming** (#186): fix native Gemini proxy authentication and streaming behavior
- **MySQL SUM type compatibility** (#201): cast MySQL `SUM()` results to `SIGNED` for proper i64 mapping in Rust
- **MySQL AVG type compatibility** (#202): cast MySQL `AVG()` results to `DOUBLE` for proper f64 mapping in Rust
- **Storage E2E MySQL backend** (#200): sync storage E2E test harness with renamed struct fields and add MySQL backend

---

## v1.7.6

> Released on 2026-05-22

#### Features

- **Vertex AI provider support** (#172): add Vertex AI as a built-in provider and fix Gemini ingress authentication handling
- **Provider copy workflow** (#173): allow duplicating an existing provider configuration with a single action
- **Route target inheritance in provider copy** (#178): automatically append route target associations when copying a provider
- **Client cache hint forwarding** (#181): forward `anthropic-beta` cache-control hints from the client request to the upstream provider without leaking client identity headers
- **Upstream error evidence preservation** (#184): capture and surface upstream response body and status code when proxy forwarding fails, improving debugging visibility
- **Gemini stream JSON handling** (#182): handle non-streaming JSON responses returned by Gemini stream endpoints, parsing them into the unified IR stream pipeline

#### Improvements / Refactoring

- **Protocol slug unification** (#174): replace opaque short codes with descriptive canonical Protocol identifiers across the codebase
- **GoogleGenerativeAI → GoogleGemini rename** (#175): rename the protocol enum variant and unify all Google/Gemini endpoint constants
- **Codec directory restructuring** (#176): reorganize the codec module into a `vendor/endpoint` directory layout aligned with protocol boundaries
- **Admin modularization** (#180): split monolithic admin handler into focused sub-modules for cleaner separation of concerns

#### Fixes

- **Storage E2E model sync** (#167): align storage E2E test harness with current model definitions
- **Storage E2E auth mode** (#168): use valid `apikey` auth_mode in storage E2E harness
- **Storage E2E token check** (#169): remove unreliable `total_output_tokens` assertion from storage E2E tests
- **Postgres api_keys INSERT** (#170): remove stale `status` column from the api_keys INSERT statement
- **Postgres AVG() f64 compatibility** (#171): cast `AVG()` results to `FLOAT8` for proper f64 mapping in Rust

---

## v1.7.5

> Released on 2026-05-19

#### Fixes

- **Ingress decode failure logging** (#164): record request-decode failures from Anthropic Messages, OpenAI-compatible Chat Completions / Embeddings / Responses, and Google Generate Content ingress handlers in the in-app log module with request metadata and 400 responses
- **Anthropic context management beta compatibility** (#165): accept the `context_management` request shape sent by the Anthropic `context-management-2025-06-27` beta, preserving it as pass-through JSON instead of rejecting requests that omit the obsolete outer `type` field

---

## v1.7.4

> Released on 2026-05-19

#### Improvements / Refactoring

- **Provider configuration simplification** (#160): collapse provider storage and UI from protocol endpoint mappings to a single `protocol` / `base_url` pair, with migration support for legacy rows and aligned standalone config docs/tests

#### Fixes

- **OpenAI-compatible streaming completion** (#162): deduplicate terminal `Done` events when both `finish_reason` and `[DONE]` are observed, preventing duplicate end-of-stream notifications

---

## v1.7.3

> Released on 2026-05-18

#### Features

- **IR and protocol codec pipeline overhaul** (#145–#153): reshape the internal request/response representation around `AiRequest`, `AiResponse`, `AiStreamDelta`, expanded `ContentBlock`, `AiError`, `CacheControl`, and `ProtocolExt`; switch ingress decoders, egress encoders, response/stream parsers, dispatcher, provider adapters, and cache flow to consume the new IR directly; remove the legacy `InternalRequest` / `InternalResponse` path and align codec traits on the `Decoder` / `Encoder` vocabulary
- **Request log metadata enrichment** (#154): persist `provider_name`, `api_key_name`, `route_id`, and `route_name` alongside request logs, and restore `is_stream` capture for stream/non-stream visibility
- **Prompt-cache usage accounting** (#156): capture chat-completions prompt-cache hit tokens so cache-read usage is preserved in downstream accounting

#### Fixes

- **Protocol conversion thinking preservation** (#157): bridge Anthropic thinking blocks into OpenAI-compatible `reasoning_content`
- **OpenAI Responses streaming usage** (#140): preserve `input_tokens` across streaming usage deltas
- **WebUI log detail loading** (#139): map `backend("get_log")` to `GET /api/v1/logs/:id`
- **Provider icons** (#133, #134): fix undefined provider icons in add/edit flows and correctly display empty custom icons
- **macOS app lifecycle** (#132): reopen the main window when the Dock icon is clicked
- **musl build warnings** (#130): silence dead-code warnings on musl builds

#### Refactoring / Internal

- Restructure proxy ingress code into protocol-specific subdirectories (#135)
- Replace `capabilities_source` string handling with typed `CapabilitiesSource` presets (#136)
- Remove `route_type` and endpoint subset filtering from route handling (#137)
- Split dispatcher internals with `CallCtx`, `CacheWriteCtx`, `RequestExtras`, `LogBuilder`, integrations hooks, routing strategies, and module renames (#141–#143)
- Add the IR field-homing design skeleton and follow-up deprecation/test/doc cleanup (#138, #144)

---

## v1.7.2

> Released on 2026-05-12

#### Fixes

- **musl static build: eliminate OpenSSL dependency** (#125): add `default-features = false` to the workspace `reqwest` dependency and switch to `rustls-tls-native-roots`; this removes the `default-tls` feature that was silently pulling `native-tls` → `openssl-sys` into the build graph, which caused the `*-unknown-linux-musl` CI jobs to fail; `http2`, `charset`, and `macos-system-configuration` are retained explicitly to avoid regressions; TLS engine remains `rustls` on all platforms while native certificate stores (Windows Cert Store / macOS Keychain / Linux `/etc/ssl/certs`) continue to be used on non-musl targets
- **musl static build: sqlite-vec BSD type compatibility** (#125): inject `CFLAGS_<target>=-Du_int8_t=uint8_t -Du_int16_t=uint16_t -Du_int64_t=uint64_t` in CI for musl targets so that `sqlite-vec v0.1.9`'s C source (which uses POSIX `u_int*_t` types absent from musl libc) compiles correctly via cc-rs's target-specific CFLAGS lookup

---

## v1.7.1

> Released on 2026-05-12

#### Features

- **musl static build for Linux** (#123): add `x86_64-unknown-linux-musl` and `aarch64-unknown-linux-musl` release targets; switch sqlx to `tls-rustls` to eliminate the OpenSSL runtime dependency; add `cfg(target_env = "musl")` branch in `crypto/mod.rs` for master-key resolution via env var / file path fallback (avoids dbus/libsecret static-link issue)

#### Fixes

- **ARM Linux sqlite-vec extension ABI** (#121): use platform-native `c_char` / `c_int` types in the `sqlite3_auto_extension` registration call so the symbol signature matches libsqlite3-sys on aarch64 Linux

#### Internal

- Apply `rustfmt` across the entire codebase; add `make fmt` / `make fmt-check` targets to `Makefile` (#124)

---

## v1.7.0

> Released on 2026-05-12

#### Features

- **System tray lifecycle fix** (#118): prevent app exit on window close — hide to tray instead; fix `TrayIcon` drop bug by managing lifetime via `app.manage()`; left-click tray icon restores window
- **nyro-tools proxy subcommand rewrite** (#111): replace `--upstream-protocol` + `--upstream-endpoint` with single `--url` (`-u`); auto-detect and strip client version prefix from egress URL; restrict forwarding to known LLM ingress paths; add structured JSON logging with UUID correlation id and protocol-aware SSE assembly for all four protocols; add `-o/--output` for log file output and `-l/--log-mode` (all|req|resp) filter
- **Claude Code OAuth on latest architecture** (#101): new `auth/drivers/claude.rs` PKCE driver registered through vendor-registry pipeline; add `anthropic/claude-code` channel and extension with OAuth-owned auth headers; introduce `compose_upstream_headers` helper centralizing the "OAuth driver suppresses default auth" invariant across all four upstream call sites; pin invariant with regression tests
- **OAuth provider flow** (#58): add full OAuth credential support, Codex OAuth channel, and runtime wiring into proxy and Tauri
- **Three-layer CI testing pyramid** (#84): Phase 1 — unit tests for protocol transformers (tool-call fragments, Anthropic thinking deltas, DeepSeek reasoning, Responses output items, tool correlation); Phase 2 — build artifact job + L3 Ollama E2E (7 links); Phase 3 — L2 aimock static E2E with 4 isolated instances (8 fixtures); Phase 4 — migrate smoke tests to `tests/e2e/`, add `storage-backends.yml` (pgvector daily schedule)
- **Protocol / ProtocolEndpoint / Vendor three-concept model** (#89–#97, #119): replace the ambiguous `ProtocolFamily` with a clean orthogonal identity system — `Protocol` (enum: `OpenAICompatible` / `OpenAIResponses` / `AnthropicMessages` / `GoogleGenerativeAI`) for wire-format suite, `ProtocolEndpoint` (`{protocol, name, version}`) for specific API endpoint, `Vendor` via existing `Provider.vendor`; canonical short names (`openai-compat`, `openai-resps`, `anthropic-msgs`, `google-genai`) in config/JSON; three-tier alias table for full backward compatibility (old canonical strings, legacy brand names `openai`/`claude`/`gemini`, short aliases) with no data migration; `protocol_endpoints` JSON upgraded to protocol-keyed format (`base_url` at protocol level, optional `endpoints` subset array) with automatic migration on first read via `normalize_endpoints_json`
- **Upstream response headers logging**: `call_non_stream` now returns `(Value, u16, HeaderMap)` preserving headers before `.json()` consumes the response; all three proxy paths (JSON, SSE stream, force-stream) capture upstream response headers and persist to `response_headers` in the request log
- **Root health endpoint**: `GET /` and `HEAD /` return `{"status":"ok"}`, enabling load-balancer and Kubernetes liveness probes that default to `HEAD /`

#### Refactoring

- **Provider layer overhaul** (#107): merge `ProviderAdapter` + `VendorExtension` into unified `Vendor` trait via `VendorRegistry`; activate PassThrough fast-path through `negotiate()` to skip IR codec round-trip when ingress == egress protocol; `dispatch_pipeline` takes `RawEnvelope` + `AiRequest` at surface; `dispatcher.rs` split into `mod.rs` + `util.rs` + `accumulator.rs`; `Gateway` runtime fields migrated from `RwLock` to `ArcSwap` eliminating hot-path `.await`; CODEC_SCHEMA_VERSION bumped to 2
- **Kernel stabilization** (#104): unified `GatewayError` taxonomy (15 variants, stable codes); `RequestContext` lifecycle tracking; observability and security split out of `handler.rs` into dedicated modules; single-orchestration `dispatcher.rs` replacing prior multi-file handler split
- **OAuth credential storage** (#82): split credentials into dedicated `provider_oauth_credentials` table with CAS state machine (`connected` / `refreshing` / `error`) and optimistic lock (`status_version`); `OAuthCredentialStore` trait with 8 methods implemented for SQLite, PostgreSQL, and Memory; remove `access_token` / `refresh_token` / `expires_at` from `Provider` struct; auto-migrate existing OAuth data from `providers` table on startup; background refresh now uses `list_expiring()` + CAS
- **Codec directories restructured by protocol**: old `codec/openai/`, `codec/anthropic/`, `codec/google/` removed; replaced by fully self-contained `codec/openai_compatible/`, `codec/openai_responses/`, `codec/anthropic_messages/`, `codec/google_generative/`
- **Trait and type renames**: `ProtocolHandler` → `EndpointHandler`; `ProtocolCapabilities` → `EndpointCapabilities`; `ProtocolRegistration` → `EndpointRegistration`; `list_by_family` → `list_by_protocol`; backward-compat `pub use` aliases retained; `ProtocolFamily` and `VendorScope::Family` removed
- **authMode normalization** (#73): rename preset JSON field `auth_mode` → `authMode`, narrow value `"api_key"` → `"apikey"` across JSON, DB, Rust and TypeScript; add SQLite/Postgres startup migration; reshape provider create/edit OAuth panel layout
- **`protocol-id.ts` replaced by `protocol.ts`**: new `PROTOCOL_TABLE`, `PROTOCOL_ALIASES`, `resolveProtocol`, `parseProtocolEndpoint`; `prettyName` returns Protocol display name only; Providers/Connect/Routes pages aligned to canonical IDs

#### Fixes

- Preserve thinking metadata across protocol conversion (#114)
- Preserve full Anthropic usage fields; enable native passthrough for ZhipuAI and MiniMax (#115)
- Fix stream passthrough error propagation and `RawEvent` forwarding (#112)
- `passthrough_run` now substitutes virtual model alias with `actual_model` (#109)
- Preserve `reasoning_content` through proxy for DeepSeek thinking mode (#98)
- Correct URL and auth header composition for OpenAI-compat vendors on Anthropic egress (#105)
- Handle mlx-lm `reasoning` field name; include `reasoning_content` in non-streaming responses (#103)
- Preserve Anthropic Thinking blocks through gateway (#90)
- Fix dark mode text contrast for `text-slate-800` (#100)
- Fix missing lock file and directory in runtime Docker build (#71)

---

## v1.6.2

> Released on 2026-04-19

#### Features

- **Request/response payload logging**: extend `request_logs` schema with `method`, `path`, `request_headers`, `request_body`, `response_headers`, `response_body` (non-breaking migrations for SQLite + Postgres via `ensure_request_log_column` / `ALTER TABLE IF NOT EXISTS`); capture ingress payloads across universal, Gemini and embeddings proxy entrypoints; aggregate streaming responses into a final JSON and persist as `response_body`; emit logs on all early-exit paths (decode failure, no route, auth failure, upstream error, cache-miss fallbacks) with full context; cache-hit paths now carry complete request/response bodies; embeddings proxy parses `usage.prompt_tokens` into `input_tokens`
- **Redesigned log viewer**: compact 7-column list (Time / Status / Model / Protocol / Latency / Token / Type) with left-aligned rows and click-to-open detail dialog; new `LogDetailDialog` with meta header and four copy-enabled payload blocks (request headers/body, response headers/body) using lazy `get_log(id)` fetch and pretty-printed JSON; Token displayed with IN/OUT labels and K/M formatting (<1000 raw, <1M one-decimal K, ≥1M two-decimal M); green SSE / sky JSON type badges replace the boolean stream column; Settings page splits Log Configuration into its own half-width card next to Proxy Configuration with HelpCircle tooltips
- **Log payload persistence toggle**: new `log_record_payloads` setting (default `true`) to disable request/response body storage for sensitive-data deployments

#### Improvements

- **Standalone provider config ergonomics**: `default_protocol` is now optional and auto-inferred from the first declared `endpoints` entry when omitted; add aliases `protocol` (for `default_protocol`) and `apikey` (for `api_key`); switch `endpoints` to `IndexMap` to preserve YAML declaration order; reject conflicting canonical + alias pairs (`default_protocol` + `protocol`, `api_key` + `apikey`) at deserialization time via `YamlProviderRaw` + `TryFrom`; emit a WARN log when protocol is inferred from multiple endpoints
- **Log retention defaults rebalanced**: `DEFAULT_RETENTION_DAYS` 30 → 7, batch size 64 → 32, cleanup interval 60s → 600s to reduce storage growth and cleanup churn
- **Split log API**: `query_logs` list now strips heavy fields (NULL bodies/headers); new `get_log(id)` endpoint fetches the full payload on demand

#### Fixes

- Fix `release-server` workflow missing `webui/dist` at compile time: `#[derive(RustEmbed)]` expansion failed because `WebUiAssets::get` was absent; add Node 20 + pnpm 9 setup and run `pnpm -C webui install/build` before `cargo build`

---

## v1.6.1

> Released on 2026-04-14

#### Features

- **Stream replay TPS throttle**: add `stream_replay_tps` (default 100) to `ExactCacheConfig` and `SemanticCacheConfig`; set to `0` to disable throttle and restore instant-flush behavior; implement `split_text_deltas` helper to chunk large `TextDelta`/`ReasoningDelta` into ~1-token pieces for smooth per-token pacing; first SSE chunk is always sent immediately to keep TTFT at zero
- **Per-cache response header control**: add `expose_headers` (default `true`) to both cache configs, independently controlling whether `X-NYRO-CACHE-*` headers are emitted per exact vs semantic cache; rename response headers to uppercase: `X-NYRO-CACHE` / `X-NYRO-CACHE-KEY` / `X-NYRO-CACHE-SCORE`
- **Embedded WebUI in server binary**: remove `--webui-dir` CLI param; embed `webui/dist` into the binary via `rust-embed`; add `--log-level` param (env `NYRO_LOG_LEVEL`) to replace hardcoded tracing filter; add env-var support for key params (`NYRO_PROXY_HOST`, `NYRO_ADMIN_TOKEN`, etc.)
- **Browser token authentication**: add `/login` page and token auth flow for browser-based WebUI access; add logout icon in web topbar when admin token is active (Tauri IPC path is unaffected)
- **Resource enable/disable toggles**: add enable/disable toggle buttons in WebUI list pages for providers, routes, and API keys; show danger badge only when resource is disabled

#### Improvements

- **Server CLI simplification**: reduce CLI params from 27 to 18; rename `--admin-key` → `--admin-token`, `--storage-dsn-env` → `--postgres-dsn`; prefix Postgres pool params with `postgres-*`; rename `--sqlite-migrate-on-start` → `--migrate-on-start`; remove 9 cache CLI params (now managed via Admin API / WebUI + DB)
- **Status field unification**: rename `providers.is_active`, `routes.is_active`, `api_keys.status` to `is_enabled` (BOOLEAN) across all storage backends, SQL queries, Rust models, and WebUI; add non-breaking schema migration for both SQLite and PostgreSQL

#### Fixes

- Fix missing `stream_replay_tps` and `expose_headers` fields in nyro-server cache config initializers (`main.rs` and `yaml_config.rs`) causing a compile error after feat #45
- Fix standalone mode proxy host/port priority bug where CLI value was silently overridden by the default
- Fix `backend.ts` null-data bug where `json.data ?? json` returned the full response object when `data` was `null`, causing `.trim()` crashes on Providers and Settings pages

#### Documentation

- Consolidate 8 stale design docs into a single `docs/design/architecture.md`
- Add `docs/standalone/` with full Standalone mode guide including cache section
- Remove `examples/` directory (content inlined into standalone docs)
- Fix stale CLI params across `docs/server/README.md`, `README.md`, and `README_CN.md`

---

## v1.6.0

> Released on 2026-04-12

#### Features

- **End-to-end cache system**: implement modular exact/semantic cache backends with SSE stream replay for streaming cache hits and singleflight request coalescing to prevent cache stampede under concurrent misses
- **Ingress route aliases**: add non-versioned route aliases (`/chat/completions`, `/messages`, `/responses`, `/models/:model_action`) alongside versioned paths for broader client compatibility
- **OpenAI-compatible models listing**: add `/v1/models` and `/models` endpoints returning route-aware model lists, with API-key-bound model filtering and graceful degradation
- **Semantic vector dimension lifecycle**: auto-rebuild semantic vector tables when embedding dimensions change, persist active dimensions in settings, and support transactional pgvector rebuild with clear permission fallback guidance

#### Improvements

- **Cache system unification**: unify exact/semantic cache runtime configuration and hot-reload behavior; enforce chat/embedding route type isolation with OpenAI endpoint validation; update WebUI route/settings flows accordingly
- **Settings save UX**: refactor settings modules to explicit save actions with unsaved-change feedback; split API key list status into management and validity badges; align SQLite semantic cache scoring with cosine distance expectations
- **Global cache/proxy linkage**: route list badges and form controls now reflect global cache/proxy enabled state; route form hides cache toggles and provider form hides proxy toggle when respective global setting is off; saved config is preserved and auto-restores on re-enable
- **Semantic cache config linkage**: clear `embedding_route` when semantic cache is toggled off; block deletion of an embedding route referenced by semantic cache config with an error dialog

#### Fixes

- Fix cache-hit log model names by persisting `actual_model` in cache entries so upstream models are reported consistently on cache hits
- Fix global cache/proxy toggles lacking proper linkage with route badges and provider proxy badge in list views
- Fix proxy backend returning 502 when global proxy is disabled but a route has `use_proxy=true`; fall back to direct HTTP client instead
- Standardize cache wording in UI and docs without changing existing config keys

#### Refactoring & Cleanup

- **Remove MySQL backend**: drop MySQL storage implementation, config/CLI paths, and sqlx mysql feature; supported backends are now SQLite / PostgreSQL / Memory
- **GitHub org rename**: update all references from `NYRO-WAY` to `nyroway` across configs, scripts, docs, install scripts, and frontend code
- Update Zai provider default capabilities source

---

## v1.5.0

> Released on 2026-04-02

#### Features

- **Storage backend expansion**: add multi-backend storage abstraction and server-side backend configuration support for SQLite / MySQL / PostgreSQL
- **Multi-target routing evolution**: add multi-target route selection and weighted/priority strategy flow, and support `weight=0` as an explicit disable state
- **Gateway protocol architecture refresh**: support multi-protocol providers, protocol-agnostic route behavior, and standalone YAML route/provider loading
- **Proxy extensibility upgrade**: extract `ProviderAdapter` and align provider-level proxy controls for cleaner provider integration

#### Improvements

- **Deprecated field cleanup**: remove legacy route/provider/log/storage fields and simplify schema/query paths around active routing behavior
- **Gateway error typing standardization**: unify proxy/auth error `type` payloads under `NYRO_*` naming for consistent client-side handling
- **CLI integration polish**: improve web CLI config preview and sync generated Claude Code settings with `CLAUDE_CODE_NO_FLICKER=1`
- **Repository migration alignment**: update project/release references to `NYRO-WAY` organization and align updater/release script paths
- **Build/runtime layout cleanup**: split Docker runtime image and dev container structure for clearer CI/CD maintenance

#### Tests & Docs

- Refresh smoke tests and docs to match protocol-agnostic routing and the latest route/provider data model

## v1.4.0

> Released on 2026-03-21

#### Features

- **Protocol normalization layer upgrade**: add semantic internal response normalization and emit item-level reasoning/function-call outputs for Responses API flows
- **Provider preset capability unification**: unify provider preset handling with capability source parsing and ship an embedded models.dev snapshot for offline metadata
- **Connect CLI workflow enhancements**: align Codex/OpenCode sync outputs with runtime defaults, refine route state anchoring, and improve config action UX

#### Improvements

- **WebUI configuration interactions**: refine provider preset behaviors and route-edit model interactions for more predictable admin flows
- **Admin error surface consistency**: return structured provider/route conflict payloads and localize conflict messages in the UI
- **CLI panel layout polish**: reorder API key vs update-config controls, keep half-width action layout, and align preview hint spacing/offset behavior
- **Local UX defaults**: default initial locale state to `en-US` and render request timestamps in local timezone in Logs

#### Fixes

- Fix cross-protocol tool-call semantics by hardening tool-call/result correlation and normalizing thinking/text delta behavior across adapters
- Fix Google model discovery auth path and model normalization in admin provider discovery flow

#### Tests & Docs

- Add protocol regression coverage for tool IDs, finish reasons, schema mapping, and provider-policy removal behavior
- Add protocol architecture hardening design doc and refresh README/UI screenshots for latest console pages

## v1.3.0

> Released on 2026-03-18

#### Features

- **OpenAI Responses pipeline support**: add request/response transformation path for `/v1/responses` to improve tool-chain compatibility with modern OpenAI-style clients
- **Provider model test workflow**: introduce staged provider testing with unified action feedback in provider management flows
- **Ollama capability detection**: add vendor-aware capability checks to auto-handle tool-support differences by model
- **Gemini cURL preview improvement**: preserve `:` in model IDs (for example `gemma3:1b`) when rendering Connect page Gemini endpoint snippets

#### Improvements

- **Provider UX consistency**: improve vendor/channel synchronization and keep route-edit state reset behavior predictable
- **Route model discovery behavior**: only enable discovery dropdown when provider model endpoint is actually available
- **Admin error handling UX**: localize backend error messages consistently and unify failure dialog presentation across admin pages

#### Fixes

- Fix MiniMax + Codex interoperability issue where upstream rejects `system` role by normalizing responses instructions for MiniMax on Responses API ingress
- Fix OpenRouter model discovery behavior and restore provider auto-test flow after provider create
- Fix Windows desktop dropdown/search selection regression caused by drag-capture conflict in Tauri title-drag handling

## v1.2.0

> Released on 2026-03-15

#### Features

- **New Connect module**: add `Connect` page with `Code` / `CLI` tabs, protocol-aware route selection, and generated examples for Python / TypeScript / cURL
- **Desktop CLI integration**: support readiness detection plus config sync/restore for Claude Code, Codex CLI, Gemini CLI, and OpenCode
- **CLI config preview & copy flow**: show per-file update fragments and inline copy action in preview area
- **API key policy upgrade**: enforce default-deny route authorization for protected routes and adopt `sk-<32 hex>` key format
- **Quota extension**: add `RPD` (requests per day) to API key model, admin CRUD, UI forms, and proxy enforcement

#### Improvements

- **API Keys page restructure**: split create/edit forms into three sections (Basic Information, Access Permission, Access Quota), align widths, and keep key/validity immutable in edit mode
- **Provider form polish**: add API key show/hide icon, restore API key echo in edit form, and align edit/create layout behavior
- **Route form consistency**: align edit layout with create layout and keep single-row inputs/selects at half width
- **Statistics time-range coverage**: make selected hours apply consistently to overview, model, and provider stats across WebUI + backend + Tauri commands

#### Fixes

- Fix `build-and-smoke` CI script for the new auth flow (remove deprecated `--proxy-key`, create/bind smoke API key to routes)
- Fix CLI sync argument mismatch (`toolId` / `apiKey`) and improve frontend error message parsing for failed sync operations
- Restore Codex `wire_api` compatibility by switching back to `responses`
- Improve dropdown/search panel visual consistency in forms and access-control layout details

#### CI & Release

- Automate Homebrew Cask checksum updates in desktop release workflow after artifacts are built
- Update release and design docs for latest route/API key behavior and installation guidance

---

## v1.1.0

> Released on 2026-03-13

#### Features

- **Route matching redesign**: switch from fuzzy `match_pattern` to exact matching on `(ingress_protocol, virtual_model)` for OpenAI / Anthropic / Gemini ingress
- **New API key system**: add `api_keys` + `api_key_routes` data model and full CRUD management with `sk-<32 hex>` key format
- **Route-level access control**: route first, then authorize API key only when `access_control` is enabled; support key binding to specific routes or global access
- **Quota enforcement for API keys**: add `RPM`, `TPM`, `TPD`, key status, and expiration checks in proxy authorization flow

#### Improvements

- **Backend migration & compatibility**:
  - add and backfill new route/provider/log fields (`ingress_protocol`, `virtual_model`, `access_control`, `channel`, `api_key_id`)
  - remove legacy route/provider fallback and priority behavior from active flow
- **Admin/API surface expansion**: add server + Tauri management endpoints/commands for API key operations
- **WebUI route & key experience refresh**:
  - new API Keys page with searchable multi-select route binding
  - create-route layout aligns provider/model fields in one row and auto-inherits target model into virtual model
  - provider create/edit flow now persists and re-anchors provider preset/channel identifiers
- **UI component standardization**: introduce shadcn-style `Badge`, `Switch`, `Checkbox`, `Dialog`, `Combobox`, `Command`, `Popover`, `MultiSelect`, `Tabs`, and related fields
- **Provider icon behavior polish**: provider list uses supplier icon as primary (light: color, dark: monochrome), protocol badge icon remains protocol-based
- **Version display automation**: settings page version now reads build-injected app version instead of hard-coded text

#### Fixes

- Fix searchable dropdown visual layering and non-transparent panel background
- Fix search result filtering and hover/highlight feedback in custom dropdown components
- Align Homebrew install docs to standard `brew install --cask nyro` flow

#### Documentation

- Add route/API key design spec: `docs/design/route-apikey.md`
- Add provider base URL/channel design note: `docs/design/provider-base-urls.md`
- Update `README.md` and `README_CN.md` installation commands and related notes

---

## v1.0.1

> Released on 2026-03-10

#### Improvements

- **Full ARM64 / aarch64 support**: native builds for all platforms using GitHub Actions ARM runners (`ubuntu-24.04-arm`, `windows-11-arm`, `macos-latest`)
  - Desktop: Linux aarch64 AppImage, Windows ARM64 NSIS installer
  - Server: Linux aarch64, macOS aarch64, Windows ARM64 binaries
- **macOS Intel native build**: use `macos-15-intel` runner instead of cross-compiling on ARM
- **Homebrew Cask**: `brew tap shuaijinchao/nyro && brew install --cask nyro` (separate `homebrew-nyro` tap repo with auto version bump on release)
- **Install scripts**: one-line install for macOS/Linux (`install.sh`) and Windows (`install.ps1`), with automatic quarantine removal on macOS
- **Frontend chunk splitting**: Vite `manualChunks` for react, query, and charts to eliminate >500kB bundle warning

#### Fixes

- **CI**: exclude `nyro-desktop` from `cargo check --workspace` to avoid GTK dependency on Linux CI
- **CI**: remove unsupported `--manifest-path` from `cargo tauri build`
- **CI**: add `pkg-config` and `libssl-dev` for server build on ubuntu-latest

#### Cleanup

- Remove MSI and deb packages from desktop release (NSIS + AppImage only)
- Remove desktop SHA256SUMS.txt (updater `.sig` files provide integrity verification)
- Move Homebrew Cask to dedicated `homebrew-nyro` repository
- Fix `main` → `master` branch references in install scripts and README

---

## v1.0.0

> Released on 2026-03-09

First public release of Nyro AI Gateway — a complete rewrite from the original OpenResty/Lua API Gateway to a pure Rust local AI protocol gateway.

#### Features

- **Multi-protocol ingress**: OpenAI (`/v1/chat/completions`), Anthropic (`/v1/messages`), Gemini (`/v1beta/models/*/generateContent`) — both streaming (SSE) and non-streaming
- **Any upstream target**: routes to any OpenAI-compatible, Anthropic, or Gemini provider
- **Provider management**: create, edit, delete providers with base URL and encrypted API key
- **Route management**: priority-based routing rules with model override and fallback provider support
- **Request logging**: persistent SQLite log with protocol, model, latency, status, and token counts
- **Usage statistics**: overview dashboard with hourly/daily charts and provider/model breakdowns
- **API key encryption**: AES-256-GCM encryption for stored API keys
- **Bearer token auth**: optional independent authentication for proxy and admin endpoints
- **Desktop application**: Tauri v2 cross-platform desktop app (macOS / Windows / Linux)
  - System tray with quick access menu
  - Optional auto-start on system login
  - In-app auto-update via Tauri updater
  - Native macOS title bar integration
  - Dark / light mode toggle
  - Chinese / English language switching
- **Server binary**: standalone `nyro-server` binary for server deployment with HTTP WebUI access
  - Configurable bind addresses for proxy and admin ports
  - CORS allowlist configuration
  - Non-loopback binding enforces auth key requirement
- **CI/CD**: GitHub Actions workflows for cross-platform desktop bundle and server binary releases
