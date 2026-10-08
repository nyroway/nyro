use nyro_control::resource::{Kind, Store, edit};
use serde_json::json;

#[tokio::test]
async fn saves_resources_renames_references_and_reopens_without_draft() {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("resources.db");
    let mut store = Store::open(&path).await.unwrap();
    let mut snapshot = store.snapshot().await.unwrap();
    assert!(snapshot.resources.models.is_empty());
    snapshot = edit(&snapshot, Kind::Upstreams, None, Some(json!({"id":"pool","kind":"llm","targets":[{"id":"a","protocol":"openai/chat-completions","base_url":"https://example.test/v1","model":"real"}]}))).unwrap();
    snapshot = edit(
        &snapshot,
        Kind::Models,
        None,
        Some(json!({"id":"chat","capability":"chat","upstream":"pool"})),
    )
    .unwrap();
    snapshot = edit(&snapshot, Kind::Consumers, None, Some(json!({"id":"app","credentials":[{"id":"key","type":"key-auth","secret":"secret"}],"grants":{"models":["chat"]}}))).unwrap();
    store.save(&snapshot).await.unwrap();
    let uid = snapshot.identities.models["chat"].clone();
    let renamed = edit(
        &snapshot,
        Kind::Models,
        Some("chat"),
        Some(json!({"id":"new-chat","capability":"chat","upstream":"pool"})),
    )
    .unwrap();
    assert_eq!(renamed.identities.models["new-chat"], uid);
    assert_eq!(renamed.resources.consumers[0].grants.models, ["new-chat"]);
    assert!(edit(&renamed, Kind::Upstreams, Some("pool"), None).is_err());
    store.save(&renamed).await.unwrap();
    store.close().await.unwrap();
    let mut reopened = Store::open(&path).await.unwrap();
    let state = reopened.snapshot().await.unwrap();
    assert_eq!(state.identities.models["new-chat"], uid);
    assert_eq!(state.resources.consumers[0].credentials[0].secret, "secret");
    reopened.close().await.unwrap();
}

#[tokio::test]
async fn rejects_old_database_without_changing_it() {
    use sqlx::{Connection, Executor};
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("old.db");
    let mut db = sqlx::SqliteConnection::connect_with(
        &sqlx::sqlite::SqliteConnectOptions::new()
            .filename(&path)
            .create_if_missing(true),
    )
    .await
    .unwrap();
    db.execute("CREATE TABLE nyro_control_state (singleton INTEGER)")
        .await
        .unwrap();
    db.close().await.unwrap();
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o600)).unwrap();
    }
    assert!(Store::open(&path).await.is_err());
}

#[tokio::test]
async fn credential_roundtrip_rotation_and_invalid_save_preserve_durable_state() {
    use nyro_control::resource::redacted;
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("resources.db");
    let mut store = Store::open(&path).await.unwrap();
    let empty = store.snapshot().await.unwrap();
    let state = edit(&empty, Kind::Consumers, None, Some(json!({"id":"app","credentials":[{"id":"primary","type":"key-auth","secret":"private-key"}]}))).unwrap();
    store.save(&state).await.unwrap();
    let safe = redacted(&state.resources);
    assert!(!safe.to_string().contains("private-key"));
    let same = edit(
        &state,
        Kind::Consumers,
        Some("app"),
        Some(safe["consumers"][0].clone()),
    )
    .unwrap();
    assert_eq!(
        same.resources.consumers[0].credentials[0].secret,
        "private-key"
    );
    let mut replacement = safe["consumers"][0].clone();
    replacement["id"] = json!("renamed");
    replacement["credentials"][0]["secret"] = json!("rotated-key");
    let rotated = edit(&same, Kind::Consumers, Some("app"), Some(replacement)).unwrap();
    assert_eq!(
        rotated.identities.consumers["renamed"],
        state.identities.consumers["app"]
    );
    store.save(&rotated).await.unwrap();
    let mut invalid = rotated.clone();
    invalid.resources.consumers[0]
        .grants
        .models
        .push("missing".into());
    assert!(store.save(&invalid).await.is_err());
    let retained = store.snapshot().await.unwrap();
    assert!(retained.resources.consumers[0].grants.models.is_empty());
    assert_eq!(
        retained.resources.consumers[0].credentials[0].secret,
        "rotated-key"
    );
    store.close().await.unwrap();
}

#[tokio::test]
async fn exclusive_owner_and_failed_sql_transaction_preserve_previous_resources() {
    use sqlx::{Connection, Executor};
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("resources.db");
    let mut store = Store::open(&path).await.unwrap();
    assert!(Store::open(&path).await.is_err());
    let empty = store.snapshot().await.unwrap();
    let state = edit(
        &empty,
        Kind::Consumers,
        None,
        Some(json!({"id":"original"})),
    )
    .unwrap();
    store.save(&state).await.unwrap();
    store.close().await.unwrap();
    let mut db = sqlx::SqliteConnection::connect_with(
        &sqlx::sqlite::SqliteConnectOptions::new().filename(&path),
    )
    .await
    .unwrap();
    db.execute("CREATE TRIGGER fail_insert BEFORE INSERT ON consumers WHEN NEW.id = 'rejected' BEGIN SELECT RAISE(ABORT, 'test failure'); END").await.unwrap();
    db.close().await.unwrap();
    let mut reopened = Store::open(&path).await.unwrap();
    let rejected = edit(
        &state,
        Kind::Consumers,
        Some("original"),
        Some(json!({"id":"rejected"})),
    )
    .unwrap();
    assert!(reopened.save(&rejected).await.is_err());
    assert!(
        reopened.snapshot().await.is_err(),
        "failed write closes the store for reconciliation"
    );
    reopened.close().await.unwrap();
    let mut reopened = Store::open(&path).await.unwrap();
    let retained = reopened.snapshot().await.unwrap();
    assert_eq!(retained.resources.consumers[0].id, "original");
    assert_eq!(retained.identities.consumers, state.identities.consumers);
    reopened.close().await.unwrap();
}

