pub mod models;

use std::path::Path;

use sqlx::Row;
use sqlx::SqlitePool;
use sqlx::sqlite::{SqliteConnectOptions, SqlitePoolOptions};

use crate::protocol::registry::ProtocolRegistry;

pub async fn init_pool(data_dir: &Path) -> anyhow::Result<SqlitePool> {
    std::fs::create_dir_all(data_dir)?;
    let db_path = data_dir.join("gateway.db");

    let options = SqliteConnectOptions::new()
        .filename(&db_path)
        .create_if_missing(true)
        .journal_mode(sqlx::sqlite::SqliteJournalMode::Wal)
        .busy_timeout(std::time::Duration::from_secs(5));

    let pool = SqlitePoolOptions::new()
        .max_connections(5)
        .connect_with(options)
        .await?;

    Ok(pool)
}

pub async fn migrate(pool: &SqlitePool) -> anyhow::Result<()> {
    sqlx::raw_sql(INIT_SQL).execute(pool).await?;
    ensure_provider_column(pool, "vendor", "TEXT").await?;
    ensure_provider_column(pool, "preset_key", "TEXT").await?;
    ensure_provider_column(pool, "channel", "TEXT").await?;
    ensure_provider_column(pool, "models_source", "TEXT").await?;
    drop_provider_column_if_exists(pool, "capabilities_source").await?;
    ensure_provider_column(pool, "static_models", "TEXT").await?;
    ensure_provider_column(pool, "last_test_success", "INTEGER").await?;
    ensure_provider_column(pool, "last_test_at", "TEXT").await?;
    ensure_provider_column(pool, "use_proxy", "INTEGER DEFAULT 0").await?;
    migrate_collapse_provider_protocol_columns(pool).await?;
    ensure_route_column(pool, "virtual_model", "TEXT").await?;
    ensure_route_column(pool, "balance", "TEXT DEFAULT 'weighted'").await?;
    ensure_route_column(pool, "access_control", "INTEGER DEFAULT 0").await?;
    ensure_request_log_column(pool, "api_key_id", "TEXT").await?;
    ensure_request_log_column(pool, "method", "TEXT").await?;
    ensure_request_log_column(pool, "path", "TEXT").await?;
    ensure_request_log_column(pool, "request_headers", "TEXT").await?;
    ensure_request_log_column(pool, "request_body", "TEXT").await?;
    ensure_request_log_column(pool, "response_headers", "TEXT").await?;
    ensure_request_log_column(pool, "response_body", "TEXT").await?;
    ensure_api_key_tables(pool).await?;
    ensure_api_key_column(pool, "rpd", "INTEGER").await?;
    // Migrate: providers/routes is_active -> is_enabled
    ensure_provider_column(pool, "is_enabled", "INTEGER DEFAULT 1").await?;
    migrate_provider_is_active_to_is_enabled(pool).await?;
    ensure_route_column(pool, "is_enabled", "INTEGER DEFAULT 1").await?;
    migrate_route_is_active_to_is_enabled(pool).await?;
    // Migrate: api_keys status -> is_enabled
    ensure_api_key_column(pool, "is_enabled", "INTEGER DEFAULT 1").await?;
    migrate_api_key_status_to_is_enabled(pool).await?;
    ensure_route_targets_table(pool).await?;
    ensure_provider_column(pool, "auth_mode", "TEXT NOT NULL DEFAULT 'apikey'").await?;
    sqlx::query("UPDATE providers SET auth_mode = 'apikey' WHERE auth_mode = 'api_key'")
        .execute(pool)
        .await?;
    ensure_provider_column(pool, "access_token", "TEXT").await?;
    ensure_provider_column(pool, "refresh_token", "TEXT").await?;
    ensure_provider_column(pool, "expires_at", "TEXT").await?;
    ensure_oauth_credentials_table(pool).await?;
    migrate_oauth_credentials_from_providers(pool).await?;
    backfill_provider_vendor(pool).await?;
    migrate_vendor_renames(pool).await?;
    normalize_provider_protocols(pool).await?;
    backfill_route_fields(pool).await?;
    backfill_route_targets(pool).await?;
    migrate_logs_v2_spec_aligned(pool).await?;
    ensure_request_log_column(pool, "is_stream", "INTEGER DEFAULT 0").await?;
    ensure_request_log_column(pool, "api_key_name", "TEXT").await?;
    ensure_request_log_column(pool, "provider_name", "TEXT").await?;
    ensure_request_log_column(pool, "model_id", "TEXT").await?;
    ensure_request_log_column(pool, "model_name", "TEXT").await?;
    ensure_request_log_column(pool, "cache_read_tokens", "INTEGER DEFAULT 0").await?;

    // Rename tables: routes → models, route_targets → model_backends, api_key_routes → api_key_models
    rename_table_if_needed(pool, "routes", "models").await?;
    rename_table_if_needed(pool, "route_targets", "model_backends").await?;
    rename_table_if_needed(pool, "api_key_routes", "api_key_models").await?;

    // Rename columns within renamed tables
    rename_column_if_needed(pool, "model_backends", "route_id", "model_id").await?;
    rename_column_if_needed(pool, "api_key_models", "route_id", "model_id").await?;

    // Rename columns: request_logs route_id/route_name → model_id/model_name
    rename_column_if_needed(pool, "request_logs", "route_id", "model_id").await?;
    rename_column_if_needed(pool, "request_logs", "route_name", "model_name").await?;

    // Rename column: models strategy → balance
    rename_column_if_needed(pool, "models", "strategy", "balance").await?;

    Ok(())
}

