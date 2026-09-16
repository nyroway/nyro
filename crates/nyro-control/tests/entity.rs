use nyro_config::Config;
use nyro_control::{
    Error, Store,
    entity::{EntityChange, EntityKind, EntityValue},
};
use serde_json::{Value, json};

fn config() -> Config {
    Config::from_yaml(
        r#"
llm:
  providers:
    p:
      kind: openai
      base_url: https://example.test/v1
      api_key: provider-secret
      transport: {proxy_url: 'http://proxy-user:proxy-password@localhost:8080', http1_only: true}
  models:
    chat:
      provider: p
      upstream_model: example
      workloads: [chat]
      allow_anonymous: true
security:
  api_keys:
    - {id: z-key, secret: client-secret, enabled: false, expires_at: 100}
"#,
    )
    .unwrap()
}

fn provider() -> Value {
    json!({"kind":"openai","base_url":"https://other.test/v1"})
}
fn model() -> Value {
    json!({"provider":"p","upstream_model":"other","workloads":["chat"],"allow_anonymous":true})
}
fn create(kind: EntityKind, id: &str, value: Value) -> EntityChange {
    EntityChange::Create {
        id: id.into(),
        value: EntityValue::from_json(kind, value).unwrap(),
    }
}
fn replace(kind: EntityKind, id: &str, value: Value) -> EntityChange {
    EntityChange::Replace {
        id: id.into(),
        value: EntityValue::from_json(kind, value).unwrap(),
    }
}
fn delete(kind: EntityKind, id: &str) -> EntityChange {
    EntityChange::Delete {
        kind,
        id: id.into(),
    }
}
async fn state(store: &mut Store) -> Value {
    serde_json::to_value(store.state().await.unwrap()).unwrap()
}

#[tokio::test]
async fn provider_credentials_keep_set_clear_and_draft_survives_reopen() {
    let temp = tempfile::tempdir().unwrap();
    let path = temp.path().join("control.db");
    let mut store = Store::open(&path, Some(&config())).await.unwrap();
    assert_eq!(
        store
            .edit(1, replace(EntityKind::Provider, "p", provider()))
            .await
            .unwrap(),
        2
    );
    let snapshot = store.state().await.unwrap();
    let p = &snapshot.draft.config.llm.providers["p"];
    assert_eq!(p.api_key.as_deref(), Some("provider-secret"));
    assert_eq!(
        p.transport.proxy_url.as_deref(),
        Some("http://proxy-user:proxy-password@localhost:8080")
    );
    assert!(!p.transport.http1_only);
    assert_eq!(snapshot.published.revision, 1);
    let mut value = provider();
    value["api_key"] = json!({"action":"set","value":"new-provider-secret"});
    value["transport"] = json!({"proxy_url":{"action":"set","value":"http://new-user:new-password@localhost:8081"},"http1_only":true});
    assert_eq!(
        store
            .edit(2, replace(EntityKind::Provider, "p", value))
            .await
            .unwrap(),
        3
    );
    store.close().await.unwrap();
    let mut store = Store::open(&path, None).await.unwrap();
    let snapshot = store.state().await.unwrap();
    assert_eq!(snapshot.draft.revision, 3);
    assert_eq!(
        snapshot.draft.config.llm.providers["p"].api_key.as_deref(),
        Some("new-provider-secret")
    );
    assert_eq!(
        snapshot.draft.config.llm.providers["p"]
            .transport
            .proxy_url
            .as_deref(),
        Some("http://new-user:new-password@localhost:8081")
    );
    assert_eq!(
        snapshot.published.config.llm.providers["p"]
            .api_key
            .as_deref(),
        Some("provider-secret")
    );
    let mut value = provider();
    value["api_key"] = json!({"action":"clear"});
    value["transport"] = json!({"proxy_url":{"action":"clear"}});
    store
        .edit(3, replace(EntityKind::Provider, "p", value))
        .await
        .unwrap();
    let snapshot = store.state().await.unwrap();
    assert!(snapshot.draft.config.llm.providers["p"].api_key.is_none());
    assert!(
        snapshot.draft.config.llm.providers["p"]
            .transport
            .proxy_url
            .is_none()
    );
}

