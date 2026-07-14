# Database Schema

The normalized relational schema backing the Go gateway's storage layer. It is
shared by the SQLite, MySQL, and Postgres backends; the SQL below is the final
post-migration state with SQLite-flavored types. GORM entities in
`go/internal/storage/model/` are the canonical source; this document mirrors
them for readability — it is illustrative, not applied to any database.

For mysql/postgres, the SQL that actually gets run is the versioned, DBA-
reviewable migration files under `go/migrations/{mysql,postgres}/`, not this
document — see [migrations.md](migrations.md) for the full workflow (how
they're generated, how CI enforces they stay in sync with the models, how a
DBA applies them, and the `--auto-migrate` flag). sqlite has no migration
files and keeps using GORM AutoMigrate.

Two columns beyond `id` carry an explicit `size` tag in the GORM models: any
`string` field with a `uniqueIndex`/`index` tag needs one, because MySQL
rejects an index on an unbounded `TEXT`/`LONGTEXT` column ("BLOB/TEXT column
... used in key specification without a key length"). `upstreams.name`,
`routes.model`, `route_upstreams.{route_id,upstream_id,model}`,
`consumers.name`, and `consumer_keys.{consumer_id,name,key_preview}` all carry
`size:191` or `size:255` tags for this reason (191 mirrors GORM's own default
for primary-key string columns; 255 for the rest). This is a real constraint
against a live mysql database, not merely a lint concern — confirmed by
running GORM AutoMigrate against mysql directly, which fails with exactly
that error without the size tags.

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