/// Rewrites legacy / alias protocol identifiers in `providers.protocol` into
/// canonical protocol-suite strings (for example, `openai` -> `openai-compatible`).
async fn normalize_provider_protocols(pool: &SqlitePool) -> anyhow::Result<()> {
    let reg = ProtocolRegistry::global();
    let rows = sqlx::query("SELECT id, protocol FROM providers")
        .fetch_all(pool)
        .await?;

    for row in rows {
        let id: String = row.try_get("id")?;
        let raw_protocol: String = row.try_get("protocol").unwrap_or_default();
        let new_protocol = normalize_provider_protocol_value(reg, &raw_protocol);

        if new_protocol == raw_protocol {
            continue;
        }

        tracing::info!(
            provider_id = %id,
            old_protocol = %raw_protocol,
            new_protocol = %new_protocol,
            "normalizing provider protocol identifier"
        );

        sqlx::query("UPDATE providers SET protocol = ?1 WHERE id = ?2")
            .bind(&new_protocol)
            .bind(&id)
            .execute(pool)
            .await?;
    }
    Ok(())
}

fn normalize_provider_protocol_value(reg: &ProtocolRegistry, raw: &str) -> String {
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return String::new();
    }
    match reg.parse_protocol(trimmed) {
        Some(protocol) => protocol.as_str().to_string(),
        None => {
            tracing::warn!(
                value = trimmed,
                "leaving unrecognized provider protocol identifier unchanged"
            );
            trimmed.to_string()
        }
    }
}

async fn backfill_provider_vendor(pool: &SqlitePool) -> anyhow::Result<()> {
    if column_exists(pool, "providers", "vendor").await?
        && column_exists(pool, "providers", "preset_key").await?
    {
        sqlx::query(
            "UPDATE providers \
             SET vendor = lower(trim(preset_key)) \
             WHERE (vendor IS NULL OR trim(vendor) = '') \
               AND preset_key IS NOT NULL \
               AND trim(preset_key) != '' \
               AND lower(trim(preset_key)) != 'nyro'",
        )
        .execute(pool)
        .await?;
    }
    Ok(())
}

/// Rename legacy vendor / preset_key values to their new canonical
/// form (PR2B):
///
/// - `custom` → `nyro`
/// - `zhipu`  → `zhipuai`
///
/// Idempotent: re-running the migration on already-normalized data is
/// a no-op.
async fn migrate_vendor_renames(pool: &SqlitePool) -> anyhow::Result<()> {
    const RENAMES: &[(&str, &str)] = &[("nyro", "custom"), ("zhipu", "zhipuai")];

    if column_exists(pool, "providers", "vendor").await? {
        for (from, to) in RENAMES {
            sqlx::query("UPDATE providers SET vendor = ?1 WHERE lower(trim(vendor)) = ?2")
                .bind(to)
                .bind(from)
                .execute(pool)
                .await?;
        }
    }

    if column_exists(pool, "providers", "preset_key").await? {
        for (from, to) in RENAMES {
            sqlx::query("UPDATE providers SET preset_key = ?1 WHERE lower(trim(preset_key)) = ?2")
                .bind(to)
                .bind(from)
                .execute(pool)
                .await?;
        }
    }
    Ok(())
}

