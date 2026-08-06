# Database Schema

The normalized relational schema backing the Go gateway's Config Engine. It is
shared by the SQLite and Postgres backends; the SQL below is the final
post-migration state with SQLite-flavored types. GORM entities in
`go/internal/storage/model/` are the canonical source; this document mirrors
them for readability — it is illustrative, not applied to any database.

For postgres, the SQL that actually gets run is rendered on demand by `nyro
tool migrate dump` / `nyro tool migrate diff` from these same models — see
[migrations.md](migrations.md) for the full workflow (how the DDL is
generated, how a DBA reviews and applies it, and the `--auto-migrate` flag).
sqlite has no manual step and keeps using GORM AutoMigrate.

A handful of identifier-ish string columns carry an explicit `size` tag in the
GORM models: `upstreams.name`, `routes.model`,
`route_upstreams.{route_id,upstream_id,model}`, `consumers.name`, and
`consumer_keys.{consumer_id,name,key_preview}` are tagged `size:191` or
`size:255` (191 mirrors GORM's own default for primary-key string columns; 255
for the rest). These render as `varchar(N)` on postgres, giving those columns a
real length bound at the database layer — names, model ids and key previews are
short by nature, and without the tags every string column would degrade to an
unbounded `text`. Keep the tags when adding a comparable column.

## Tables

```sql
CREATE TABLE upstreams (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  provider TEXT NOT NULL,           -- provider preset id or 'custom'
  protocol TEXT,
  base_url TEXT,
  credentials_json TEXT,
  models_json TEXT,                 -- manual static model list (when using `models`)
  models_url TEXT,                  -- discovery endpoint URL (when using `models_url`); list is NOT persisted
  proxy_url TEXT,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE routes (
  id TEXT PRIMARY KEY,
  model TEXT NOT NULL UNIQUE,
  balance TEXT NOT NULL DEFAULT 'weighted',
  enable_auth BOOLEAN NOT NULL DEFAULT FALSE,
  enable_payload BOOLEAN NOT NULL DEFAULT FALSE,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE route_upstreams (
  id TEXT PRIMARY KEY,
  route_id TEXT NOT NULL,
  upstream_id TEXT NOT NULL,
  model TEXT NOT NULL,
  weight INTEGER NOT NULL DEFAULT 100,
  priority INTEGER NOT NULL DEFAULT 1,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (route_id, upstream_id, model)
);

CREATE TABLE consumers (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE consumer_keys (
  id TEXT PRIMARY KEY,
  consumer_id TEXT NOT NULL,
  name TEXT NOT NULL,
  key_preview TEXT NOT NULL,
  key_hash TEXT NOT NULL,
  key_plaintext TEXT,            -- recoverable raw key; set only under server --raw-api-keys, else empty
  enabled BOOLEAN NOT NULL DEFAULT TRUE,
  expires_at TEXT,
  last_used_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (consumer_id, name)
);

CREATE TABLE consumer_routes (
  consumer_id TEXT NOT NULL,
  route_id TEXT NOT NULL,
  PRIMARY KEY (consumer_id, route_id)
);

CREATE TABLE consumer_quotas (
  id TEXT PRIMARY KEY,
  consumer_id TEXT NOT NULL,
  quota_type TEXT NOT NULL,          -- requests | tokens | concurrency
  quota_limit INTEGER NOT NULL,
  window TEXT,                        -- NULL for concurrency
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
```

## Notes

- `upstreams.provider` stores the provider preset id (or `custom`). It is
  control-plane metadata: it anchors the UI's selected preset on edit and lets
  discovery-URL fallback look up the preset. The data plane does not depend on
  it (outbound auth is resolved by the provider auth scheme).
- `upstreams.models_json` and `upstreams.models_url` are mutually exclusive
  in practice, matching the YAML `models` xor `models_url` rule. Discovered model
  lists are intentionally NOT stored; only the discovery endpoint URL is
  persisted.
- There is no `upstream_models` table. Discovery is live + in-memory cached, so
  per-model rows/curation are out of scope for this design.
- Routing is driven exclusively by `route_upstreams.model`. Upstream model
  metadata never participates in target selection.
- `consumer_quotas` binds to `consumers` (not `consumer_keys`), so all keys of a
  consumer share one quota pool.

## YAML to SQL Mapping

- `upstreams[].name` -> `upstreams.name`
- `upstreams[].provider` -> `upstreams.provider`
- `upstreams[].protocol` -> `upstreams.protocol`
- `upstreams[].base_url` -> `upstreams.base_url`
- `upstreams[].credentials` -> `upstreams.credentials_json`
- `upstreams[].models` -> `upstreams.models_json`
- `upstreams[].models_url` -> `upstreams.models_url`
- `upstreams[].proxy.url` -> `upstreams.proxy_url`
- `routes[].model` -> `routes.model`
- `routes[].balance` / `enable_auth` / `enable_payload` -> `routes.*`
- `routes[].upstreams[]` -> `route_upstreams`
- `consumers[].keys[]` -> `consumer_keys`
- `consumers[].routes[]` -> `consumer_routes`
- `consumers[].quotas.requests[]` -> `consumer_quotas` (`quota_type = 'requests'`)
- `consumers[].quotas.tokens[]` -> `consumer_quotas` (`quota_type = 'tokens'`)
- `consumers[].quotas.concurrency.max_requests` -> `consumer_quotas`
  (`quota_type = 'concurrency'`, `window = NULL`)
- `settings` nested YAML -> `settings` dot-key rows

## Embedded infrastructure databases

Single-node `nyro serve` keeps its three local databases under
`~/.nyro/data` by default:

- `config.db` is the SQLite Config Engine described above. A Postgres DSN can
  replace it.
- `state.db` backs the embedded Redis-compatible State Engine.
- `observe.db` stores lossless OTLP batches plus query indexes for the embedded
  Observe Engine.

SQL connection ownership is centralized under `go/infra/database`: SQLite and
Postgres pools are opened, verified, configured, and closed there. The Config
Engine wraps its caller-owned pool with GORM for schema and query behavior;
State and Observe use independent caller-owned SQLite pools directly. Their
protocol boundaries are Redis and OTLP, so a clustered deployment can disable
the embedded listeners and point clients at external components.

`state.db` contains `state_kv(key BLOB PRIMARY KEY, value BLOB NOT NULL,
expires_at_ms INTEGER NULL)` and a partial expiry index on `expires_at_ms`.

`observe.db` contains:

- `otlp_batches`: the original protobuf payload, signal, and receiver time;
- `otlp_log_index`, `otlp_span_index`, and `otlp_metric_index`: coordinates and
  signal-specific fields used to locate records inside each original payload;
- `otlp_log_attribute_definitions` and `otlp_log_attributes`: registered,
  typed log-attribute indexes. Nyro registers log id, upstream id, route id,
  route model, consumer id, and HTTP response status.

Foreign-key cascades remove signal indexes with their source batch. Retention
deletes old batches in bounded transactions; it does not rewrite OTLP payloads.
Legacy Parquet observability files are neither imported nor deleted during
startup; remove or archive them separately after validating the SQLite cutover.
