use nyro_config::Config;
use nyro_control::{Error, MAX_CONFIG_BYTES, Store};
use sqlx::{Connection, SqliteConnection, sqlite::SqliteConnectOptions};
use std::{path::Path, time::Duration};

fn config() -> Config {
    Config::from_yaml(
        r#"
llm:
  providers:
    p: {kind: openai, base_url: 'https://example.test/v1', api_key: provider-secret}
  models:
    chat:
      provider: p
      upstream_model: example
      workloads: [chat]
      allow_anonymous: true
"#,
    )
    .unwrap()
}

async fn database(path: &Path) -> SqliteConnection {
    let connection = SqliteConnection::connect_with(
        &SqliteConnectOptions::new()
            .filename(path)
            .create_if_missing(true),
    )
    .await
    .unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o600)).unwrap();
    }
    connection
}

#[tokio::test]
async fn reopens_published_snapshot_without_publishing_newer_draft() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("control.db");
    let mut store = Store::open(&path, Some(&config())).await.unwrap();
    let initial = store.state().await.unwrap();
    assert_eq!(initial.draft.revision, 1);
    assert_eq!(initial.published.revision, 1);
    let mut updated = config();
    updated.limit.concurrency = 17;
    assert_eq!(store.save(1, &updated).await.unwrap().revision, 2);
    assert_eq!(store.publish(2).await.unwrap().config.limit.concurrency, 17);
    assert_eq!(store.publish(2).await.unwrap().revision, 2);
    updated.limit.concurrency = 23;
    store.save(2, &updated).await.unwrap();
    store.close().await.unwrap();

    let mut store = Store::open(&path, None).await.unwrap();
    let state = store.state().await.unwrap();
    assert_eq!(state.draft.revision, 3);
    assert_eq!(state.draft.config.limit.concurrency, 23);
    assert_eq!(state.published.revision, 2);
    assert_eq!(state.published.config.limit.concurrency, 17);
    store.close().await.unwrap();
}

#[tokio::test]
async fn stale_writes_and_publications_preserve_both_snapshots() {
    let temp = tempfile::tempdir().unwrap();
    let mut store = Store::open(&temp.path().join("control.db"), Some(&config()))
        .await
        .unwrap();
    let mut updated = config();
    updated.limit.concurrency = 17;
    store.save(1, &updated).await.unwrap();
    assert!(matches!(
        store.save(1, &config()).await,
        Err(Error::Conflict)
    ));
    assert!(matches!(store.publish(1).await, Err(Error::Conflict)));
    assert!(matches!(
        store.publish(u64::MAX).await,
        Err(Error::Conflict)
    ));
    let state = store.state().await.unwrap();
    assert_eq!(state.draft.revision, 2);
    assert_eq!(state.draft.config.limit.concurrency, 17);
    assert_eq!(state.published.revision, 1);
}

#[tokio::test]
async fn invalid_or_oversized_config_does_not_change_state() {
    let temp = tempfile::tempdir().unwrap();
    let mut store = Store::open(&temp.path().join("control.db"), Some(&config()))
        .await
        .unwrap();
    let mut invalid = config();
    invalid.server.max_body_bytes = 0;
    assert!(matches!(store.save(1, &invalid).await, Err(Error::Invalid)));
    let mut oversized = config();
    oversized.llm.providers.get_mut("p").unwrap().api_key = Some("s".repeat(MAX_CONFIG_BYTES));
    assert!(matches!(
        store.save(1, &oversized).await,
        Err(Error::Invalid)
    ));
    assert_eq!(store.state().await.unwrap().draft.revision, 1);
}

#[tokio::test]
async fn seed_is_required_only_for_empty_database_and_rejected_after_initialization() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("control.db");
    assert!(matches!(
        Store::open(&path, None).await,
        Err(Error::SeedRequired)
    ));
    let mut invalid = config();
    invalid.limit.concurrency = 0;
    assert!(matches!(
        Store::open(&path, Some(&invalid)).await,
        Err(Error::Invalid)
    ));
    Store::open(&path, Some(&config()))
        .await
        .unwrap()
        .close()
        .await
        .unwrap();
    assert!(matches!(
        Store::open(&path, Some(&config())).await,
        Err(Error::AlreadyInitialized)
    ));
    assert!(Store::open(&path, None).await.is_ok());
}

#[tokio::test]
async fn refuses_foreign_database_without_changing_it() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("legacy.db");
    let mut db = database(&path).await;
    sqlx::query("CREATE TABLE models (secret TEXT)")
        .execute(&mut db)
        .await
        .unwrap();
    db.close().await.unwrap();
    let before = std::fs::read(&path).unwrap();
    assert!(matches!(
        Store::open(&path, Some(&config())).await,
        Err(Error::Schema)
    ));
    assert_eq!(std::fs::read(&path).unwrap(), before);
}

#[tokio::test]
async fn refuses_corrupt_persisted_state() {
    for corruption in [
        "PRAGMA user_version = 99",
        "UPDATE nyro_control_state SET draft_json = '{}'",
        "UPDATE nyro_control_state SET draft_json = '{invalid-secret'",
        "DELETE FROM nyro_control_state",
        "ALTER TABLE nyro_control_state ADD COLUMN unexpected TEXT",
        "CREATE TABLE unexpected (id INTEGER)",
        "CREATE TABLE sqlitex (id INTEGER)",
        "PRAGMA ignore_check_constraints = ON; UPDATE nyro_control_state SET draft_revision = 0",
        "PRAGMA ignore_check_constraints = ON; UPDATE nyro_control_state SET published_revision = 2",
        "UPDATE nyro_control_state SET draft_json = json_set(draft_json, '$.limit.concurrency', 17)",
        "UPDATE nyro_control_state SET draft_revision = 2, draft_json = json_set(draft_json, '$.limit.concurrency', 0)",
        "PRAGMA ignore_check_constraints = ON; UPDATE nyro_control_state SET published_json = CAST(zeroblob(1048577) AS TEXT)",
    ] {
        let temp = tempfile::tempdir().unwrap();
        let path = temp.path().join("control.db");
        Store::open(&path, Some(&config()))
            .await
            .unwrap()
            .close()
            .await
            .unwrap();
        let mut db = database(&path).await;
        sqlx::raw_sql(corruption).execute(&mut db).await.unwrap();
        db.close().await.unwrap();
        assert!(
            matches!(Store::open(&path, None).await, Err(Error::Schema)),
            "{corruption}"
        );
    }
}