#[tokio::test]
async fn create_replace_delete_all_entity_kinds_and_sorted_views() {
    let temp = tempfile::tempdir().unwrap();
    let mut store = Store::open(&temp.path().join("control.db"), Some(&config()))
        .await
        .unwrap();
    store
        .edit(1, create(EntityKind::Provider, "a-provider", provider()))
        .await
        .unwrap();
    store
        .edit(2, create(EntityKind::Model, "a-model", model()))
        .await
        .unwrap();
    store
        .edit(
            3,
            create(
                EntityKind::ApiKey,
                "a-key",
                json!({"secret":{"action":"set","value":"new-key-secret"}}),
            ),
        )
        .await
        .unwrap();
    let snapshot = store.state().await.unwrap().draft;
    for (kind, ids) in [
        (EntityKind::Provider, json!(["a-provider", "p"])),
        (EntityKind::Model, json!(["a-model", "chat"])),
        (EntityKind::ApiKey, json!(["a-key", "z-key"])),
    ] {
        let view = snapshot.entities(kind, None).unwrap();
        assert_eq!(view["draft_revision"], 4);
        assert_eq!(
            view["items"]
                .as_array()
                .unwrap()
                .iter()
                .map(|item| item["id"].clone())
                .collect::<Vec<_>>(),
            ids.as_array().unwrap().clone()
        );
    }
    assert!(
        snapshot.config.llm.providers["a-provider"]
            .api_key
            .is_none()
    );
    let mut value = model();
    value["upstream_model"] = json!("replacement");
    store
        .edit(4, replace(EntityKind::Model, "a-model", value))
        .await
        .unwrap();
    assert_eq!(
        store.state().await.unwrap().draft.config.llm.models["a-model"].backends[0].upstream_model,
        "replacement"
    );
    store
        .edit(
            5,
            replace(
                EntityKind::ApiKey,
                "a-key",
                json!({"enabled":false,"expires_at":123}),
            ),
        )
        .await
        .unwrap();
    assert_eq!(
        store
            .state()
            .await
            .unwrap()
            .draft
            .entities(EntityKind::ApiKey, Some("a-key"))
            .unwrap(),
        json!({"draft_revision":6,"item":{"id":"a-key","value":{"enabled":false,"expires_at":123,"has_secret":true}}})
    );
    store
        .edit(6, delete(EntityKind::Provider, "a-provider"))
        .await
        .unwrap();
    store
        .edit(7, delete(EntityKind::Model, "a-model"))
        .await
        .unwrap();
    store
        .edit(8, delete(EntityKind::ApiKey, "a-key"))
        .await
        .unwrap();
    let snapshot = store.state().await.unwrap();
    assert_eq!(snapshot.draft.revision, 9);
    assert_eq!(snapshot.published.revision, 1);
    assert!(matches!(
        snapshot
            .draft
            .entities(EntityKind::Provider, Some("a-provider")),
        Err(Error::NotFound)
    ));
    assert!(matches!(
        snapshot.draft.entities(EntityKind::Model, Some("a-model")),
        Err(Error::NotFound)
    ));
    assert!(matches!(
        snapshot.draft.entities(EntityKind::ApiKey, Some("a-key")),
        Err(Error::NotFound)
    ));
}

