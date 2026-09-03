# Nyro Go Schema Reference

This directory is the reference for the Nyro Go gateway's user-facing
configuration and its normalized database schema. It reflects the agreed
design direction and is the place to look before changing the config shape or
the storage tables.

- [config.md](config.md) - standalone `config.yaml` structure (user-facing).
- [database.md](database.md) - normalized SQL schema (storage layer).
- [protocols.md](protocols.md) - protocol identity, IDs, names, and aliases.

## Design Principles

- The YAML model is the user-facing configuration shape; the database schema is
  the normalized relational form used by admin, config-sync, and storage.
- Standalone YAML contains data-plane configuration only. Admin-only metadata
  and local storage settings, such as `gateway.public_url` and telemetry
  retention, are documented in `config.md` but live in the admin DB or CLI
  flags and are never distributed to gateways.
- The data plane resolves routing purely from `route_upstreams.model`. Upstream
  model lists and discovery are control-plane concerns only (route dropdowns,
  health checks) and never drive routing.
- An upstream declares its models in exactly one of two mutually exclusive ways:
  - `models`: a static, manually curated list (persisted in `models_json`).
  - `models_url`: a discovery endpoint URL fetched live at control-plane
    request time with a short in-memory TTL cache (only the URL is persisted;
    the fetched list is not).
- Provider presets (`internal/llm/provider/*.go`) expose immutable
  `Definition` data and Driver factories through the explicitly assembled
  Bootstrap catalog. Drivers apply Provider authentication and endpoint rules;
  importing a provider does not register it implicitly.
- Protocol IDs identify a concrete API wire surface (an interface), not a
  provider "family"; they are vendor-prefixed but vendor-orthogonal in use.
  See [protocols.md](protocols.md).

## Status

Design reference, fully landed in code as of the `provider`/`models_url`/
protocol-id refactor. Keep this doc in sync when the schema changes. Follow the
[Go database rules](../../AGENTS.md); the repository-root
[Rust database rules](../../../AGENTS.md) apply only to Rust reference
artifacts.
