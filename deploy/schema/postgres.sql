-- Nyro AI Gateway — PostgreSQL Final Schema
--
-- This file represents the authoritative final-state schema after all migrations.
-- Use it to pre-create the database on a fresh PostgreSQL instance before starting
-- the server, or pass it to your DBA for review.
--
-- Generated from: crates/nyro-core/src/storage/postgres/mod.rs (POSTGRES_INIT_SQL + migrate())
-- Regenerate  : nyro-tools dump-schema --backend postgres
-- Keep in sync: update this file whenever POSTGRES_INIT_SQL or the migrate() function changes.

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
    access_token TEXT,
    refresh_token TEXT,
    expires_at TIMESTAMPTZ,
    use_proxy BOOLEAN NOT NULL DEFAULT FALSE,
    last_test_success BOOLEAN,
    last_test_at TIMESTAMPTZ,
    is_enabled BOOLEAN DEFAULT TRUE,
    priority INTEGER DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

-- Final name: models (renamed from routes)
CREATE TABLE IF NOT EXISTS models (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    balance TEXT DEFAULT 'weighted',
    target_provider TEXT NOT NULL REFERENCES providers(id),
    target_model TEXT NOT NULL,
    enable_auth BOOLEAN DEFAULT FALSE,
    enable_payload BOOLEAN,
    is_enabled BOOLEAN DEFAULT TRUE,
    priority INTEGER DEFAULT 0,
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

-- Final name: model_backends (renamed from route_targets)
CREATE TABLE IF NOT EXISTS model_backends (
    id TEXT PRIMARY KEY,
    model_id TEXT NOT NULL REFERENCES models(id) ON DELETE CASCADE,
    provider_id TEXT NOT NULL REFERENCES providers(id),
    model TEXT NOT NULL,
    weight INTEGER DEFAULT 100,
    priority INTEGER DEFAULT 1,
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_model_backends_model_id ON model_backends(model_id);

CREATE TABLE IF NOT EXISTS request_logs (
    id                        TEXT PRIMARY KEY,
    created_at                BIGINT NOT NULL DEFAULT 0,
    api_key_id                TEXT,
    api_key_name              TEXT,
    client_protocol           TEXT,
    upstream_protocol         TEXT,
    provider_id               TEXT,
    provider_name             TEXT,
    model_id                  TEXT,
    model_name                TEXT,
    upstream_url              TEXT,
    client_model              TEXT,
    upstream_model            TEXT,
    method                    TEXT,
    path                      TEXT,
    client_request_headers    TEXT,
    client_request_body       TEXT,
    client_response_headers   TEXT,
    client_response_body      TEXT,
    upstream_request_headers  TEXT,
    upstream_request_body     TEXT,
    upstream_response_headers TEXT,
    upstream_response_body    TEXT,
    upstream_status_code      INTEGER,
    client_status_code        INTEGER,
    latency_total_ms          BIGINT,
    latency_upstream_ms       BIGINT,
    input_tokens              INTEGER DEFAULT 0,
    output_tokens             INTEGER DEFAULT 0,
    cache_read_tokens         INTEGER DEFAULT 0,
    is_stream                 BOOLEAN DEFAULT FALSE,
    stream_chunks_count       INTEGER DEFAULT 0,
    stream_first_chunk_ms     BIGINT
);

CREATE INDEX IF NOT EXISTS idx_logs_created_at ON request_logs(created_at);
CREATE INDEX IF NOT EXISTS idx_logs_provider_id ON request_logs(provider_id);
CREATE INDEX IF NOT EXISTS idx_logs_client_status ON request_logs(client_status_code);
CREATE INDEX IF NOT EXISTS idx_logs_upstream_model ON request_logs(upstream_model);
CREATE INDEX IF NOT EXISTS idx_logs_api_key ON request_logs(api_key_id);

CREATE TABLE IF NOT EXISTS settings (
    name TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS api_keys (
    id TEXT PRIMARY KEY,
    token TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    rpm INTEGER,
    rpd INTEGER,
    tpm INTEGER,
    tpd INTEGER,
    is_enabled BOOLEAN DEFAULT TRUE,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

-- Final name: api_key_models (renamed from api_key_routes)
CREATE TABLE IF NOT EXISTS api_key_models (
    api_key_id TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    model_id TEXT NOT NULL REFERENCES models(id) ON DELETE CASCADE,
    PRIMARY KEY (api_key_id, model_id)
);

CREATE INDEX IF NOT EXISTS idx_api_keys_token ON api_keys(token);
CREATE INDEX IF NOT EXISTS idx_api_key_models_model_id ON api_key_models(model_id);

CREATE TABLE IF NOT EXISTS provider_oauth_credentials (
    provider_id       TEXT PRIMARY KEY REFERENCES providers(id) ON DELETE CASCADE,
    driver_key        TEXT NOT NULL DEFAULT '',
    scheme            TEXT NOT NULL DEFAULT '',
    access_token      TEXT NOT NULL DEFAULT '',
    refresh_token     TEXT,
    expires_at        TIMESTAMPTZ,
    resource_url      TEXT,
    subject_id        TEXT,
    scopes            TEXT NOT NULL DEFAULT '[]',
    meta              TEXT NOT NULL DEFAULT '{}',
    status            TEXT NOT NULL DEFAULT 'connected',
    status_version    INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT,
    last_refresh_at   TIMESTAMPTZ,
    created_at        TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMPTZ DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_oauth_creds_status ON provider_oauth_credentials(status);
CREATE INDEX IF NOT EXISTS idx_oauth_creds_expires ON provider_oauth_credentials(expires_at);
