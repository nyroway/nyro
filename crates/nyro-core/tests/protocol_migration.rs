//! Acceptance: SQLite startup migration collapses legacy provider protocol
//! columns into `protocol` / `base_url` and drops the old columns.

use nyro_core::db::{init_pool, migrate};
use sqlx::Row;

#[tokio::test]
async fn migration_collapses_legacy_columns_and_is_idempotent() {
    let dir = tempfile::tempdir().unwrap();
    let pool = init_pool(dir.path()).await.unwrap();

    // Build an old providers table shape before running migrate().
    sqlx::query(
        "CREATE TABLE providers (
            id TEXT PRIMARY KEY,
            name TEXT NOT NULL,
            protocol TEXT NOT NULL,
            base_url TEXT NOT NULL,
            api_key TEXT NOT NULL,
            default_protocol TEXT NOT NULL DEFAULT '',
            protocol_endpoints TEXT NOT NULL DEFAULT '{}',
            created_at TEXT DEFAULT (datetime('now')),
            updated_at TEXT DEFAULT (datetime('now'))
        )",
    )
    .execute(&pool)
    .await
    .unwrap();

    sqlx::query(
        "INSERT INTO providers (id, name, protocol, base_url, api_key, default_protocol, protocol_endpoints) \
         VALUES ('p1', 'p1', 'openai', '', 'k', 'anthropic', \
         '{\"openai\":{\"base_url\":\"https://a.example/v1\"},\"anthropic\":{\"base_url\":\"https://b.example/v1\"}}')"
    )
    .execute(&pool)
    .await
    .unwrap();

    migrate(&pool).await.unwrap();

    let row = sqlx::query("SELECT protocol, base_url FROM providers WHERE id = 'p1'")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(row.get::<String, _>("protocol"), "anthropic-messages");
    assert_eq!(row.get::<String, _>("base_url"), "https://b.example/v1");

    let columns = sqlx::query("PRAGMA table_info(providers)")
        .fetch_all(&pool)
        .await
        .unwrap()
        .into_iter()
        .map(|row| row.get::<String, _>("name"))
        .collect::<Vec<_>>();
    assert!(!columns.iter().any(|name| name == "default_protocol"));
    assert!(!columns.iter().any(|name| name == "protocol_endpoints"));

    let snapshot_before = sqlx::query("SELECT id, protocol, base_url FROM providers ORDER BY id")
        .fetch_all(&pool)
        .await
        .unwrap()
        .into_iter()
        .map(|r| {
            (
                r.get::<String, _>("id"),
                r.get::<String, _>("protocol"),
                r.get::<String, _>("base_url"),
            )
        })
        .collect::<Vec<_>>();

    migrate(&pool).await.unwrap();

    let snapshot_after = sqlx::query("SELECT id, protocol, base_url FROM providers ORDER BY id")
        .fetch_all(&pool)
        .await
        .unwrap()
        .into_iter()
        .map(|r| {
            (
                r.get::<String, _>("id"),
                r.get::<String, _>("protocol"),
                r.get::<String, _>("base_url"),
            )
        })
        .collect::<Vec<_>>();

    assert_eq!(
        snapshot_before, snapshot_after,
        "second migrate() must be a no-op on already-normalized rows"
    );
}

#[tokio::test]
async fn migration_preserves_existing_base_url_when_legacy_json_disagrees() {
    let dir = tempfile::tempdir().unwrap();
    let pool = init_pool(dir.path()).await.unwrap();

    sqlx::query(
        "CREATE TABLE providers (
            id TEXT PRIMARY KEY,
            name TEXT NOT NULL,
            protocol TEXT NOT NULL,
            base_url TEXT NOT NULL,
            api_key TEXT NOT NULL,
            default_protocol TEXT NOT NULL DEFAULT '',
            protocol_endpoints TEXT NOT NULL DEFAULT '{}',
            created_at TEXT DEFAULT (datetime('now')),
            updated_at TEXT DEFAULT (datetime('now'))
        )",
    )
    .execute(&pool)
    .await
    .unwrap();

    sqlx::query(
        "INSERT INTO providers (id, name, protocol, base_url, api_key, default_protocol, protocol_endpoints) \
         VALUES ('p2', 'p2', 'openai', 'https://existing.example/v1', 'k', 'openai', \
         '{\"openai\":{\"base_url\":\"https://legacy.example/v1\"}}')"
    )
    .execute(&pool)
    .await
    .unwrap();

    migrate(&pool).await.unwrap();

    let base_url: String = sqlx::query_scalar("SELECT base_url FROM providers WHERE id = 'p2'")
        .fetch_one(&pool)
        .await
        .unwrap();
    assert_eq!(base_url, "https://existing.example/v1");
}
