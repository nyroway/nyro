-- Nyro gateway schema (SQLite). Applied idempotently on bootstrap.
-- AutoMigrate is OFF; this file is the source of truth. Columns match the
-- Rust post-migration final schema (db/mod.rs INIT_SQL + renames:
-- routes→models, route_targets→model_backends, settings.name, request_logs
-- 31-column spec) so a Go gateway can read a Rust DB after cutover.

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

CREATE TABLE IF NOT EXISTS provider_oauth_credentials (
  provider_id TEXT PRIMARY KEY,
  driver_key TEXT,
  scheme TEXT,
  access_token TEXT,
  refresh_token TEXT,
  expires_at TEXT,
  resource_url TEXT,
  subject_id TEXT,
  scopes TEXT,
  meta TEXT,
  status TEXT NOT NULL DEFAULT 'connected',
  status_version INTEGER NOT NULL DEFAULT 0,
  last_error TEXT,
  last_refresh_at TEXT,
  created_at TEXT,
  updated_at TEXT
);

CREATE TABLE IF NOT EXISTS request_logs (
  id TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL DEFAULT 0,
  api_key_id TEXT,
  api_key_name TEXT,
  client_protocol TEXT,
  upstream_protocol TEXT,
  provider_id TEXT,
  provider_name TEXT,
  model_id TEXT,
  model_name TEXT,
  upstream_url TEXT,
  client_model TEXT,
  upstream_model TEXT,
  method TEXT,
  path TEXT,
  client_request_headers TEXT,
  client_request_body TEXT,
  client_response_headers TEXT,
  client_response_body TEXT,
  upstream_request_headers TEXT,
  upstream_request_body TEXT,
  upstream_response_headers TEXT,
  upstream_response_body TEXT,
  upstream_status_code INTEGER,
  client_status_code INTEGER,
  latency_total_ms INTEGER,
  latency_upstream_ms INTEGER,
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cache_read_tokens INTEGER NOT NULL DEFAULT 0,
  is_stream INTEGER NOT NULL DEFAULT 0,
  stream_chunks_count INTEGER NOT NULL DEFAULT 0,
  stream_first_chunk_ms INTEGER
);

CREATE INDEX IF NOT EXISTS idx_request_logs_created_at ON request_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_request_logs_api_key_id ON request_logs(api_key_id);