async fn migrate_collapse_provider_protocol_columns(pool: &SqlitePool) -> anyhow::Result<()> {
    let has_default_protocol = column_exists(pool, "providers", "default_protocol").await?;
    let has_protocol_endpoints = column_exists(pool, "providers", "protocol_endpoints").await?;
    if !has_default_protocol && !has_protocol_endpoints {
        return Ok(());
    }

    if has_default_protocol {
        sqlx::query(
            "UPDATE providers \
             SET protocol = trim(default_protocol) \
             WHERE default_protocol IS NOT NULL AND trim(default_protocol) != ''",
        )
        .execute(pool)
        .await?;
    }

    if has_protocol_endpoints {
        let rows = sqlx::query("SELECT id, protocol, base_url, protocol_endpoints FROM providers")
            .fetch_all(pool)
            .await?;
        for row in rows {
            let id: String = row.try_get("id")?;
            let protocol: String = row.try_get("protocol").unwrap_or_default();
            let base_url: String = row.try_get("base_url").unwrap_or_default();
            if !base_url.trim().is_empty() {
                continue;
            }
            let raw_endpoints: String = row.try_get("protocol_endpoints").unwrap_or_default();
            if let Some(next_base_url) = base_url_from_protocol_endpoints(&raw_endpoints, &protocol)
            {
                sqlx::query("UPDATE providers SET base_url = ?1 WHERE id = ?2")
                    .bind(next_base_url)
                    .bind(id)
                    .execute(pool)
                    .await?;
            }
        }
    }

    if has_protocol_endpoints {
        drop_provider_column_if_exists(pool, "protocol_endpoints").await?;
    }
    if has_default_protocol {
        drop_provider_column_if_exists(pool, "default_protocol").await?;
    }

    Ok(())
}

fn base_url_from_protocol_endpoints(raw: &str, protocol: &str) -> Option<String> {
    let reg = ProtocolRegistry::global();
    let target = reg.parse_protocol(protocol)?;
    let value = serde_json::from_str::<serde_json::Value>(raw.trim()).ok()?;
    let obj = value.as_object()?;
    let mut skipped = 0usize;
    let mut matched = None;
    for (key, entry) in obj {
        let Some(entry_protocol) = reg.parse_protocol(key) else {
            skipped += 1;
            continue;
        };
        if entry_protocol == target {
            matched = entry
                .as_object()
                .and_then(|object| object.get("base_url"))
                .and_then(|value| value.as_str())
                .map(str::trim)
                .filter(|value| !value.is_empty())
                .map(ToString::to_string);
            if matched.is_some() {
                break;
            }
        } else {
            skipped += 1;
        }
    }
    if skipped > 0 {
        tracing::warn!(
            protocol = protocol,
            skipped_entries = skipped,
            "dropping non-selected protocol_endpoints entries during provider protocol collapse"
        );
    }
    matched
}

async fn ensure_provider_column(
    pool: &SqlitePool,
    column_name: &str,
    definition: &str,
) -> anyhow::Result<()> {
    if !column_exists(pool, "providers", column_name).await? {
        let sql = format!("ALTER TABLE providers ADD COLUMN {column_name} {definition}");
        sqlx::query(&sql).execute(pool).await?;
    }

    Ok(())
}

/// Drop a providers column that is no longer part of the schema (SQLite 3.35+).
async fn drop_provider_column_if_exists(
    pool: &SqlitePool,
    column_name: &str,
) -> anyhow::Result<()> {
    if column_exists(pool, "providers", column_name).await? {
        let sql = format!("ALTER TABLE providers DROP COLUMN {column_name}");
        sqlx::query(&sql).execute(pool).await?;
    }
    Ok(())
}

async fn drop_route_column_if_exists(pool: &SqlitePool, column_name: &str) -> anyhow::Result<()> {
    if column_exists(pool, "routes", column_name).await? {
        let sql = format!("ALTER TABLE routes DROP COLUMN {column_name}");
        sqlx::query(&sql).execute(pool).await?;
    }
    Ok(())
}