#[tokio::test]
async fn views_project_credentials_without_mutating_persisted_secrets() {
    let temp = tempfile::tempdir().unwrap();
    let mut store = Store::open(&temp.path().join("control.db"), Some(&config()))
        .await
        .unwrap();
    let before = state(&mut store).await;
    let snapshot = store.state().await.unwrap().draft;
    let full = snapshot.redacted();
    assert_eq!(full["revision"], 1);
    assert_eq!(
        full["config"]["llm"]["providers"]["p"],
        json!({"kind":"openai","base_url":"https://example.test/v1","native_chat":false,"api":null,"has_api_key":true,"transport":{"has_proxy_url":true,"http1_only":true}})
    );
    assert_eq!(
        full["config"]["security"]["api_keys"],
        json!([{"id":"z-key","enabled":false,"expires_at":100,"has_secret":true}])
    );
    for view in [
        full.clone(),
        snapshot.entities(EntityKind::Provider, None).unwrap(),
        snapshot.entities(EntityKind::ApiKey, None).unwrap(),
    ] {
        let encoded = view.to_string();
        for secret in [
            "provider-secret",
            "client-secret",
            "proxy-user",
            "proxy-password",
            "\"api_key\"",
            "\"proxy_url\"",
            "\"secret\"",
            "[REDACTED]",
        ] {
            assert!(!encoded.contains(secret), "view leaked {secret}");
        }
    }
    assert!(serde_json::from_value::<Config>(full["config"].clone()).is_err());
    assert_eq!(state(&mut store).await, before);
}

#[test]
fn rejects_null_credentials_unknown_fields_and_ambiguous_changes() {
    for field in ["api_key", "transport"] {
        let mut value = provider();
        value[field] = Value::Null;
        assert!(matches!(
            EntityValue::from_json(EntityKind::Provider, value),
            Err(Error::Invalid)
        ));
    }
    for credential in [
        Value::Null,
        json!("raw-secret"),
        json!({"action":"set"}),
        json!({"action":"set","value":null}),
        json!({"action":"keep","value":"ignored"}),
        json!({"action":"clear","extra":true}),
    ] {
        let mut p = provider();
        p["api_key"] = credential.clone();
        assert!(matches!(
            EntityValue::from_json(EntityKind::Provider, p),
            Err(Error::Invalid)
        ));
        let mut p = provider();
        p["transport"] = json!({"proxy_url":credential.clone()});
        assert!(matches!(
            EntityValue::from_json(EntityKind::Provider, p),
            Err(Error::Invalid)
        ));
        assert!(matches!(
            EntityValue::from_json(EntityKind::ApiKey, json!({"secret":credential})),
            Err(Error::Invalid)
        ));
    }
    for (kind, value) in [
        (
            EntityKind::Provider,
            json!({"kind":"openai","base_url":"https://example.test","has_api_key":true}),
        ),
        (EntityKind::ApiKey, json!({"id":"renamed"})),
        (EntityKind::ApiKey, json!({"subject_limits":{}})),
        (
            EntityKind::Model,
            json!({"provider":"p","upstream_model":"x","workloads":["chat"],"unknown":true}),
        ),
    ] {
        assert!(matches!(
            EntityValue::from_json(kind, value),
            Err(Error::Invalid)
        ));
    }
}

#[tokio::test]
async fn api_key_secret_is_required_on_create_preserved_on_update_and_rotatable() {
    let temp = tempfile::tempdir().unwrap();
    let mut store = Store::open(&temp.path().join("control.db"), Some(&config()))
        .await
        .unwrap();
    let before = state(&mut store).await;
    for value in [
        json!({}),
        json!({"secret":{"action":"clear"}}),
        json!({"secret":{"action":"set","value":""}}),
        json!({"secret":{"action":"set","value":"has space"}}),
        json!({"secret":{"action":"set","value":"client-secret"}}),
    ] {
        assert!(matches!(
            store
                .edit(1, create(EntityKind::ApiKey, "new-key", value))
                .await,
            Err(Error::Invalid)
        ));
        assert_eq!(state(&mut store).await, before);
    }
    assert!(matches!(
        store
            .edit(
                1,
                replace(
                    EntityKind::ApiKey,
                    "z-key",
                    json!({"secret":{"action":"clear"}})
                )
            )
            .await,
        Err(Error::Invalid)
    ));
    store
        .edit(1, replace(EntityKind::ApiKey, "z-key", json!({})))
        .await
        .unwrap();
    let key = store
        .state()
        .await
        .unwrap()
        .draft
        .config
        .security
        .api_keys
        .remove(0);
    assert_eq!(key.secret, "client-secret");
    assert!(key.enabled);
    assert!(key.expires_at.is_none());
    store
        .edit(
            2,
            replace(
                EntityKind::ApiKey,
                "z-key",
                json!({"secret":{"action":"set","value":"rotated-secret"}}),
            ),
        )
        .await
        .unwrap();
    assert_eq!(
        store.state().await.unwrap().draft.config.security.api_keys[0].secret,
        "rotated-secret"
    );
}

