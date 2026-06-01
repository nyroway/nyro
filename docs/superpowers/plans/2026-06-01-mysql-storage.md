# MySQL Storage Support Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add MySQL 8.0+ as a fourth storage backend for Nyro AI Gateway.

**Architecture:** Follow the existing trait-based repository pattern. A new `MysqlStorage` implements the same `Storage` trait used by SQLite/PostgreSQL, with fully independent SQL strings using MySQL-specific syntax. A prerequisite refactoring renames `key` columns to eliminate the MySQL reserved word conflict.

**Tech Stack:** Rust, SQLx 0.8 (mysql feature), async-trait, tokio.

---

## Phase 0: Prerequisite — Rename `key` Columns

`key` is a MySQL reserved word. Rename across all backends before adding MySQL.

**Files:**
- Modify: `crates/nyro-core/src/db/models.rs:176-206`
- Modify: `crates/nyro-core/src/db/mod.rs` (DDL + migration)
- Modify: `crates/nyro-core/src/storage/sqlite/mod.rs` (SQL strings + field access)
- Modify: `crates/nyro-core/src/storage/postgres/mod.rs` (SQL strings + DDL + migration + field access)

### Task 1: Rename `ApiKey.key` → `ApiKey.token` in model structs

**Files:**
- Modify: `crates/nyro-core/src/db/models.rs:176-206`

- [ ] **Step 1: Update `ApiKey` struct**

In `crates/nyro-core/src/db/models.rs`, change line 179:

```rust
// Before:
pub key: String,

// After:
#[serde(rename = "key")]
pub token: String,
```

The `#[serde(rename = "key")]` keeps the JSON API field name as `"key"` for backward compatibility.

- [ ] **Step 2: Update `ApiKeyWithBindings` struct**

In the same file, change line 194:

```rust
// Before:
pub key: String,

// After:
#[serde(rename = "key")]
pub token: String,
```

- [ ] **Step 3: Verify compilation**

Run: `cargo check -p nyro-core 2>&1 | head -40`
Expected: Compile errors in files referencing `.key` on `ApiKey` or `ApiKeyWithBindings` — this is expected, we fix them next.

- [ ] **Step 4: Commit**

```bash
git add crates/nyro-core/src/db/models.rs
git commit -m "refactor(models): rename ApiKey.key → ApiKey.token for MySQL compat"
```

### Task 2: Update SQLite storage — `key` → `token` / `name`

**Files:**
- Modify: `crates/nyro-core/src/storage/sqlite/mod.rs`
- Modify: `crates/nyro-core/src/db/mod.rs`

- [ ] **Step 1: Update SQLite storage SQL strings and field access**

In `crates/nyro-core/src/storage/sqlite/mod.rs`, make these replacements:

1. Line 683 — `list()` API key SELECT:
   ```rust
   // Before:
   "SELECT id, key, name, rpm, rpd, tpm, tpd, COALESCE(is_enabled, 1) AS is_enabled, expires_at, created_at, updated_at FROM api_keys ORDER BY created_at DESC"
   // After:
   "SELECT id, token, name, rpm, rpd, tpm, tpd, COALESCE(is_enabled, 1) AS is_enabled, expires_at, created_at, updated_at FROM api_keys ORDER BY created_at DESC"
   ```

2. Line 693 — field access in `list()`:
   ```rust
   // Before:
   key: row.key,
   // After:
   token: row.token,
   ```

3. Line 711 — `get()` API key SELECT:
   ```rust
   // Before:
   "SELECT id, key, name, rpm, rpd, tpm, tpd, COALESCE(is_enabled, 1) AS is_enabled, expires_at, created_at, updated_at FROM api_keys WHERE id = ?"
   // After:
   "SELECT id, token, name, rpm, rpd, tpm, tpd, COALESCE(is_enabled, 1) AS is_enabled, expires_at, created_at, updated_at FROM api_keys WHERE id = ?"
   ```

4. Line 723 — field access in `get()`:
   ```rust
   // Before:
   key: row.key,
   // After:
   token: row.token,
   ```

5. Line 741 — `create()` INSERT:
   ```rust
   // Before:
   "INSERT INTO api_keys (id, key, name, rpm, rpd, tpm, tpd, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
   // After:
   "INSERT INTO api_keys (id, token, name, rpm, rpd, tpm, tpd, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"
   ```

6. Line 760 — `update()` SELECT:
   ```rust
   // Before:
   "SELECT id, key, name, rpm, rpd, tpm, tpd, COALESCE(is_enabled, 1) AS is_enabled, expires_at, created_at, updated_at FROM api_keys WHERE id = ?"
   // After:
   "SELECT id, token, name, rpm, rpd, tpm, tpd, COALESCE(is_enabled, 1) AS is_enabled, expires_at, created_at, updated_at FROM api_keys WHERE id = ?"
   ```

