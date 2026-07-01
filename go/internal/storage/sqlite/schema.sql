-- Nyro gateway schema (SQLite). Applied idempotently on bootstrap.
-- AutoMigrate is OFF; this file is the source of truth. Columns match the
-- Rust post-migration final schema (db/mod.rs INIT_SQL + renames:
-- routes→models, route_targets→model_backends, settings.name) so a Go
-- gateway can read a Rust DB after cutover. The request_logs table was
-- removed in Phase 4 (request logs now live in the parquet observability
-- store); Migrate() drops any leftover copy.

CREATE TABLE IF NOT EXISTS providers (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  vendor TEXT,
  protocol TEXT NOT NULL,
  base_url TEXT NOT NULL,
  preset_key TEXT,
  channel TEXT,
  models_source TEXT,
  static_models TEXT,
  api_key TEXT NOT NULL,
  auth_mode TEXT NOT NULL DEFAULT 'apikey' CHECK (auth_mode IN ('apikey', 'oauth')),
  use_proxy INTEGER NOT NULL DEFAULT 0,
  last_test_success INTEGER,
  last_test_at TEXT,
  is_enabled INTEGER NOT NULL DEFAULT 1,
  priority INTEGER NOT NULL DEFAULT 0,
  created_at TEXT,
  updated_at TEXT
);

CREATE TABLE IF NOT EXISTS models (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  balance TEXT NOT NULL DEFAULT 'weighted',
  target_provider TEXT NOT NULL,
  target_model TEXT NOT NULL,
  enable_auth INTEGER NOT NULL DEFAULT 0,
  enable_payload INTEGER,
  is_enabled INTEGER NOT NULL DEFAULT 1,
  priority INTEGER NOT NULL DEFAULT 0,
  created_at TEXT
);

CREATE TABLE IF NOT EXISTS model_backends (
  id TEXT PRIMARY KEY,
  model_id TEXT NOT NULL,
  provider_id TEXT NOT NULL,
  model TEXT NOT NULL,
  weight INTEGER NOT NULL DEFAULT 100,
  priority INTEGER NOT NULL DEFAULT 1,
  created_at TEXT
);

CREATE TABLE IF NOT EXISTS settings (
  name TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at TEXT
);

CREATE TABLE IF NOT EXISTS api_keys (
  id TEXT PRIMARY KEY,
  token TEXT NOT NULL,
  name TEXT NOT NULL,
  rpm INTEGER,
  rpd INTEGER,
  tpm INTEGER,
  tpd INTEGER,
  is_enabled INTEGER NOT NULL DEFAULT 1,
  expires_at TEXT,
  created_at TEXT,
  updated_at TEXT
);

CREATE TABLE IF NOT EXISTS api_key_models (
  api_key_id TEXT NOT NULL,
  model_id TEXT NOT NULL,
  PRIMARY KEY (api_key_id, model_id)
);

-- provider_oauth_credentials table removed: OAuth/driver auth infrastructure
-- deleted; cloud provider auth will be rebuilt in provider.NewAuthenticator.