#[tokio::test]
async fn stale_missing_duplicate_referenced_and_invalid_edits_are_atomic() {
    let temp = tempfile::tempdir().unwrap();
    let mut store = Store::open(&temp.path().join("control.db"), Some(&config()))
        .await
        .unwrap();
    let before = state(&mut store).await;
    for (kind, id, value) in [
        (EntityKind::Provider, "p", provider()),
        (EntityKind::Model, "chat", model()),
        (EntityKind::ApiKey, "z-key", json!({})),
    ] {
        assert!(matches!(
            store.edit(0, replace(kind, "missing", value.clone())).await,
            Err(Error::Conflict)
        ));
        assert!(matches!(
            store.edit(1, create(kind, id, value.clone())).await,
            Err(Error::AlreadyExists)
        ));
        assert!(matches!(
            store.edit(1, replace(kind, "missing", value)).await,
            Err(Error::NotFound)
        ));
        assert!(matches!(
            store.edit(1, delete(kind, "missing")).await,
            Err(Error::NotFound)
        ));
    }
    assert!(matches!(
        store.edit(1, delete(EntityKind::Provider, "p")).await,
        Err(Error::Referenced)
    ));
    assert!(matches!(
        store.edit(1, delete(EntityKind::Model, "chat")).await,
        Err(Error::Invalid)
    ));
    let mut invalid_model = model();
    invalid_model["provider"] = json!("missing");
    assert!(matches!(
        store
            .edit(1, replace(EntityKind::Model, "chat", invalid_model))
            .await,
        Err(Error::Invalid)
    ));
    let mut invalid_provider = provider();
    invalid_provider["api_key"] = json!({"action":"set","value":""});
    assert!(matches!(
        store
            .edit(1, replace(EntityKind::Provider, "p", invalid_provider))
            .await,
        Err(Error::Invalid)
    ));
    assert_eq!(state(&mut store).await, before);
}

#[tokio::test]
async fn deletion_rejects_disabled_backends_anonymous_subjects_and_subject_limits() {
    for reference in ["backend", "model-subject", "subject-limit"] {
        let mut seed = config();
        match reference {
            "backend" => {
                seed.llm
                    .providers
                    .insert("disabled".into(), seed.llm.providers["p"].clone());
                let mut backend = seed.llm.models["chat"].backends[0].clone();
                backend.id = "disabled".into();
                backend.provider = "disabled".into();
                backend.weight = 0;
                seed.llm
                    .models
                    .get_mut("chat")
                    .unwrap()
                    .backends
                    .push(backend);
            }
            "model-subject" => {
                seed.llm
                    .models
                    .get_mut("chat")
                    .unwrap()
                    .subjects
                    .insert("z-key".into());
            }
            _ => {
                seed.llm.subject_limits.insert(
                    "z-key".into(),
                    serde_json::from_value(json!({"rpm":10})).unwrap(),
                );
            }
        }
        let temp = tempfile::tempdir().unwrap();
        let mut store = Store::open(&temp.path().join("control.db"), Some(&seed))
            .await
            .unwrap();
        let before = state(&mut store).await;
        let change = if reference == "backend" {
            delete(EntityKind::Provider, "disabled")
        } else {
            delete(EntityKind::ApiKey, "z-key")
        };
        assert!(matches!(
            store.edit(1, change).await,
            Err(Error::Referenced)
        ));
        assert_eq!(state(&mut store).await, before);
    }
}
