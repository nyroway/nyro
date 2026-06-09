<!-- Generated: 2026-05-21 | Updated: 2026-05-21 -->

# Nyro AI Gateway

## Purpose
Nyro is a Rust workspace for a local AI protocol gateway with a Tauri desktop app, standalone server, and React WebUI. It translates OpenAI / Anthropic / Gemini-compatible client traffic to configured model providers while keeping administration and configuration local.

## Key Files

| File | Description |
|------|-------------|
| `Cargo.toml` | Rust workspace definition for `nyro-core`, `nyro-tools`, `src-tauri`, and `src-server`. |
| `Cargo.lock` | Locked Rust dependency graph. |
| `README.md` / `README_CN.md` | User-facing project documentation in English and Chinese. |
| `Makefile` | Common development and release commands. |
| `docs/design/architecture.md` | Architecture overview and module layout. |
| `webui/package.json` | React/Vite WebUI dependencies and scripts. |

## Subdirectories

| Directory | Purpose |
|-----------|---------|
| `crates/nyro-core/` | Core Rust library: gateway, proxy, protocol conversion, provider adapters, storage, admin service. |
| `crates/nyro-tools/` | Rust CLI/tooling crate. |
| `src-server/` | Standalone server binary exposing proxy/admin HTTP surfaces. |
| `src-tauri/` | Tauri desktop application shell and IPC integration. |
| `webui/` | React + TypeScript management console. |
| `docs/` | Design, server, standalone, and testing documentation. |
| `tests/` | Python/E2E test assets and shared fixtures. |
| `scripts/` | Install and release automation. |

## For AI Agents

### Working In This Repository
- Keep changes focused and reversible; do not mix unrelated cleanup into feature or refactor work.
- Prefer existing patterns and utilities before adding abstractions or dependencies.
- Do not edit generated build output such as `webui/dist/` unless the task explicitly requires it.
- Preserve separate English and Chinese user-facing docs when updating public documentation.

### Multilingual Defaults
- For any multilingual/i18n-capable value, the default must be English, in both frontend and backend code.
- This applies to UI labels, fallback strings, seed/default configuration, generated examples, API defaults, and documentation-derived constants.
- Add localized alternatives explicitly, but keep the canonical fallback/default value in English unless a caller/user setting selects another language.

### Testing Requirements
- Rust core changes: run the narrowest relevant `cargo test -p <crate> ...`, then `cargo check -p <crate>` or `cargo clippy -p <crate> --all-targets` when behavior or public APIs change.
- WebUI changes: run the relevant package script from `webui/` such as `npm run lint` or `npm run build` when TypeScript/UI behavior changes.
- Documentation-only changes should still be checked for path/name accuracy.

### Common Patterns
- `nyro-core` should remain transport-agnostic; desktop IPC and server HTTP layers call into core APIs rather than embedding core business logic.
- Admin service code should be split by functional responsibility and tested through public APIs where possible; keep private state-machine tests internal instead of exposing private APIs just for tests.
- Protocol/provider logic should keep protocol conversion boundaries explicit and avoid coupling provider adapters to UI/server transport concerns.

## Dependencies

### Internal
- `src-tauri/` and `src-server/` depend on `crates/nyro-core/`.
- `webui/` talks to the desktop IPC/server admin surfaces and should not duplicate core business rules.
- Documentation in `docs/` should reflect current crate and module boundaries.

### External
- Rust workspace uses Tokio, Axum, Reqwest, SQLx, Serde, Tauri, and tracing-related crates.
- WebUI uses React, Vite, TypeScript, Radix UI primitives, TanStack Query, and Zustand.

<!-- MANUAL: Any manually added notes below this line are preserved on regeneration -->

### Database Changes

When modifying the database schema — including changes to `INIT_SQL`, `POSTGRES_INIT_SQL`, `MYSQL_INIT_SQL` constants or the `migrate()` function in any storage backend — you **must** also:

1. Update `docs/database/schema.md` to reflect the new table/column definitions.
2. Regenerate `deploy/schema/postgres.sql` and `deploy/schema/mysql.sql` to match the final post-migration state:
   ```bash
   nyro-tools dump-schema --backend postgres > deploy/schema/postgres.sql
   nyro-tools dump-schema --backend mysql    > deploy/schema/mysql.sql
   ```
   These files are the authoritative reference schema for DBAs. They represent the **final state** after all migrations have run (with final table names: `models`, `model_backends`, `api_key_models`).

> The SQL files in `deploy/schema/` are derived reference artifacts — do not manually edit them except to update the header comment. Always regenerate from the migration source of truth.