async fn ensure_route_column(
    pool: &SqlitePool,
    column_name: &str,
    definition: &str,
) -> anyhow::Result<()> {
    if !column_exists(pool, "routes", column_name).await? {
        let sql = format!("ALTER TABLE routes ADD COLUMN {column_name} {definition}");
        sqlx::query(&sql).execute(pool).await?;
    }

    Ok(())
}

/// Idempotent migration: upgrade request_logs from the legacy 21-column schema
/// to the spec-aligned 26-column schema.
///
/// Operations (all idempotent via column existence checks):
///   1. RENAME COLUMN (11 renames) — old → new names per spec.
///   2. ADD COLUMN (9 new columns).
///   3. Migrate created_at: TEXT datetime → INTEGER Unix ms.
///   4. Rebuild indexes with new names.
async fn migrate_logs_v2_spec_aligned(pool: &SqlitePool) -> anyhow::Result<()> {
    // Helper: rename a column in request_logs if the old name still exists.
    async fn rename_col(pool: &SqlitePool, old: &str, new: &str) -> anyhow::Result<()> {
        if column_exists(pool, "request_logs", old).await?
            && !column_exists(pool, "request_logs", new).await?
        {
            sqlx::query(&format!(
                "ALTER TABLE request_logs RENAME COLUMN {old} TO {new}"
            ))
            .execute(pool)
            .await?;
        }
        Ok(())
    }

    // ── Step 1: RENAME COLUMNS ─────────────────────────────────────────────
    // Handle intermediate state: an earlier version of this migration renamed
    // `created_at → timestamp`.  Roll it back to the canonical `created_at`.
    rename_col(pool, "timestamp", "created_at").await?;
    rename_col(pool, "ingress_protocol", "client_protocol").await?;
    rename_col(pool, "egress_protocol", "upstream_protocol").await?;
    rename_col(pool, "provider_name", "provider_id").await?;
    rename_col(pool, "request_model", "client_model").await?;
    rename_col(pool, "actual_model", "upstream_model").await?;
    rename_col(pool, "status_code", "client_status_code").await?;
    rename_col(pool, "duration_ms", "latency_total_ms").await?;
    rename_col(pool, "request_headers", "client_request_headers").await?;
    rename_col(pool, "request_body", "client_request_body").await?;
    rename_col(pool, "response_headers", "upstream_response_headers").await?;
    rename_col(pool, "response_body", "client_response_body").await?;

    // ── Step 2: Migrate created_at values ─────────────────────────────────
    if column_exists(pool, "request_logs", "created_at").await? {
        // 2a. NULL → 0 (rows inserted by older code paths that omitted created_at).
        sqlx::query("UPDATE request_logs SET created_at = 0 WHERE created_at IS NULL")
            .execute(pool)
            .await?;

        // 2b. ISO8601 TEXT (e.g. "2024-01-15 10:00:00") → INTEGER Unix milliseconds.
        //     Detect by checking whether any row holds a text value that starts with
        //     a 4-digit year (LIKE '____-%'), which unambiguously identifies ISO8601.
        let has_iso_ts: bool = sqlx::query_scalar::<_, i64>(
            "SELECT COUNT(*) FROM request_logs \
             WHERE typeof(created_at) = 'text' AND created_at LIKE '____-%' LIMIT 1",
        )
        .fetch_one(pool)
        .await
        .map(|n| n > 0)
        .unwrap_or(false);

        if has_iso_ts {
            sqlx::query(
                "UPDATE request_logs \
                 SET created_at = CAST(strftime('%s', created_at) AS INTEGER) * 1000 \
                 WHERE typeof(created_at) = 'text' AND created_at LIKE '____-%'",
            )
            .execute(pool)
            .await?;
        }
    }

    // ── Step 3: ADD new columns ────────────────────────────────────────────
    ensure_request_log_column(pool, "upstream_url", "TEXT").await?;
    ensure_request_log_column(pool, "client_response_headers", "TEXT").await?;
    ensure_request_log_column(pool, "upstream_request_headers", "TEXT").await?;
    ensure_request_log_column(pool, "upstream_request_body", "TEXT").await?;
    ensure_request_log_column(pool, "upstream_response_body", "TEXT").await?;
    ensure_request_log_column(pool, "upstream_status_code", "INTEGER").await?;
    ensure_request_log_column(pool, "latency_upstream_ms", "INTEGER").await?;
    ensure_request_log_column(pool, "stream_chunks_count", "INTEGER DEFAULT 0").await?;
    ensure_request_log_column(pool, "stream_first_chunk_ms", "INTEGER").await?;

    // ── Step 4: Rebuild indexes ────────────────────────────────────────────
    // Drop stale index left by the intermediate `timestamp`-column migration.
    sqlx::query("DROP INDEX IF EXISTS idx_logs_timestamp")
        .execute(pool)
        .await?;
    sqlx::query("CREATE INDEX IF NOT EXISTS idx_logs_created_at ON request_logs(created_at)")
        .execute(pool)
        .await?;
    sqlx::query("CREATE INDEX IF NOT EXISTS idx_logs_provider_id ON request_logs(provider_id)")
        .execute(pool)
        .await?;
    sqlx::query(
        "CREATE INDEX IF NOT EXISTS idx_logs_client_status ON request_logs(client_status_code)",
    )
    .execute(pool)
    .await?;
    sqlx::query(
        "CREATE INDEX IF NOT EXISTS idx_logs_upstream_model ON request_logs(upstream_model)",
    )
    .execute(pool)
    .await?;
    sqlx::query("CREATE INDEX IF NOT EXISTS idx_logs_api_key ON request_logs(api_key_id)")
        .execute(pool)
        .await?;
    sqlx::query(
        "CREATE INDEX IF NOT EXISTS idx_logs_client_protocol ON request_logs(client_protocol)",
    )
    .execute(pool)
    .await?;
    sqlx::query(
        "CREATE INDEX IF NOT EXISTS idx_logs_upstream_protocol ON request_logs(upstream_protocol)",
    )
    .execute(pool)
    .await?;

    Ok(())
}