7. Line 847 — `find_api_key()` in `AuthAccessStore`:
   ```rust
   // Before:
   "SELECT id, COALESCE(name, '') AS name, COALESCE(is_enabled, 1) AS is_enabled, expires_at, rpm, rpd, tpm, tpd FROM api_keys WHERE key = ?"
   // After:
   "SELECT id, COALESCE(name, '') AS name, COALESCE(is_enabled, 1) AS is_enabled, expires_at, rpm, rpd, tpm, tpd FROM api_keys WHERE token = ?"
   ```

8. Lines 647, 656, 667 — `SettingsStore` SQL (`settings.key` → `settings.name`):
   ```rust
   // get() — line 647:
   // Before: "SELECT value FROM settings WHERE key = ?"
   // After:  "SELECT value FROM settings WHERE name = ?"

   // set() — line 656:
   // Before: "INSERT INTO settings (key, value, updated_at) VALUES (?, ?, datetime('now')) ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at"
   // After:  "INSERT INTO settings (name, value, updated_at) VALUES (?, ?, datetime('now')) ON CONFLICT(name) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at"

   // list_all() — line 667:
   // Before: "SELECT key, value FROM settings"
   // After:  "SELECT name, value FROM settings"
   ```

- [ ] **Step 2: Update SQLite DDL in `db/mod.rs`**

In the `INIT_SQL` constant and migration code in `crates/nyro-core/src/db/mod.rs`:

1. In `INIT_SQL` — `api_keys` table DDL:
   ```sql
   -- Before: key TEXT NOT NULL UNIQUE
   -- After:  token TEXT NOT NULL UNIQUE
   ```

2. In `INIT_SQL` — `settings` table DDL:
   ```sql
   -- Before: key TEXT PRIMARY KEY
   -- After:  name TEXT PRIMARY KEY
   ```

3. In `INIT_SQL` — index name:
   ```sql
   -- Before: CREATE INDEX IF NOT EXISTS idx_api_keys_key ON api_keys(key)
   -- After:  CREATE INDEX IF NOT EXISTS idx_api_keys_token ON api_keys(token)
   ```

4. Add migration steps for existing databases (after the existing migrations, before the final `Ok(())`):
   ```rust
   // Rename settings.key → settings.name
   rename_column_if_needed(&pool, "settings", "key", "name").await?;

   // Rename api_keys.key → api_keys.token
   rename_column_if_needed(&pool, "api_keys", "key", "token").await?;
   ```

   Also add a helper `rename_column_if_needed` if not already present (check if `rename_column_if_needed` exists — it does in `db/mod.rs` already for SQLite).

5. Update the settings key migration line (around line 110):
   ```rust
   // Before:
   "UPDATE settings SET key = 'enable_payload' WHERE key = 'log_record_payloads'"
   // After:
   "UPDATE settings SET name = 'enable_payload' WHERE name = 'log_record_payloads'"
   ```

- [ ] **Step 3: Verify compilation**

Run: `cargo check -p nyro-core 2>&1 | head -40`
Expected: PASS (no errors)

- [ ] **Step 4: Commit**

```bash
git add crates/nyro-core/src/storage/sqlite/mod.rs crates/nyro-core/src/db/mod.rs
git commit -m "refactor(sqlite): rename key columns for MySQL compat"
```

### Task 3: Update PostgreSQL storage — `key` → `token` / `name`

**Files:**
- Modify: `crates/nyro-core/src/storage/postgres/mod.rs`

- [ ] **Step 1: Update PostgreSQL SQL strings and field access**

In `crates/nyro-core/src/storage/postgres/mod.rs`, make these replacements:

1. `api_key_select()` helper (line 1515):
   ```rust
   // Before:
   "SELECT id, key, name, rpm, rpd, tpm, tpd, ..."
   // After:
   "SELECT id, token, name, rpm, rpd, tpm, tpd, ..."
   ```

2. `api_key_with_bindings()` helper (line 1529):
   ```rust
   // Before:
   key: row.key,
   // After:
   token: row.token,
   ```

3. `create()` INSERT (line 680):
   ```rust
   // Before:
   "INSERT INTO api_keys (id, key, name, rpm, rpd, tpm, tpd, expires_at) VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::timestamptz)"
   // After:
   "INSERT INTO api_keys (id, token, name, rpm, rpd, tpm, tpd, expires_at) VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::timestamptz)"
   ```

4. `find_api_key()` in AuthAccessStore (line 780):
   ```rust
   // Before:
   "...FROM api_keys WHERE key = $1"
   // After:
   "...FROM api_keys WHERE token = $1"
   ```

5. `SettingsStore::get()` (line 618):
   ```rust
   // Before: "SELECT value FROM settings WHERE key = $1"
   // After:  "SELECT value FROM settings WHERE name = $1"
   ```