/// Requires an explicitly supplied empty disposable database. Leaves the test resources in it.
#[tokio::test]
#[ignore = "requires NYRO_TEST_CONTROL_POSTGRES_URL pointing to an empty disposable PostgreSQL database"]
async fn postgres_resources_survive_rename_reopen_and_reject_second_owner() {
    let url = std::env::var("NYRO_TEST_CONTROL_POSTGRES_URL").expect("disposable database URL");
    let mut store = Store::open_postgres(&url).await.unwrap();
    let empty = store.snapshot().await.unwrap();
    assert!(
        empty.resources.upstreams.is_empty()
            && empty.resources.models.is_empty()
            && empty.resources.mcps.is_empty()
            && empty.resources.consumers.is_empty()
    );
    assert!(Store::open_postgres(&url).await.is_err());
    let state = edit(&empty, Kind::Upstreams, None, Some(json!({"id":"pool","kind":"mcp","targets":[{"id":"primary","transport":"streamable-http","url":"https://example.test/mcp"}]}))).unwrap();
    let state = edit(
        &state,
        Kind::Mcps,
        None,
        Some(json!({"id":"tools","upstream":"pool","allowed_tools":["search"]})),
    )
    .unwrap();
    let state = edit(
        &state,
        Kind::Consumers,
        None,
        Some(json!({"id":"app","grants":{"mcps":["tools"]}})),
    )
    .unwrap();
    store.save(&state).await.unwrap();
    let mut renamed = serde_json::to_value(&state.resources.mcps[0]).unwrap();
    renamed["id"] = json!("renamed");
    let state = edit(&state, Kind::Mcps, Some("tools"), Some(renamed)).unwrap();
    store.save(&state).await.unwrap();
    store.close().await.unwrap();
    let mut reopened = Store::open_postgres(&url).await.unwrap();
    let retained = reopened.snapshot().await.unwrap();
    assert_eq!(
        retained.identities.mcps["renamed"],
        state.identities.mcps["renamed"]
    );
    assert_eq!(retained.resources.consumers[0].grants.mcps, ["renamed"]);
    reopened.close().await.unwrap();
}

#[test]
fn authenticated_egress_url_is_redacted_and_can_be_retained_rotated_or_cleared() {
    let empty = nyro_config::compile::Snapshot::file(Default::default());
    let state = edit(&empty, Kind::Upstreams, None, Some(json!({"id":"pool","kind":"llm","targets":[{"id":"a","protocol":"openai/chat-completions","base_url":"https://example.test/v1","model":"real","egress":{"proxy_url":"http://proxy-user:proxy-secret@localhost:8080"}}]}))).unwrap();
    let mut view = nyro_control::resource::redacted(&state.resources)["upstreams"][0].clone();
    assert!(!view.to_string().contains("proxy-secret"));
    assert!(!view.to_string().contains("proxy-user"));
    assert_eq!(view["targets"][0]["egress"]["has_proxy_url"], true);
    let kept = edit(&state, Kind::Upstreams, Some("pool"), Some(view.clone())).unwrap();
    assert_eq!(
        kept.resources.upstreams[0].targets[0].egress.proxy_url,
        state.resources.upstreams[0].targets[0].egress.proxy_url
    );
    view["targets"][0]["egress"]["proxy_url"] = json!("http://proxy-user:rotated@localhost:8080");
    let rotated = edit(&kept, Kind::Upstreams, Some("pool"), Some(view.clone())).unwrap();
    assert!(
        rotated.resources.upstreams[0].targets[0]
            .egress
            .proxy_url
            .as_ref()
            .unwrap()
            .contains("rotated")
    );
    view["targets"][0]["egress"]["proxy_url"] = serde_json::Value::Null;
    let cleared = edit(&rotated, Kind::Upstreams, Some("pool"), Some(view)).unwrap();
    assert!(
        cleared.resources.upstreams[0].targets[0]
            .egress
            .proxy_url
            .is_none()
    );
}

#[test]
fn replacing_targets_with_scalar_values_is_rejected_without_panicking() {
    let empty = nyro_config::compile::Snapshot::file(Default::default());
    let state = edit(&empty, Kind::Upstreams, None, Some(json!({"id":"pool","kind":"mcp","targets":[{"id":"a","transport":"streamable-http","url":"https://example.test/mcp"}]}))).unwrap();
    for value in [
        json!(42),
        json!(true),
        json!("invalid"),
        serde_json::Value::Null,
        json!([]),
    ] {
        assert!(
            edit(
                &state,
                Kind::Upstreams,
                Some("pool"),
                Some(json!({"id":"pool","kind":"mcp","targets":[value]}))
            )
            .is_err()
        );
    }
}
