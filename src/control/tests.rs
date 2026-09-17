use super::*;
use axum::{
    body::{Body, to_bytes},
    http::Request,
};
use nyro_security::ApiKey;
use tempfile::TempDir;
use tower::ServiceExt;

const ADMIN: &str = "admin-secret-for-tests";

async fn setup() -> (TempDir, Arc<Control>, Config) {
    let dir = tempfile::tempdir().unwrap();
    let config = Config::from_yaml(
        r#"
llm:
  providers:
    p: {kind: openai, base_url: 'http://127.0.0.1:1/v1'}
  models:
    public:
      provider: p
      upstream_model: original
      workloads: [chat]
      allow_anonymous: true
      quota: {total_tokens: 100, reserve_tokens: 1}
"#,
    )
    .unwrap();
    let store = Store::open(&dir.path().join("control.db"), Some(&config))
        .await
        .unwrap();
    let resources = Resources::new(&config).unwrap();
    let host = crate::bootstrap::host(&config, &resources).await.unwrap();
    let keys = ApiKeys::new(vec![ApiKey {
        id: "admin".into(),
        secret: ADMIN.into(),
        enabled: true,
        expires_at: None,
    }])
    .unwrap();
    (dir, Control::new(store, resources, host, 1, keys), config)
}

async fn request(
    control: &Arc<Control>,
    method: &str,
    path: &str,
    body: Value,
    keys: &[&str],
) -> (StatusCode, Value) {
    let mut request = Request::builder()
        .method(method)
        .uri(path)
        .header("Content-Type", "application/json");
    for key in keys {
        request = request.header(header::AUTHORIZATION, format!("Bearer {key}"));
    }
    let response = router(control.clone())
        .oneshot(request.body(Body::from(body.to_string())).unwrap())
        .await
        .unwrap();
    assert_eq!(response.headers()[header::CACHE_CONTROL], "no-store");
    let status = response.status();
    let body = to_bytes(response.into_body(), 2 * MAX_CONFIG_BYTES)
        .await
        .unwrap();
    (status, serde_json::from_slice(&body).unwrap())
}

async fn finish(control: &Arc<Control>) {
    control.shutdown().await;
    control.host.shutdown().await.unwrap();
}

#[tokio::test]
async fn authentication_and_parse_rejections_never_save_or_echo_secrets() {
    let (_dir, control, config) = setup().await;
    for keys in [vec![], vec!["data-key"], vec![ADMIN, ADMIN]] {
        let (status, body) = request(
            &control,
            "PUT",
            "/admin/config",
            json!({"expected_revision":1,"config":config}),
            &keys,
        )
        .await;
        assert_eq!(status, StatusCode::UNAUTHORIZED);
        assert!(!body.to_string().contains(ADMIN));
    }
    for (path, value, want) in [
        (
            "/admin/config?key=secret",
            json!({"expected_revision":1,"config":config}),
            StatusCode::BAD_REQUEST,
        ),
        (
            "/admin/config",
            json!({"expected_revision":1,"config":"private-invalid-secret"}),
            StatusCode::UNPROCESSABLE_ENTITY,
        ),
        (
            "/admin/config",
            json!({"expected_revision":1,"config":config,"extra":true}),
            StatusCode::UNPROCESSABLE_ENTITY,
        ),
    ] {
        let (status, body) = request(&control, "PUT", path, value, &[ADMIN]).await;
        assert_eq!(status, want);
        assert!(!body.to_string().contains("private-invalid-secret"));
    }
    assert_eq!(
        control
            .managed
            .lock()
            .await
            .store
            .state()
            .await
            .unwrap()
            .draft
            .revision,
        1
    );
    finish(&control).await;
}

#[tokio::test]
async fn duplicate_publication_keeps_generation_and_restart_settings_reject_before_commit() {
    let (_dir, control, mut config) = setup().await;
    let original = control.host.acquire().unwrap().generation().id;
    assert_eq!(
        request(
            &control,
            "POST",
            "/admin/config/publish",
            json!({"revision":1}),
            &[ADMIN]
        )
        .await
        .0,
        StatusCode::OK
    );
    assert_eq!(control.host.acquire().unwrap().generation().id, original);
    config.server.listen.set_port(19999);
    assert_eq!(
        request(
            &control,
            "PUT",
            "/admin/config",
            json!({"expected_revision":1,"config":config}),
            &[ADMIN]
        )
        .await
        .0,
        StatusCode::OK
    );
    assert_eq!(
        request(
            &control,
            "POST",
            "/admin/config/publish",
            json!({"revision":2}),
            &[ADMIN]
        )
        .await
        .0,
        StatusCode::CONFLICT
    );
    assert_eq!(
        control
            .managed
            .lock()
            .await
            .store
            .state()
            .await
            .unwrap()
            .published
            .revision,
        1
    );
    assert_eq!(control.host.acquire().unwrap().generation().id, original);
    finish(&control).await;
}