async fn ensure_request_log_column(
    pool: &SqlitePool,
    column_name: &str,
    definition: &str,
) -> anyhow::Result<()> {
    if !column_exists(pool, "request_logs", column_name).await? {
        let sql = format!("ALTER TABLE request_logs ADD COLUMN {column_name} {definition}");
        sqlx::query(&sql).execute(pool).await?;
    }

    Ok(())
}

async fn ensure_api_key_column(
    pool: &SqlitePool,
    column_name: &str,
    definition: &str,
) -> anyhow::Result<()> {
    if !column_exists(pool, "api_keys", column_name).await? {
        let sql = format!("ALTER TABLE api_keys ADD COLUMN {column_name} {definition}");
        sqlx::query(&sql).execute(pool).await?;
    }

    Ok(())
}

async fn ensure_api_key_tables(pool: &SqlitePool) -> anyhow::Result<()> {
    sqlx::query(
        r#"CREATE TABLE IF NOT EXISTS api_keys (
            id          TEXT PRIMARY KEY,
            key         TEXT NOT NULL UNIQUE,
            name        TEXT NOT NULL,
            rpm         INTEGER,
            rpd         INTEGER,
            tpm         INTEGER,
            tpd         INTEGER,
            is_enabled  INTEGER DEFAULT 1,
            expires_at  TEXT,
            created_at  TEXT DEFAULT (datetime('now')),
            updated_at  TEXT DEFAULT (datetime('now'))
        )"#,
    )
    .execute(pool)
    .await?;

    sqlx::query(
        r#"CREATE TABLE IF NOT EXISTS api_key_routes (
            api_key_id  TEXT NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
            route_id    TEXT NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
            PRIMARY KEY (api_key_id, route_id)
        )"#,
    )
    .execute(pool)
    .await?;

    sqlx::query("CREATE INDEX IF NOT EXISTS idx_api_keys_key ON api_keys(key)")
        .execute(pool)
        .await?;
    sqlx::query(
        "CREATE INDEX IF NOT EXISTS idx_api_key_routes_route_id ON api_key_routes(route_id)",
    )
    .execute(pool)
    .await?;

    Ok(())
}

async fn ensure_route_targets_table(pool: &SqlitePool) -> anyhow::Result<()> {
    sqlx::query(
        r#"CREATE TABLE IF NOT EXISTS route_targets (
            id          TEXT PRIMARY KEY,
            route_id    TEXT NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
            provider_id TEXT NOT NULL REFERENCES providers(id),
            model       TEXT NOT NULL,
            weight      INTEGER DEFAULT 100,
            priority    INTEGER DEFAULT 1,
            created_at  TEXT DEFAULT (datetime('now'))
        )"#,
    )
    .execute(pool)
    .await?;

    sqlx::query("CREATE INDEX IF NOT EXISTS idx_route_targets_route_id ON route_targets(route_id)")
        .execute(pool)
        .await?;

    Ok(())
}