6. `SettingsStore::set()` (line 627):
   ```rust
   // Before: "INSERT INTO settings (key, value, updated_at) VALUES ($1, $2, CURRENT_TIMESTAMP) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value, updated_at=EXCLUDED.updated_at"
   // After:  "INSERT INTO settings (name, value, updated_at) VALUES ($1, $2, CURRENT_TIMESTAMP) ON CONFLICT(name) DO UPDATE SET value=EXCLUDED.value, updated_at=EXCLUDED.updated_at"
   ```

7. `SettingsStore::list_all()` (line 638):
   ```rust
   // Before: "SELECT key, value FROM settings"
   // After:  "SELECT name, value FROM settings"
   ```

- [ ] **Step 2: Update PostgreSQL DDL in `POSTGRES_INIT_SQL`**

In the `POSTGRES_INIT_SQL` constant (lines 1594-1736):

1. `settings` table:
   ```sql
   -- Before: key TEXT PRIMARY KEY
   -- After:  name TEXT PRIMARY KEY
   ```

2. `api_keys` table:
   ```sql
   -- Before: key TEXT NOT NULL UNIQUE
   -- After:  token TEXT NOT NULL UNIQUE
   ```

3. Index:
   ```sql
   -- Before: CREATE INDEX IF NOT EXISTS idx_api_keys_key ON api_keys(key)
   -- After:  CREATE INDEX IF NOT EXISTS idx_api_keys_token ON api_keys(token)
   ```

- [ ] **Step 3: Add PostgreSQL migration steps**

In `PostgresBootstrap::migrate()` (after the existing migrations, before the final `Ok(())` at line 1269), add:

```rust
// Rename settings.key → settings.name
pg_rename_column_if_needed(self.adapter.pool(), "settings", "key", "name").await?;

// Rename api_keys.key → api_keys.token
pg_rename_column_if_needed(self.adapter.pool(), "api_keys", "key", "token").await?;
```

Also update the settings key rename migration (line 1264):
```rust
// Before:
"UPDATE settings SET key = 'enable_payload' WHERE key = 'log_record_payloads'"
// After:
"UPDATE settings SET name = 'enable_payload' WHERE name = 'log_record_payloads'"
```

- [ ] **Step 4: Verify compilation**

Run: `cargo check -p nyro-core 2>&1 | head -40`
Expected: PASS

- [ ] **Step 5: Run existing tests**

Run: `cargo test -p nyro-core 2>&1 | tail -20`
Expected: All tests pass.

- [ ] **Step 6: Commit**

```bash
git add crates/nyro-core/src/storage/postgres/mod.rs
git commit -m "refactor(postgres): rename key columns for MySQL compat"
```

---

## Phase 1: Infrastructure — SQL Abstraction Layer

### Task 4: Add MySQL feature to SQLx and update abstraction layer

**Files:**
- Modify: `Cargo.toml:16`
- Modify: `crates/nyro-core/src/storage/sql/config.rs`
- Modify: `crates/nyro-core/src/storage/sql/dialect.rs`
- Modify: `crates/nyro-core/src/storage/sql/query.rs`

- [ ] **Step 1: Add `mysql` feature to workspace `Cargo.toml`**

In `Cargo.toml`, line 16:

```toml
# Before:
sqlx = { version = "0.8", features = ["runtime-tokio", "sqlite", "postgres", "tls-rustls"] }
# After:
sqlx = { version = "0.8", features = ["runtime-tokio", "sqlite", "postgres", "mysql", "tls-rustls"] }
```

- [ ] **Step 2: Add `Mysql` to `SqlBackendKind`**

In `crates/nyro-core/src/storage/sql/config.rs`:

```rust
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SqlBackendKind {
    Sqlite,
    Postgres,
    Mysql,
}
```

- [ ] **Step 3: Add `Mysql` to `SqlDialect`**

In `crates/nyro-core/src/storage/sql/dialect.rs`:

```rust
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SqlDialect {
    Sqlite,
    Postgres,
    Mysql,
}

impl SqlDialect {
    pub fn placeholder(self, index: usize) -> String {
        match self {
            SqlDialect::Postgres => format!("${index}"),
            SqlDialect::Sqlite | SqlDialect::Mysql => "?".to_string(),
        }
    }

    pub fn supports_returning(self) -> bool {
        matches!(self, SqlDialect::Sqlite | SqlDialect::Postgres)
    }

    pub fn upsert_keyword(self) -> &'static str {
        match self {
            SqlDialect::Sqlite | SqlDialect::Postgres => "ON CONFLICT",
            SqlDialect::Mysql => "ON DUPLICATE KEY UPDATE",
        }
    }
}
```

- [ ] **Step 4: Add `Mysql` arms to query helpers**