#[tokio::test]
async fn concurrent_edits_with_same_revision_cannot_overwrite_each_other() {
    let (_dir, control, config) = setup().await;
    let value = json!({"expected_revision":1,"config":config});
    let (a, b) = tokio::join!(
        request(&control, "PUT", "/admin/config", value.clone(), &[ADMIN]),
        request(&control, "PUT", "/admin/config", value, &[ADMIN])
    );
    assert!(matches!(
        (a.0, b.0),
        (StatusCode::OK, StatusCode::CONFLICT) | (StatusCode::CONFLICT, StatusCode::OK)
    ));
    assert_eq!(
        control
            .managed
            .lock()
            .await
            .store
            .state()
            .await
            .unwrap()
            .draft
            .revision,
        2
    );
    finish(&control).await;
}

#[tokio::test]
async fn disconnected_caller_does_not_cancel_accepted_save() {
    let (_dir, control, config) = setup().await;
    let guard = control.managed.lock().await;
    let owned = control.clone();
    let caller = tokio::spawn(async move {
        owned
            .execute(Command::Save(Save {
                expected_revision: 1,
                config,
            }))
            .await
    });
    tokio::time::timeout(Duration::from_secs(2), async {
        while control.work.is_empty() {
            tokio::task::yield_now().await;
        }
    })
    .await
    .unwrap();
    caller.abort();
    let _ = caller.await;
    drop(guard);
    tokio::time::timeout(Duration::from_secs(2), async {
        while !control.work.is_empty() {
            tokio::task::yield_now().await;
        }
    })
    .await
    .unwrap();
    assert_eq!(
        control
            .managed
            .lock()
            .await
            .store
            .state()
            .await
            .unwrap()
            .draft
            .revision,
        2
    );
    finish(&control).await;
}

#[tokio::test]
async fn postcommit_activation_failure_is_pending_and_restart_target_is_durable() {
    let (dir, control, mut config) = setup().await;
    config.llm.models.get_mut("public").unwrap().backends[0].upstream_model = "replacement".into();
    request(
        &control,
        "PUT",
        "/admin/config",
        json!({"expected_revision":1,"config":config}),
        &[ADMIN],
    )
    .await;
    // Force the real kernel's activation failure after candidate construction can succeed.
    control.host.shutdown().await.unwrap();
    let (status, body) = request(
        &control,
        "POST",
        "/admin/config/publish",
        json!({"revision":2}),
        &[ADMIN],
    )
    .await;
    assert_eq!(status, StatusCode::ACCEPTED);
    assert_eq!(body["publication"], "pending");
    assert_eq!(body["active_revision"], 1);
    let mut state = control.managed.lock().await;
    assert_eq!(state.store.state().await.unwrap().published.revision, 2);
    drop(state);
    control.shutdown().await;
    // Close the actual connection, reopen the same database and build the recovered Host.
    let control = Arc::try_unwrap(control).ok().unwrap();
    control.managed.into_inner().store.close().await.unwrap();
    let mut store = Store::open(&dir.path().join("control.db"), None)
        .await
        .unwrap();
    let recovered = store.state().await.unwrap().published;
    assert_eq!(recovered.revision, 2);
    assert_eq!(
        recovered.config.llm.models["public"].backends[0].upstream_model,
        "replacement"
    );
    let resources = Resources::new(&recovered.config).unwrap();
    let host = crate::bootstrap::host(&recovered.config, &resources)
        .await
        .unwrap();
    assert_eq!(
        host.acquire().unwrap().generation().fingerprint,
        Some(recovered.config.fingerprint().unwrap())
    );
    host.shutdown().await.unwrap();
    store.close().await.unwrap();
}

#[tokio::test]
async fn oversized_admin_body_is_rejected_without_a_write() {
    let (_dir, control, _) = setup().await;
    let (status, body) = request(
        &control,
        "PUT",
        "/admin/config",
        json!({"config":"x".repeat(MAX_CONFIG_BYTES)}),
        &[ADMIN],
    )
    .await;
    assert_eq!(status, StatusCode::PAYLOAD_TOO_LARGE);
    assert_eq!(body["error"]["code"], "invalid_request");
    assert_eq!(
        control
            .managed
            .lock()
            .await
            .store
            .state()
            .await
            .unwrap()
            .draft
            .revision,
        1
    );
    finish(&control).await;
}

#[tokio::test]
async fn accepted_write_timeout_reports_pending_and_eventually_finishes() {
    let (_dir, control, config) = setup().await;
    let guard = control.managed.lock().await;
    let queued = control.clone();
    let caller = tokio::spawn(async move {
        queued
            .execute(Command::Save(Save {
                expected_revision: 1,
                config,
            }))
            .await
    });
    while control.work.is_empty() {
        tokio::task::yield_now().await;
    }
    tokio::time::pause();
    tokio::time::advance(Duration::from_secs(11)).await;
    let response = caller.await.unwrap();
    assert_eq!(response.status(), StatusCode::ACCEPTED);
    let body = to_bytes(response.into_body(), 4096).await.unwrap();
    assert_eq!(
        serde_json::from_slice::<Value>(&body).unwrap(),
        json!({"operation":"pending"})
    );
    tokio::time::resume();
    drop(guard);
    tokio::time::timeout(Duration::from_secs(2), async {
        while !control.work.is_empty() {
            tokio::task::yield_now().await;
        }
    })
    .await
    .unwrap();
    assert_eq!(
        control
            .managed
            .lock()
            .await
            .store
            .state()
            .await
            .unwrap()
            .draft
            .revision,
        2
    );
    finish(&control).await;
}

mod entity;
