-- Nyro AI Gateway — MySQL Final Schema
--
-- This file represents the authoritative final-state schema after all migrations.
-- Use it to pre-create the database on a fresh MySQL instance before starting
-- the server, or pass it to your DBA for review.
--
-- Generated from: crates/nyro-core/src/storage/mysql.rs (MYSQL_INIT_SQL + migrate())
-- Regenerate  : nyro-tools dump-schema --backend mysql
-- Keep in sync: update this file whenever MYSQL_INIT_SQL or the migrate() function changes.

CREATE TABLE IF NOT EXISTS providers (
    id VARCHAR(36) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    vendor VARCHAR(255),
    protocol VARCHAR(255) NOT NULL,
    base_url TEXT NOT NULL,
    preset_key VARCHAR(255),
    channel VARCHAR(255),
    models_source TEXT,
    static_models TEXT,
    api_key TEXT NOT NULL,
    auth_mode VARCHAR(255) NOT NULL DEFAULT 'apikey',
    access_token TEXT,
    refresh_token TEXT,
    expires_at DATETIME,
    use_proxy TINYINT(1) NOT NULL DEFAULT 0,
    last_test_success TINYINT(1),
    last_test_at DATETIME,
    is_enabled TINYINT(1) DEFAULT 1,
    priority INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Final name: models (renamed from routes)
CREATE TABLE IF NOT EXISTS models (
    id VARCHAR(36) PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    balance VARCHAR(255) DEFAULT 'weighted',
    target_provider VARCHAR(36) NOT NULL,
    target_model VARCHAR(255) NOT NULL,
    enable_auth TINYINT(1) DEFAULT 0,
    enable_payload TINYINT(1) DEFAULT NULL,
    is_enabled TINYINT(1) DEFAULT 1,
    priority INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (target_provider) REFERENCES providers(id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Final name: model_backends (renamed from route_targets)
CREATE TABLE IF NOT EXISTS model_backends (
    id VARCHAR(36) PRIMARY KEY,
    model_id VARCHAR(36) NOT NULL,
    provider_id VARCHAR(36) NOT NULL,
    model VARCHAR(255) NOT NULL,
    weight INTEGER DEFAULT 100,
    priority INTEGER DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (model_id) REFERENCES models(id) ON DELETE CASCADE,
    FOREIGN KEY (provider_id) REFERENCES providers(id)
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE INDEX idx_model_backends_model_id ON model_backends(model_id);

CREATE TABLE IF NOT EXISTS request_logs (
    id                        VARCHAR(36) PRIMARY KEY,
    created_at                BIGINT NOT NULL DEFAULT 0,
    api_key_id                VARCHAR(36),
    api_key_name              VARCHAR(255),
    client_protocol           VARCHAR(255),
    upstream_protocol         VARCHAR(255),
    provider_id               VARCHAR(36),
    provider_name             VARCHAR(255),
    model_id                  VARCHAR(36),
    model_name                VARCHAR(255),
    upstream_url              TEXT,
    client_model              VARCHAR(255),
    upstream_model            VARCHAR(255),
    method                    VARCHAR(255),
    path                      TEXT,
    client_request_headers    TEXT,
    client_request_body       LONGTEXT,
    client_response_headers   TEXT,
    client_response_body      LONGTEXT,
    upstream_request_headers  TEXT,
    upstream_request_body     LONGTEXT,
    upstream_response_headers TEXT,
    upstream_response_body    LONGTEXT,
    upstream_status_code      INTEGER,
    client_status_code        INTEGER,
    latency_total_ms          BIGINT,
    latency_upstream_ms       BIGINT,
    input_tokens              INTEGER DEFAULT 0,
    output_tokens             INTEGER DEFAULT 0,
    cache_read_tokens         INTEGER DEFAULT 0,
    is_stream                 TINYINT(1) DEFAULT 0,
    stream_chunks_count       INTEGER DEFAULT 0,
    stream_first_chunk_ms     BIGINT
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE INDEX idx_logs_created_at ON request_logs(created_at);
CREATE INDEX idx_logs_provider_id ON request_logs(provider_id);
CREATE INDEX idx_logs_client_status ON request_logs(client_status_code);
CREATE INDEX idx_logs_upstream_model ON request_logs(upstream_model);
CREATE INDEX idx_logs_api_key ON request_logs(api_key_id);

CREATE TABLE IF NOT EXISTS settings (
    name VARCHAR(255) PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS api_keys (
    id VARCHAR(36) PRIMARY KEY,
    token VARCHAR(255) NOT NULL UNIQUE,
    name VARCHAR(255) NOT NULL,
    rpm INTEGER,
    rpd INTEGER,
    tpm INTEGER,
    tpd INTEGER,
    is_enabled TINYINT(1) DEFAULT 1,
    expires_at DATETIME,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Final name: api_key_models (renamed from api_key_routes)
CREATE TABLE IF NOT EXISTS api_key_models (
    api_key_id VARCHAR(36) NOT NULL,
    model_id VARCHAR(36) NOT NULL,
    PRIMARY KEY (api_key_id, model_id),
    FOREIGN KEY (api_key_id) REFERENCES api_keys(id) ON DELETE CASCADE,
    FOREIGN KEY (model_id) REFERENCES models(id) ON DELETE CASCADE
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE INDEX idx_api_keys_token ON api_keys(token);
CREATE INDEX idx_api_key_models_model_id ON api_key_models(model_id);

CREATE TABLE IF NOT EXISTS provider_oauth_credentials (
    provider_id       VARCHAR(36) PRIMARY KEY,
    driver_key        TEXT NOT NULL,
    scheme            VARCHAR(255) NOT NULL DEFAULT '',
    access_token      TEXT NOT NULL,
    refresh_token     TEXT,
    expires_at        DATETIME,
    resource_url      TEXT,
    subject_id        VARCHAR(255),
    scopes            TEXT NOT NULL,
    meta              TEXT NOT NULL,
    status            VARCHAR(255) NOT NULL DEFAULT 'connected',
    status_version    INTEGER NOT NULL DEFAULT 0,
    last_error        TEXT,
    last_refresh_at   DATETIME,
    created_at        DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    FOREIGN KEY (provider_id) REFERENCES providers(id) ON DELETE CASCADE
) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE INDEX idx_oauth_creds_status ON provider_oauth_credentials(status);
CREATE INDEX idx_oauth_creds_expires ON provider_oauth_credentials(expires_at);