In `crates/nyro-core/src/storage/sql/query.rs`:

```rust
pub fn pagination_clause(
    dialect: SqlDialect,
    _pagination: &Pagination,
    next_bind_index: usize,
) -> String {
    match dialect {
        SqlDialect::Postgres => {
            format!(
                " LIMIT {} OFFSET {}",
                dialect.placeholder(next_bind_index),
                dialect.placeholder(next_bind_index + 1)
            )
        }
        SqlDialect::Sqlite | SqlDialect::Mysql => " LIMIT ? OFFSET ?".to_string(),
    }
}

pub fn now_expr(dialect: SqlDialect) -> &'static str {
    match dialect {
        SqlDialect::Sqlite => "datetime('now')",
        SqlDialect::Postgres => "CURRENT_TIMESTAMP",
        SqlDialect::Mysql => "NOW()",
    }
}
```

- [ ] **Step 5: Verify compilation**

Run: `cargo check -p nyro-core 2>&1 | head -40`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add Cargo.toml crates/nyro-core/src/storage/sql/config.rs crates/nyro-core/src/storage/sql/dialect.rs crates/nyro-core/src/storage/sql/query.rs
git commit -m "feat(storage): add MySQL to SQL abstraction layer"
```

### Task 5: Update connection pool to support MySQL

**Files:**
- Modify: `crates/nyro-core/src/storage/sql/pool.rs`

- [ ] **Step 1: Add `Mysql` variant to `RelationalPool`**

Replace the entire file content:

```rust
use anyhow::Context;
use sqlx::mysql::MySqlPoolOptions;
use sqlx::postgres::PgPoolOptions;
use sqlx::sqlite::SqlitePoolOptions;
use sqlx::{MySql, Pool, Postgres, Sqlite};

use super::config::{SqlBackendConfig, SqlBackendKind};
use super::dialect::SqlDialect;

#[derive(Clone)]
pub enum RelationalPool {
    Sqlite(Pool<Sqlite>),
    Postgres(Pool<Postgres>),
    Mysql(Pool<MySql>),
}

impl RelationalPool {
    pub async fn connect(kind: SqlBackendKind, cfg: &SqlBackendConfig) -> anyhow::Result<Self> {
        match kind {
            SqlBackendKind::Sqlite => {
                let pool = SqlitePoolOptions::new()
                    .max_connections(cfg.max_connections)
                    .min_connections(cfg.min_connections)
                    .acquire_timeout(cfg.acquire_timeout)
                    .idle_timeout(cfg.idle_timeout)
                    .connect(&cfg.url)
                    .await
                    .with_context(|| format!("failed to connect sqlite: {}", cfg.url))?;
                Ok(Self::Sqlite(pool))
            }
            SqlBackendKind::Postgres => {
                let pool = PgPoolOptions::new()
                    .max_connections(cfg.max_connections)
                    .min_connections(cfg.min_connections)
                    .acquire_timeout(cfg.acquire_timeout)
                    .idle_timeout(cfg.idle_timeout)
                    .connect(&cfg.url)
                    .await
                    .with_context(|| format!("failed to connect postgres: {}", cfg.url))?;
                Ok(Self::Postgres(pool))
            }
            SqlBackendKind::Mysql => {
                let pool = MySqlPoolOptions::new()
                    .max_connections(cfg.max_connections)
                    .min_connections(cfg.min_connections)
                    .acquire_timeout(cfg.acquire_timeout)
                    .idle_timeout(cfg.idle_timeout)
                    .connect(&cfg.url)
                    .await
                    .with_context(|| format!("failed to connect mysql: {}", cfg.url))?;
                Ok(Self::Mysql(pool))
            }
        }
    }

    pub fn dialect(&self) -> SqlDialect {
        match self {
            RelationalPool::Sqlite(_) => SqlDialect::Sqlite,
            RelationalPool::Postgres(_) => SqlDialect::Postgres,
            RelationalPool::Mysql(_) => SqlDialect::Mysql,
        }
    }

    pub async fn ping(&self) -> anyhow::Result<()> {
        match self {
            RelationalPool::Sqlite(pool) => {
                sqlx::query("SELECT 1").execute(pool).await?;
            }
            RelationalPool::Postgres(pool) => {
                sqlx::query("SELECT 1").execute(pool).await?;
            }
            RelationalPool::Mysql(pool) => {
                sqlx::query("SELECT 1").execute(pool).await?;
            }
        }
        Ok(())
    }

    pub async fn close(self) {
        match self {
            RelationalPool::Sqlite(pool) => pool.close().await,
            RelationalPool::Postgres(pool) => pool.close().await,
            RelationalPool::Mysql(pool) => pool.close().await,
        }
    }

    pub fn as_postgres(&self) -> Option<&Pool<Postgres>> {
        match self {
            RelationalPool::Postgres(pool) => Some(pool),
            _ => None,
        }
    }