async fn migrate_provider_is_active_to_is_enabled(pool: &SqlitePool) -> anyhow::Result<()> {
    if column_exists(pool, "providers", "is_active").await? {
        sqlx::query("UPDATE providers SET is_enabled = is_active WHERE is_enabled IS NULL OR is_enabled != is_active AND is_active IS NOT NULL")
            .execute(pool)
            .await?;
    }
    Ok(())
}

async fn migrate_route_is_active_to_is_enabled(pool: &SqlitePool) -> anyhow::Result<()> {
    if column_exists(pool, "routes", "is_active").await? {
        sqlx::query("UPDATE routes SET is_enabled = is_active WHERE is_enabled IS NULL OR is_enabled != is_active AND is_active IS NOT NULL")
            .execute(pool)
            .await?;
    }
    Ok(())
}

async fn migrate_api_key_status_to_is_enabled(pool: &SqlitePool) -> anyhow::Result<()> {
    if column_exists(pool, "api_keys", "status").await? {
        sqlx::query(
            "UPDATE api_keys SET is_enabled = CASE WHEN status = 'active' THEN 1 ELSE 0 END \
             WHERE is_enabled IS NULL OR (status = 'active' AND is_enabled = 0) OR (status != 'active' AND is_enabled = 1)",
        )
        .execute(pool)
        .await?;
    }
    Ok(())
}

async fn backfill_route_fields(pool: &SqlitePool) -> anyhow::Result<()> {
    if column_exists(pool, "models", "balance").await? {
        sqlx::query(
            "UPDATE models SET balance = 'weighted' WHERE balance IS NULL OR trim(balance) = ''",
        )
        .execute(pool)
        .await?;
    } else if column_exists(pool, "routes", "strategy").await? {
        sqlx::query(
            "UPDATE routes SET strategy = 'weighted' WHERE strategy IS NULL OR trim(strategy) = ''",
        )
        .execute(pool)
        .await?;
    }
    drop_route_column_if_exists(pool, "route_type").await?;

    Ok(())
}

async fn backfill_route_targets(pool: &SqlitePool) -> anyhow::Result<()> {
    sqlx::query(
        r#"
        INSERT INTO route_targets (id, route_id, provider_id, model, weight, priority)
        SELECT lower(hex(randomblob(16))), r.id, r.target_provider, r.target_model, 100, 1
        FROM routes r
        WHERE r.target_provider IS NOT NULL
          AND trim(r.target_provider) != ''
          AND NOT EXISTS (
              SELECT 1 FROM route_targets rt WHERE rt.route_id = r.id
          )
        "#,
    )
    .execute(pool)
    .await?;

    Ok(())
}

async fn ensure_oauth_credentials_table(pool: &SqlitePool) -> anyhow::Result<()> {
    sqlx::query(
        r#"
        CREATE TABLE IF NOT EXISTS provider_oauth_credentials (
            provider_id       TEXT PRIMARY KEY REFERENCES providers(id) ON DELETE CASCADE,
            driver_key        TEXT NOT NULL DEFAULT '',
            scheme            TEXT NOT NULL DEFAULT '',
            access_token      TEXT NOT NULL DEFAULT '',
            refresh_token     TEXT,
            expires_at        TEXT,
            resource_url      TEXT,
            subject_id        TEXT,
            scopes            TEXT NOT NULL DEFAULT '[]',
            meta              TEXT NOT NULL DEFAULT '{}',
            status            TEXT NOT NULL DEFAULT 'connected',
            status_version    INTEGER NOT NULL DEFAULT 0,
            last_error        TEXT,
            last_refresh_at   TEXT,
            created_at        TEXT DEFAULT (datetime('now')),
            updated_at        TEXT DEFAULT (datetime('now'))
        )
        "#,
    )
    .execute(pool)
    .await?;
    Ok(())
}

