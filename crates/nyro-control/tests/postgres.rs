//! Explicit opt-in: NYRO_TEST_POSTGRES_URL must identify an administrative database.
//! Every test creates and drops its own database; the supplied database is never modified.
use nyro_config::Config;
use nyro_control::{Error, Store};

#[tokio::test]
async fn postgres_rejects_unsafe_connection_options_without_exposing_credentials() {
    for url in [
        "postgres://user:secret@localhost/example?sslmode=prefer",
        "postgres://user:secret@localhost/example?sslmode=allow",
        "postgres://user:secret@example.test/example?sslmode=disable",
        "not-a-postgres-url-with-secret",
    ] {
        let result = Store::open_postgres(url, None).await;
        assert!(matches!(result, Err(Error::Storage)));
        let error = result.err().unwrap();
        assert!(!format!("{error:?} {error}").contains("secret"));
    }
}

fn config() -> Config {
    Config::from_yaml(
        r#"
llm:
  providers:
    p: {kind: openai, base_url: 'https://example.test/v1', api_key: provider-secret}
  models:
    chat: {provider: p, upstream_model: example, workloads: [chat], allow_anonymous: true}
"#,
    )
    .unwrap()
}

use sqlx::{ConnectOptions, Connection, PgConnection, postgres::PgConnectOptions};
use std::{
    str::FromStr,
    sync::atomic::{AtomicU64, Ordering},
};