    pub fn as_mysql(&self) -> Option<&Pool<MySql>> {
        match self {
            RelationalPool::Mysql(pool) => Some(pool),
            _ => None,
        }
    }
}
```

**Note:** The `SqlBackendConfig` currently has `acquire_timeout` and `max_lifetime` fields. Per the approved plan, these should be removed. But the `SqlitePoolOptions` and `PgPoolOptions` in existing code use `acquire_timeout`. For now, keep `acquire_timeout` in `SqlBackendConfig` to avoid breaking the existing SQLite/Postgres pool setup. The removal of `acquire_timeout` and `max_lifetime` from `SqlStorageConfig` (the public config) will happen in Task 6.

- [ ] **Step 2: Verify compilation**

Run: `cargo check -p nyro-core 2>&1 | head -40`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add crates/nyro-core/src/storage/sql/pool.rs
git commit -m "feat(pool): add MySQL connection pool variant"
```

---

## Phase 2: Configuration

### Task 6: Add MySQL to config and storage module

**Files:**
- Modify: `crates/nyro-core/src/config.rs`
- Modify: `crates/nyro-core/src/storage/mod.rs`

- [ ] **Step 1: Update `StorageBackendKind` and `GatewayStorageConfig`**

In `crates/nyro-core/src/config.rs`:

```rust
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum StorageBackendKind {
    #[default]
    Sqlite,
    Postgres,
    Mysql,
}

// ... SqliteStorageConfig unchanged ...

// Remove acquire_timeout and max_lifetime from SqlStorageConfig:
#[derive(Debug, Clone)]
pub struct SqlStorageConfig {
    pub url: Option<String>,
    pub max_connections: u32,
    pub min_connections: u32,
    pub idle_timeout: Option<Duration>,
}

impl Default for SqlStorageConfig {
    fn default() -> Self {
        Self {
            url: None,
            max_connections: 10,
            min_connections: 1,
            idle_timeout: Some(Duration::from_secs(300)),
        }
    }
}

#[derive(Debug, Clone)]
pub struct GatewayStorageConfig {
    pub backend: StorageBackendKind,
    pub sqlite: SqliteStorageConfig,
    pub postgres: SqlStorageConfig,
    pub mysql: SqlStorageConfig,
}

impl Default for GatewayStorageConfig {
    fn default() -> Self {
        Self {
            backend: StorageBackendKind::Sqlite,
            sqlite: SqliteStorageConfig::default(),
            postgres: SqlStorageConfig::default(),
            mysql: SqlStorageConfig::default(),
        }
    }
}
```

**Note:** Removing `acquire_timeout` and `max_lifetime` from `SqlStorageConfig` means `build_storage_config()` in `main.rs` and `to_sql_backend_config()` in `lib.rs` must also be updated. The `SqlBackendConfig` (internal) keeps `acquire_timeout` with a hardcoded default.

- [ ] **Step 2: Update `SqlBackendConfig` to remove `max_lifetime` and hardcode `acquire_timeout`**

In `crates/nyro-core/src/storage/sql/config.rs`:

```rust
use std::time::Duration;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SqlBackendKind {
    Sqlite,
    Postgres,
    Mysql,
}

#[derive(Debug, Clone)]
pub struct SqlBackendConfig {
    pub url: String,
    pub max_connections: u32,
    pub min_connections: u32,
    pub acquire_timeout: Duration,
    pub idle_timeout: Option<Duration>,
}

impl SqlBackendConfig {
    pub fn with_url(url: impl Into<String>) -> Self {
        Self {
            url: url.into(),
            ..Self::default()
        }
    }
}

impl Default for SqlBackendConfig {
    fn default() -> Self {
        Self {
            url: String::new(),
            max_connections: 10,
            min_connections: 1,
            acquire_timeout: Duration::from_secs(10),
            idle_timeout: Some(Duration::from_secs(300)),
        }
    }
}
```

- [ ] **Step 3: Update `to_sql_backend_config` in `lib.rs`**

In `crates/nyro-core/src/lib.rs`, update the `to_sql_backend_config` function:

```rust
fn to_sql_backend_config(
    config: &SqlStorageConfig,
    backend: &str,
) -> anyhow::Result<SqlBackendConfig> {
    let url = config
        .configured_url()
        .with_context(|| format!("{backend} backend selected but storage url is empty"))?;
    Ok(SqlBackendConfig {
        url,
        max_connections: config.max_connections,
        min_connections: config.min_connections,
        idle_timeout: config.idle_timeout,
        ..Default::default()
    })
}
```

- [ ] **Step 4: Register MySQL module in storage**

In `crates/nyro-core/src/storage/mod.rs`:

