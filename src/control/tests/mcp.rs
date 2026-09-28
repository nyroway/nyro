use super::*;
#[tokio::test]
async fn mcp_full_snapshot_save_publish_and_export() {
    let (_dir, control, mut config) = setup().await;
    config.security.api_keys.push(ApiKey {
        id: "mcp-client".into(),
        secret: "mcp-client-secret".into(),
        enabled: true,
        expires_at: None,
    });
    config.mcp=Some(serde_json::from_value(json!({"servers":{"knowledge":{"transport":"http","url":"http://127.0.0.1:1/mcp","bearer_token":"mcp-upstream-secret","subjects":["mcp-client"],"allowed_tools":["read"]}}})).unwrap());
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
    let probe = || {
        Request::builder()
            .method("POST")
            .uri("/mcp/knowledge")
            .body(Body::empty())
            .unwrap()
    };
    assert_eq!(
        control
            .host
            .acquire()
            .unwrap()
            .value()
            .mcp
            .handle(probe(), CancellationToken::new())
            .await
            .status(),
        StatusCode::NOT_FOUND
    );
    let (status, view) = request(&control, "GET", "/admin/config", json!(null), &[ADMIN]).await;
    assert_eq!(status, StatusCode::OK);
    assert!(!view.to_string().contains("mcp-upstream-secret"));
    assert_eq!(
        view["draft"]["config"]["mcp"]["servers"]["knowledge"]["has_bearer_token"],
        true
    );
    let (_, export) = request(
        &control,
        "GET",
        "/admin/config/export",
        json!(null),
        &[ADMIN],
    )
    .await;
    let imported: Config = serde_json::from_value(export["draft"]["config"].clone()).unwrap();
    assert_eq!(
        imported.fingerprint().unwrap(),
        config.fingerprint().unwrap()
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
        StatusCode::OK
    );
    assert_eq!(
        control
            .host
            .acquire()
            .unwrap()
            .value()
            .mcp
            .handle(probe(), CancellationToken::new())
            .await
            .status(),
        StatusCode::UNAUTHORIZED
    );
    config
        .mcp
        .as_mut()
        .unwrap()
        .servers
        .get_mut("knowledge")
        .unwrap()
        .subjects = vec!["missing".into()];
    assert_eq!(
        request(
            &control,
            "PUT",
            "/admin/config",
            json!({"expected_revision":2,"config":config}),
            &[ADMIN]
        )
        .await
        .0,
        StatusCode::UNPROCESSABLE_ENTITY
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
            .draft
            .revision,
        2
    );
    finish(&control).await;
}