struct Database {
    admin: PgConnection,
    options: PgConnectOptions,
    url: String,
    name: String,
}
impl Database {
    async fn create() -> Self {
        Self::create_with_encoding(false).await
    }
    async fn create_with_encoding(latin1: bool) -> Self {
        static NEXT: AtomicU64 = AtomicU64::new(0);
        let url = std::env::var("NYRO_TEST_POSTGRES_URL")
            .expect("set NYRO_TEST_POSTGRES_URL to an administrative database");
        let options = PgConnectOptions::from_str(&url)
            .unwrap()
            .disable_statement_logging();
        let mut admin = PgConnection::connect_with(&options).await.unwrap();
        let name = format!(
            "nyro_control_test_{}_{}_{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        );
        let encoding = if latin1 {
            " TEMPLATE template0 ENCODING 'LATIN1' LC_COLLATE 'C' LC_CTYPE 'C'"
        } else {
            ""
        };
        sqlx::query(&format!("CREATE DATABASE {name}{encoding}"))
            .execute(&mut admin)
            .await
            .unwrap();
        let options = options.database(&name);
        let mut parsed = options.to_url_lossy();
        let query: Vec<(String, String)> = parsed
            .query_pairs()
            .filter(|(key, _)| key != "statement-cache-capacity")
            .map(|(k, v)| (k.into_owned(), v.into_owned()))
            .collect();
        parsed.set_query(None);
        parsed.query_pairs_mut().extend_pairs(query);
        let url = parsed.to_string();
        Self {
            admin,
            options,
            url,
            name,
        }
    }
    async fn connect(&self) -> PgConnection {
        PgConnection::connect_with(&self.options).await.unwrap()
    }
    async fn cleanup(mut self) {
        sqlx::query(&format!("DROP DATABASE {} WITH (FORCE)", self.name))
            .execute(&mut self.admin)
            .await
            .unwrap();
    }
}

#[tokio::test]
#[ignore = "requires explicitly configured PostgreSQL admin; creates isolated databases"]
async fn postgres_durability_cas_validation_and_ownership() {
    let db = Database::create().await;
    assert!(matches!(
        Store::open_postgres(&db.url, None).await,
        Err(Error::SeedRequired)
    ));
    let mut invalid = config();
    invalid.limit.concurrency = 0;
    assert!(matches!(
        Store::open_postgres(&db.url, Some(&invalid)).await,
        Err(Error::Invalid)
    ));
    let mut store = Store::open_postgres(&db.url, Some(&config()))
        .await
        .unwrap();
    assert!(matches!(
        Store::open_postgres(&db.url, None).await,
        Err(Error::Storage)
    ));
    let mut updated = config();
    updated.limit.concurrency = 17;
    assert_eq!(store.save(1, &updated).await.unwrap().revision, 2);
    assert!(matches!(
        store.save(1, &config()).await,
        Err(Error::Conflict)
    ));
    assert!(matches!(store.save(2, &invalid).await, Err(Error::Invalid)));
    assert!(matches!(store.publish(1).await, Err(Error::Conflict)));
    assert_eq!(store.publish(2).await.unwrap().revision, 2);
    assert_eq!(store.publish(2).await.unwrap().revision, 2);
    updated.limit.concurrency = 23;
    store.save(2, &updated).await.unwrap();
    store.close().await.unwrap();
    assert!(matches!(
        Store::open_postgres(&db.url, Some(&config())).await,
        Err(Error::AlreadyInitialized)
    ));
    let mut store = Store::open_postgres(&db.url, None).await.unwrap();
    let state = store.state().await.unwrap();
    assert_eq!(state.draft.revision, 3);
    assert_eq!(state.draft.config.limit.concurrency, 23);
    assert_eq!(state.published.revision, 2);
    assert_eq!(state.published.config.limit.concurrency, 17);
    drop(store);
    let mut store = Store::open_postgres(&db.url, None).await.unwrap();
    assert_eq!(store.state().await.unwrap().draft.revision, 3);
    store.close().await.unwrap();
    let mut connection = db.connect().await;
    sqlx::query("UPDATE public.nyro_control_state SET draft_revision = 9223372036854775807")
        .execute(&mut connection)
        .await
        .unwrap();
    let mut store = Store::open_postgres(&db.url, None).await.unwrap();
    assert!(matches!(
        store.save(i64::MAX as u64, &config()).await,
        Err(Error::Conflict)
    ));
    assert_eq!(
        store.publish(i64::MAX as u64).await.unwrap().revision,
        i64::MAX as u64
    );
    store.close().await.unwrap();
    connection.close().await.unwrap();
    db.cleanup().await;
}

#[tokio::test]
#[ignore = "requires explicitly configured PostgreSQL admin; creates isolated databases"]
async fn postgres_refuses_foreign_and_corrupt_databases() {
    for corruption in [
        "CREATE TABLE public.models (secret text)",
        "CREATE SCHEMA foreign_schema",
        "CREATE SCHEMA pgcustom",
        "CREATE FUNCTION public.foreign_function() RETURNS integer LANGUAGE sql AS 'SELECT 1'",
        "CREATE TYPE public.foreign_type AS ENUM ('x')",
        "ALTER TABLE public.nyro_control_state ADD COLUMN unexpected text",
        "ALTER TABLE public.nyro_control_state DROP CONSTRAINT nyro_control_state_schema_version_check",
        "DELETE FROM public.nyro_control_state",
        "UPDATE public.nyro_control_state SET draft_json = '{}'",
        "UPDATE public.nyro_control_state SET draft_json = '{invalid-secret'",
        "UPDATE public.nyro_control_state SET draft_json = replace(draft_json, 'provider-secret', 'changed-secret')",
    ] {
        let db = Database::create().await;
        Store::open_postgres(&db.url, Some(&config()))
            .await
            .unwrap()
            .close()
            .await
            .unwrap();
        let mut connection = db.connect().await;
        sqlx::query(corruption)
            .execute(&mut connection)
            .await
            .unwrap();
        assert!(
            matches!(
                Store::open_postgres(&db.url, None).await,
                Err(Error::Schema)
            ),
            "{corruption}"
        );
        connection.close().await.unwrap();
        db.cleanup().await;
    }
    let db = Database::create().await;
    let mut connection = db.connect().await;
    sqlx::query("CREATE TABLE public.models (secret text)")
        .execute(&mut connection)
        .await
        .unwrap();
    assert!(matches!(
        Store::open_postgres(&db.url, Some(&config())).await,
        Err(Error::Schema)
    ));
    let exists: bool =
        sqlx::query_scalar("SELECT to_regclass('public.nyro_control_state') IS NOT NULL")
            .fetch_one(&mut connection)
            .await
            .unwrap();
    assert!(!exists);
    connection.close().await.unwrap();
    db.cleanup().await;
}

#[tokio::test]
#[ignore = "requires explicitly configured PostgreSQL admin; creates isolated databases"]
async fn postgres_entity_credentials_and_reference_guards_are_atomic() {
    use nyro_control::entity::{EntityChange, EntityKind, EntityValue};
    use serde_json::json;
    let db = Database::create().await;
    let mut store = Store::open_postgres(&db.url, Some(&config()))
        .await
        .unwrap();
    assert!(matches!(
        store
            .edit(
                1,
                EntityChange::Delete {
                    kind: EntityKind::Provider,
                    id: "p".into()
                }
            )
            .await,
        Err(Error::Referenced)
    ));
    let replacement = |credential: Option<serde_json::Value>| {
        let mut value = json!({"kind":"openai", "base_url":"https://other.test/v1"});
        if let Some(credential) = credential {
            value["api_key"] = credential;
        }
        EntityChange::Replace {
            id: "p".into(),
            value: EntityValue::from_json(EntityKind::Provider, value).unwrap(),
        }
    };
    store.edit(1, replacement(None)).await.unwrap();
    assert_eq!(
        store.state().await.unwrap().draft.config.llm.providers["p"]
            .api_key
            .as_deref(),
        Some("provider-secret")
    );
    store
        .edit(
            2,
            replacement(Some(json!({"action":"set","value":"rotated-secret"}))),
        )
        .await
        .unwrap();
    let state = store.state().await.unwrap();
    assert_eq!(
        state.draft.config.llm.providers["p"].api_key.as_deref(),
        Some("rotated-secret")
    );
    assert_eq!(
        state.published.config.llm.providers["p"].api_key.as_deref(),
        Some("provider-secret")
    );
    assert!(
        !state
            .draft
            .redacted()
            .to_string()
            .contains("rotated-secret")
    );
    assert!(matches!(
        store.edit(2, replacement(None)).await,
        Err(Error::Conflict)
    ));
    store
        .edit(3, replacement(Some(json!({"action":"clear"}))))
        .await
        .unwrap();
    assert!(
        store.state().await.unwrap().draft.config.llm.providers["p"]
            .api_key
            .is_none()
    );
    store.close().await.unwrap();
    db.cleanup().await;
}

#[tokio::test]
#[ignore = "requires explicitly configured PostgreSQL admin; creates isolated databases"]
async fn postgres_killed_session_never_reconnects_and_releases_ownership() {
    let db = Database::create().await;
    let mut store = Store::open_postgres(&db.url, Some(&config()))
        .await
        .unwrap();
    let killed: bool = sqlx::query_scalar("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND application_name = 'nyro-control'").bind(&db.name).fetch_one(&mut db.connect().await).await.unwrap();
    assert!(killed);
    assert!(matches!(store.state().await, Err(Error::Storage)));
    assert!(matches!(
        store.save(1, &config()).await,
        Err(Error::Storage)
    ));
    assert!(matches!(store.publish(1).await, Err(Error::Storage)));
    let mut reopened = Store::open_postgres(&db.url, None).await.unwrap();
    assert_eq!(reopened.state().await.unwrap().draft.revision, 1);
    reopened.close().await.unwrap();
    db.cleanup().await;
}

#[tokio::test]
#[ignore = "requires explicitly configured PostgreSQL admin; creates isolated databases"]
async fn postgres_lock_timeout_rolls_back_and_permanently_closes_session() {
    let db = Database::create().await;
    let mut store = Store::open_postgres(&db.url, Some(&config()))
        .await
        .unwrap();
    let mut connection = db.connect().await;
    let mut tx = connection.begin().await.unwrap();
    sqlx::query("LOCK TABLE public.nyro_control_state IN ACCESS EXCLUSIVE MODE")
        .execute(&mut *tx)
        .await
        .unwrap();
    let before = std::time::Instant::now();
    assert!(matches!(
        store.save(1, &config()).await,
        Err(Error::Storage)
    ));
    assert!(before.elapsed() < std::time::Duration::from_secs(3));
    tx.rollback().await.unwrap();
    assert!(matches!(store.state().await, Err(Error::Storage)));
    let mut reopened = Store::open_postgres(&db.url, None).await.unwrap();
    assert_eq!(reopened.state().await.unwrap().draft.revision, 1);
    reopened.close().await.unwrap();
    connection.close().await.unwrap();
    db.cleanup().await;
}

#[tokio::test]
#[ignore = "requires explicitly configured PostgreSQL admin; creates isolated databases"]
async fn postgres_definitive_sql_write_failure_preserves_snapshots() {
    let mut db = Database::create().await;
    let mut store = Store::open_postgres(&db.url, Some(&config()))
        .await
        .unwrap();
    let mut draft = config();
    draft.limit.concurrency = 17;
    store.save(1, &draft).await.unwrap();
    store.close().await.unwrap();
    sqlx::query(&format!(
        "ALTER DATABASE {} SET default_transaction_read_only = on",
        db.name
    ))
    .execute(&mut db.admin)
    .await
    .unwrap();
    let mut store = Store::open_postgres(&db.url, None).await.unwrap();
    let before = serde_json::to_value(store.state().await.unwrap()).unwrap();
    assert!(matches!(
        store.save(2, &config()).await,
        Err(Error::Storage)
    ));
    assert_eq!(
        serde_json::to_value(store.state().await.unwrap()).unwrap(),
        before
    );
    assert!(matches!(store.publish(2).await, Err(Error::Storage)));
    assert_eq!(
        serde_json::to_value(store.state().await.unwrap()).unwrap(),
        before
    );
    store.close().await.unwrap();
    db.cleanup().await;
}

#[tokio::test]
#[ignore = "requires explicitly configured PostgreSQL admin; creates isolated databases"]
async fn postgres_rejects_non_utf8_database_before_initialization() {
    let db = Database::create_with_encoding(true).await;
    let mut connection = db.connect().await;
    let encoding: String = sqlx::query_scalar("SHOW server_encoding")
        .fetch_one(&mut connection)
        .await
        .unwrap();
    assert_eq!(encoding, "LATIN1");
    let result = Store::open_postgres(&db.url, Some(&config())).await;
    let rejected = matches!(result, Err(Error::Schema));
    drop(result);
    let initialized: bool =
        sqlx::query_scalar("SELECT to_regclass('public.nyro_control_state') IS NOT NULL")
            .fetch_one(&mut connection)
            .await
            .unwrap();
    connection.close().await.unwrap();
    db.cleanup().await;
    assert!(rejected, "non-UTF8 databases must be refused");
    assert!(
        !initialized,
        "refusal must happen before schema initialization"
    );
}