#[tokio::test]
async fn second_owner_is_rejected_until_connection_closes() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("control.db");
    let first = Store::open(&path, Some(&config())).await.unwrap();
    let result = tokio::time::timeout(Duration::from_secs(2), Store::open(&path, None))
        .await
        .expect("a second owner must fail promptly");
    assert!(matches!(result, Err(Error::Storage)));
    first.close().await.unwrap();
    Store::open(&path, None)
        .await
        .unwrap()
        .close()
        .await
        .unwrap();
}

#[cfg(unix)]
#[tokio::test]
async fn new_database_is_private() {
    use std::os::unix::fs::PermissionsExt;
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("control.db");
    let store = Store::open(&path, Some(&config())).await.unwrap();
    assert_eq!(
        std::fs::metadata(&path).unwrap().permissions().mode() & 0o777,
        0o600
    );
    store.close().await.unwrap();
}

#[test]
fn errors_expose_no_database_or_configuration_details() {
    use std::error::Error as _;
    for error in [
        Error::Storage,
        Error::Invalid,
        Error::Conflict,
        Error::Schema,
        Error::SeedRequired,
        Error::AlreadyInitialized,
    ] {
        assert!(error.source().is_none());
        assert!(!format!("{error:?} {error}").contains("secret"));
    }
}

#[tokio::test]
async fn exhausted_revision_does_not_overflow_or_change_the_published_target() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("control.db");
    Store::open(&path, Some(&config()))
        .await
        .unwrap()
        .close()
        .await
        .unwrap();
    let mut db = database(&path).await;
    sqlx::query("UPDATE nyro_control_state SET draft_revision = 9223372036854775807")
        .execute(&mut db)
        .await
        .unwrap();
    db.close().await.unwrap();
    let mut store = Store::open(&path, None).await.unwrap();
    assert!(matches!(
        store.save(i64::MAX as u64, &config()).await,
        Err(Error::Conflict)
    ));
    let state = store.state().await.unwrap();
    assert_eq!(state.draft.revision, i64::MAX as u64);
    assert_eq!(state.published.revision, 1);
    assert_eq!(
        store.publish(i64::MAX as u64).await.unwrap().revision,
        i64::MAX as u64
    );
}

#[tokio::test]
async fn refuses_sqlite_prefixed_foreign_table_without_initializing() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("foreign.db");
    let mut db = database(&path).await;
    sqlx::query("CREATE TABLE sqlitex (secret TEXT)")
        .execute(&mut db)
        .await
        .unwrap();
    db.close().await.unwrap();
    assert!(matches!(
        Store::open(&path, Some(&config())).await,
        Err(Error::Schema)
    ));
    let mut db = database(&path).await;
    let count: i64 =
        sqlx::query_scalar("SELECT COUNT(*) FROM sqlite_schema WHERE name = 'nyro_control_state'")
            .fetch_one(&mut db)
            .await
            .unwrap();
    assert_eq!(count, 0);
}

#[tokio::test]
async fn refuses_directory_database_path_promptly() {
    let temp = tempfile::tempdir().unwrap();
    let result = tokio::time::timeout(
        Duration::from_millis(500),
        Store::open(temp.path(), Some(&config())),
    )
    .await
    .expect("a directory must fail promptly");
    assert!(matches!(result, Err(Error::Storage)));
}

#[cfg(unix)]
#[tokio::test]
async fn refuses_fifo_database_path_promptly() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("control.fifo");
    assert!(
        std::process::Command::new("mkfifo")
            .arg(&path)
            .status()
            .unwrap()
            .success()
    );
    let result = tokio::time::timeout(
        Duration::from_millis(500),
        Store::open(&path, Some(&config())),
    )
    .await
    .expect("a FIFO must fail promptly");
    assert!(matches!(result, Err(Error::Storage)));
}

#[cfg(unix)]
#[tokio::test]
async fn refuses_public_empty_database_before_writing_credentials() {
    use std::os::unix::fs::PermissionsExt;
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("control.db");
    std::fs::write(&path, []).unwrap();
    std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o644)).unwrap();
    assert!(matches!(
        Store::open(&path, Some(&config())).await,
        Err(Error::Storage)
    ));
    assert!(std::fs::read(&path).unwrap().is_empty());
    assert_eq!(
        std::fs::metadata(&path).unwrap().permissions().mode() & 0o777,
        0o644
    );
}

#[cfg(unix)]
#[tokio::test]
async fn refuses_reopening_database_with_public_permissions() {
    use std::os::unix::fs::PermissionsExt;
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("control.db");
    Store::open(&path, Some(&config()))
        .await
        .unwrap()
        .close()
        .await
        .unwrap();
    std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o644)).unwrap();
    let before = std::fs::read(&path).unwrap();
    assert!(matches!(
        Store::open(&path, None).await,
        Err(Error::Storage)
    ));
    assert_eq!(std::fs::read(&path).unwrap(), before);
    assert_eq!(
        std::fs::metadata(&path).unwrap().permissions().mode() & 0o777,
        0o644
    );
}