async fn migrate_oauth_credentials_from_providers(pool: &SqlitePool) -> anyhow::Result<()> {
    if !column_exists(pool, "providers", "access_token").await? {
        return Ok(());
    }
    sqlx::query(
        r#"
        INSERT OR IGNORE INTO provider_oauth_credentials
            (provider_id, access_token, refresh_token, expires_at, status)
        SELECT id, COALESCE(access_token, ''), refresh_token, expires_at, 'connected'
        FROM providers
        WHERE auth_mode = 'oauth'
          AND (
            (access_token IS NOT NULL AND access_token != '')
            OR (refresh_token IS NOT NULL AND refresh_token != '')
          )
        "#,
    )
    .execute(pool)
    .await?;
    Ok(())
}

async fn column_exists(
    pool: &SqlitePool,
    table_name: &str,
    column_name: &str,
) -> anyhow::Result<bool> {
    let pragma = format!("PRAGMA table_info({table_name})");
    let rows = sqlx::query(&pragma).fetch_all(pool).await?;
    Ok(rows.iter().any(|row| {
        row.try_get::<String, _>("name")
            .map(|name| name == column_name)
            .unwrap_or(false)
    }))
}

async fn table_exists(pool: &SqlitePool, table_name: &str) -> anyhow::Result<bool> {
    let rows = sqlx::query(&format!("PRAGMA table_info({table_name})"))
        .fetch_all(pool)
        .await?;
    Ok(!rows.is_empty())
}

async fn rename_table_if_needed(pool: &SqlitePool, old: &str, new: &str) -> anyhow::Result<()> {
    if table_exists(pool, old).await? && !table_exists(pool, new).await? {
        tracing::info!("renaming table {old} -> {new}");
        sqlx::query(&format!("ALTER TABLE {old} RENAME TO {new}"))
            .execute(pool)
            .await?;
    }
    Ok(())
}

async fn rename_column_if_needed(
    pool: &SqlitePool,
    table: &str,
    old: &str,
    new: &str,
) -> anyhow::Result<()> {
    if column_exists(pool, table, old).await? && !column_exists(pool, table, new).await? {
        tracing::info!("renaming column {table}.{old} -> {table}.{new}");
        sqlx::query(&format!("ALTER TABLE {table} RENAME COLUMN {old} TO {new}"))
            .execute(pool)
            .await?;
    }
    Ok(())
}

const INIT_SQL: &str = r#"
CREATE TABLE IF NOT EXISTS providers (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    vendor      TEXT,
    protocol    TEXT NOT NULL,
    base_url    TEXT NOT NULL,
    preset_key  TEXT,
    channel     TEXT,
    models_source TEXT,
    static_models TEXT,
    api_key     TEXT NOT NULL,
    auth_mode   TEXT NOT NULL DEFAULT 'apikey' CHECK (auth_mode IN ('apikey', 'oauth')),
    access_token TEXT,
    refresh_token TEXT,
    expires_at  TEXT,
    use_proxy   INTEGER DEFAULT 0,
    last_test_success INTEGER,
    last_test_at TEXT,
    is_enabled  INTEGER DEFAULT 1,
    priority    INTEGER DEFAULT 0,
    created_at  TEXT DEFAULT (datetime('now')),
    updated_at  TEXT DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS routes (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    virtual_model     TEXT,
    balance           TEXT DEFAULT 'weighted',
    target_provider   TEXT NOT NULL REFERENCES providers(id),
    target_model      TEXT NOT NULL,
    access_control    INTEGER DEFAULT 0,
    is_enabled        INTEGER DEFAULT 1,
    priority          INTEGER DEFAULT 0,
    created_at        TEXT DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS route_targets (
    id          TEXT PRIMARY KEY,
    route_id    TEXT NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
    provider_id TEXT NOT NULL REFERENCES providers(id),
    model       TEXT NOT NULL,
    weight      INTEGER DEFAULT 100,
    priority    INTEGER DEFAULT 1,
    created_at  TEXT DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_route_targets_route_id ON route_targets(route_id);

CREATE TABLE IF NOT EXISTS request_logs (
    id                        TEXT PRIMARY KEY,
    created_at                INTEGER NOT NULL DEFAULT 0,
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
    latency_total_ms          INTEGER,
    latency_upstream_ms       INTEGER,
    input_tokens              INTEGER DEFAULT 0,
    output_tokens             INTEGER DEFAULT 0,
    cache_read_tokens         INTEGER DEFAULT 0,
    is_stream                 INTEGER DEFAULT 0,
    stream_chunks_count       INTEGER DEFAULT 0,
    stream_first_chunk_ms     INTEGER
);

CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT DEFAULT (datetime('now'))
);
"#;