```rust
pub mod memory;
pub mod mysql;
pub mod postgres;
pub mod sql;
pub mod sqlite;
pub mod traits;

pub use memory::MemoryStorage;
pub use mysql::MysqlStorage;
pub use postgres::PostgresStorage;
pub use sqlite::SqliteStorage;
pub use traits::{
    ApiKeyAccessRecord, ApiKeyStore, AuthAccessStore, DynStorage, LogStore, ModelBackendStore,
    ModelSnapshotStore, ModelStore, ProviderStore, SettingsStore, Storage, StorageBootstrap,
    UsageWindow,
};
```

- [ ] **Step 5: Verify compilation (expect error — mysql.rs doesn't exist yet)**

Run: `cargo check -p nyro-core 2>&1 | head -20`
Expected: Error about missing `mysql.rs` — this is expected. We create it in Task 8.

- [ ] **Step 6: Commit**

```bash
git add crates/nyro-core/src/config.rs crates/nyro-core/src/storage/mod.rs crates/nyro-core/src/storage/sql/config.rs crates/nyro-core/src/lib.rs
git commit -m "feat(config): add Mysql backend kind and streamline SqlStorageConfig"
```

---

## Phase 3: Core Implementation — `MysqlStorage`

### Task 7: Create `mysql.rs` — struct definitions, adapter, and `Storage` trait impl

**Files:**
- Create: `crates/nyro-core/src/storage/mysql.rs`

This is the largest task. The file is ~1700 lines, modeled on `postgres/mod.rs`.

- [ ] **Step 1: Create the file with imports, adapter, storage struct, and `Storage` trait impl**

Write the file `crates/nyro-core/src/storage/mysql.rs` with the full implementation. The complete file content is derived from `postgres/mod.rs` with these systematic changes:

- `Pool<Postgres>` → `Pool<MySql>` everywhere
- `sqlx::Postgres` → `sqlx::MySql`
- `$1`, `$2` placeholders → `?` placeholders
- `CURRENT_TIMESTAMP` → `NOW()`
- `TIMESTAMPTZ` → `DATETIME`
- `BOOLEAN` → `TINYINT(1)`
- `TEXT` PK types → `VARCHAR(36)` for IDs, `VARCHAR(255)` for names
- `BIGINT` / `INTEGER` → same in MySQL
- `COALESCE(is_enabled, TRUE)` → `COALESCE(is_enabled, 1)`
- `COALESCE(use_proxy, FALSE)` → `COALESCE(use_proxy, 0)`
- `COALESCE(is_stream, FALSE)` → `COALESCE(is_stream, 0)`
- `ON CONFLICT(...) DO UPDATE SET ...=EXCLUDED.xxx` → `ON DUPLICATE KEY UPDATE ...=VALUES(xxx)`
- `INSERT ... ON CONFLICT DO NOTHING` → `INSERT IGNORE ...`
- `to_char(col AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS')` → `DATE_FORMAT(col, '%Y-%m-%d %H:%i:%S')`
- `EXTRACT(EPOCH FROM CURRENT_TIMESTAMP - INTERVAL '...') * 1000` → `UNIX_TIMESTAMP(NOW() - INTERVAL ? SECOND) * 1000`
- `NULL::text AS col` → `CAST(NULL AS CHAR) AS col`
- `col::BIGINT` → `col` (BIGINT is already BIGINT in MySQL, no cast needed)
- `col::FLOAT8` → `col` (AVG returns DOUBLE natively)
- `NULLIF($8, '')::timestamptz` → `NULLIF(?, '')`
- `to_char(date_trunc('hour', to_timestamp(created_at/1000) AT TIME ZONE 'UTC'), 'YYYY-MM-DD HH24:00:00')` → `DATE_FORMAT(FROM_UNIXTIME(created_at/1000), '%Y-%m-%d %H:00:00')`
- `strftime('%Y-%m-%d %H:00:00', datetime(created_at/1000, 'unixepoch'))` → `DATE_FORMAT(FROM_UNIXTIME(created_at/1000), '%Y-%m-%d %H:00:00')`
- `datetime(expires_at) <= datetime('now', '+' || ? || ' seconds')` → `expires_at <= NOW() + INTERVAL ? SECOND`
- `datetime(updated_at, '+' || ? || ' seconds') < datetime('now')` → `updated_at + INTERVAL ? SECOND < NOW()`
- `settings.key` → `settings.name`
- `api_keys.key` → `api_keys.token`
- `EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() ...)` → `EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = DATABASE() ...)`
- `pg_constraint` checks → MySQL `information_schema.table_constraints`
- `ALTER TABLE ... ADD COLUMN IF NOT EXISTS` → check with `information_schema.columns` first
- Table charset: `CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci` on every CREATE TABLE
- Index creation: use `information_schema.statistics` check pattern

**Key helper functions to include (mirroring PostgreSQL):**
- `mysql_column_exists(pool, table, column) -> bool` via `information_schema.columns WHERE table_schema = DATABASE()`
- `mysql_table_exists(pool, table) -> bool` via `information_schema.tables WHERE table_schema = DATABASE()`
- `mysql_rename_table_if_needed(pool, old, new)` via `RENAME TABLE old TO new`
- `mysql_rename_column_if_needed(pool, table, old, new)` via `ALTER TABLE t RENAME COLUMN old TO new`
- `provider_select(suffix)`, `model_select(suffix)`, `api_key_select(suffix)` — SQL fragment builders
- `api_key_with_bindings(row, model_ids)` — struct mapping
- `normalize_provider_vendor(vendor)` — same logic as PostgreSQL
- `interval_expr(window)` — same logic
- `list_api_key_model_ids(pool, id)` — with `?` placeholders
- `replace_api_key_models(pool, id, model_ids)` — with `INSERT IGNORE`
- `normalize_provider_protocols_mysql(pool)` — MySQL variant
- `base_url_from_protocol_endpoints(raw, protocol)` — identical to PostgreSQL

- [ ] **Step 2: Verify compilation**

Run: `cargo check -p nyro-core 2>&1 | head -40`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add crates/nyro-core/src/storage/mysql.rs
git commit -m "feat(storage): add MySQL storage implementation"
```

---

## Phase 4: Integration

### Task 8: Wire MySQL into Gateway and CLI

**Files:**
- Modify: `crates/nyro-core/src/lib.rs`
- Modify: `src-server/src/main.rs`

- [ ] **Step 1: Add MySQL to `RuntimeStorageKind` and Gateway bootstrap**

In `crates/nyro-core/src/lib.rs`:

1. Add import:
   ```rust
   use sqlx::{MySql, Pool, Postgres, SqlitePool};
   ```

2. Add `Mysql` variant:
   ```rust
   #[derive(Clone, Copy, Debug, PartialEq, Eq)]
   pub enum RuntimeStorageKind {
       Memory,
       Sqlite,
       Postgres,
       Mysql,
   }
   ```

3. Add pool field to `Gateway`:
   ```rust
   #[allow(dead_code)]
   mysql_pool: Option<Pool<MySql>>,
   ```

4. Update `Gateway::new()` match block to handle MySQL:
   ```rust
   let (storage_kind, storage, sqlite_pool, postgres_pool, mysql_pool): (
       RuntimeStorageKind,
       DynStorage,
       Option<SqlitePool>,
       Option<Pool<Postgres>>,
       Option<Pool<MySql>>,
   ) = match config.storage.backend {
       StorageBackendKind::Sqlite => {
           // ... existing code ...
           (RuntimeStorageKind::Sqlite, Arc::new(sqlite_storage), Some(pool), None, None)
       }
       StorageBackendKind::Postgres => {
           // ... existing code ...
           (RuntimeStorageKind::Postgres, Arc::new(postgres_storage), None, Some(pool), None)
       }
       StorageBackendKind::Mysql => {
           let backend_config = to_sql_backend_config(&config.storage.mysql, "mysql")?;
           let mysql_storage = MysqlStorage::connect(backend_config).await?;
           let pool = mysql_storage.pool().clone();
           (
               RuntimeStorageKind::Mysql,
               Arc::new(mysql_storage),
               None,
               None,
               Some(pool),
           )
       }
   };
   ```

5. Update `from_storage_with_kind` signature:
   ```rust
   async fn from_storage_with_kind(
       config: GatewayConfig,
       storage: DynStorage,
       storage_kind: RuntimeStorageKind,
       sqlite_pool: Option<SqlitePool>,
       postgres_pool: Option<Pool<Postgres>>,
       mysql_pool: Option<Pool<MySql>>,
   ) -> anyhow::Result<(Self, mpsc::Receiver<LogEntry>)> {
   ```

6. Add `mysql_pool` to the `Gateway` struct initialization.

7. Update `from_storage` to pass `None` for `mysql_pool`.

- [ ] **Step 2: Add MySQL CLI flags to server**

In `src-server/src/main.rs`:

1. Update `--storage-backend` value parser:
   ```rust
   #[arg(long, value_parser = ["sqlite", "postgres", "mysql"], default_value = "sqlite",
         env = "NYRO_STORAGE_BACKEND", help_heading = "Storage")]
   storage_backend: String,
   ```

2. Add MySQL CLI arguments (after the PostgreSQL args block):
   ```rust
   #[arg(
       long,
       env = "NYRO_MYSQL_DSN",
       help = "MySQL connection string (required when --storage-backend=mysql)",
       help_heading = "Storage"
   )]
   mysql_dsn: Option<String>,

   #[arg(
       long,
       default_value_t = 10,
       help = "MySQL: max connection pool size",
       help_heading = "Storage"
   )]
   mysql_max_connections: u32,

   #[arg(
       long,
       default_value_t = 1,
       help = "MySQL: min connection pool size",
       help_heading = "Storage"
   )]
   mysql_min_connections: u32,

   #[arg(
       long,
       help = "MySQL: idle connection timeout (seconds)",
       help_heading = "Storage"
   )]
   mysql_idle_timeout: Option<u64>,
   ```

3. Remove `--postgres-acquire-timeout` and `--postgres-max-lifetime` arguments.

4. Update `build_storage_config()`:
   ```rust
   fn build_storage_config(args: &Args) -> anyhow::Result<GatewayStorageConfig> {
       let backend = parse_storage_backend(&args.storage_backend)?;

       let postgres_url = if matches!(backend, StorageBackendKind::Postgres) {
           let dsn = args.postgres_dsn.as_deref().map(str::trim).filter(|s| !s.is_empty())
               .ok_or_else(|| anyhow::anyhow!("--postgres-dsn (or env NYRO_POSTGRES_DSN) is required when --storage-backend=postgres"))?;
           Some(dsn.to_string())
       } else {
           None
       };

       let mysql_url = if matches!(backend, StorageBackendKind::Mysql) {
           let dsn = args.mysql_dsn.as_deref().map(str::trim).filter(|s| !s.is_empty())
               .ok_or_else(|| anyhow::anyhow!("--mysql-dsn (or env NYRO_MYSQL_DSN) is required when --storage-backend=mysql"))?;
           Some(dsn.to_string())
       } else {
           None
       };

       Ok(GatewayStorageConfig {
           backend,
           sqlite: SqliteStorageConfig { migrate_on_start: args.migrate_on_start },
           postgres: SqlStorageConfig {
               url: postgres_url,
               max_connections: args.postgres_max_connections,
               min_connections: args.postgres_min_connections,
               idle_timeout: args.postgres_idle_timeout.map(Duration::from_secs),
           },
           mysql: SqlStorageConfig {
               url: mysql_url,
               max_connections: args.mysql_max_connections,
               min_connections: args.mysql_min_connections,
               idle_timeout: args.mysql_idle_timeout.map(Duration::from_secs),
           },
       })
   }
   ```

5. Update `parse_storage_backend()`:
   ```rust
   fn parse_storage_backend(value: &str) -> anyhow::Result<StorageBackendKind> {
       match value.trim().to_ascii_lowercase().as_str() {
           "sqlite" => Ok(StorageBackendKind::Sqlite),
           "postgres" => Ok(StorageBackendKind::Postgres),
           "mysql" => Ok(StorageBackendKind::Mysql),
           other => anyhow::bail!("unsupported storage backend: {other}"),
       }
   }
   ```

- [ ] **Step 3: Verify compilation**

Run: `cargo check 2>&1 | head -40`
Expected: PASS

- [ ] **Step 4: Run all tests**

Run: `cargo test 2>&1 | tail -20`
Expected: All existing tests pass.

- [ ] **Step 5: Commit**

```bash
git add crates/nyro-core/src/lib.rs src-server/src/main.rs
git commit -m "feat: wire MySQL storage into Gateway and CLI"
```

---

## Phase 5: Verification

### Task 9: Manual verification with MySQL Docker container

- [ ] **Step 1: Start MySQL 8.0 container**

```bash
docker run -d --name nyro-mysql-test \
  -e MYSQL_ROOT_PASSWORD=test \
  -e MYSQL_DATABASE=nyro_test \
  -p 3306:3306 \
  mysql:8.0 \
  --character-set-server=utf8mb4 \
  --collation-server=utf8mb4_unicode_ci
```

- [ ] **Step 2: Build and run server**

```bash
cargo build --bin nyro-server 2>&1 | tail -5
./target/debug/nyro-server \
  --storage-backend=mysql \
  --mysql-dsn="mysql://root:test@127.0.0.1:3306/nyro_test"
```

Expected: Server starts, logs show successful connection and migration.

- [ ] **Step 3: Verify schema**

```bash
docker exec nyro-mysql-test mysql -uroot -ptest nyro_test -e "SHOW TABLES;"
docker exec nyro-mysql-test mysql -uroot -ptest nyro_test -e "DESCRIBE providers;"
docker exec nyro-mysql-test mysql -uroot -ptest nyro_test -e "DESCRIBE settings;"
docker exec nyro-mysql-test mysql -uroot -ptest nyro_test -e "DESCRIBE api_keys;"
```

Expected: All 8 tables exist with correct columns. `settings` has `name` column (not `key`). `api_keys` has `token` column (not `key`).

- [ ] **Step 4: Cleanup**

```bash
docker stop nyro-mysql-test && docker rm nyro-mysql-test
```
