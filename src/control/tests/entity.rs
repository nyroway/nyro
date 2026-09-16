use super::*;

#[tokio::test]
async fn entity_queries_hide_credentials_and_explicit_export_retains_them() {
    let (_dir, control, mut config) = setup().await;
    let provider = config.llm.providers.get_mut("p").unwrap();
    provider.api_key = Some("provider-private-secret".into());
    provider.transport.proxy_url = Some("http://user:proxy-private-secret@localhost:8080".into());
    config.security.api_keys.push(ApiKey {
        id: "client".into(),
        secret: "client-private-secret".into(),
        enabled: true,
        expires_at: None,
    });
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
    for path in [
        "/admin/providers",
        "/admin/providers/p",
        "/admin/api-keys",
        "/admin/api-keys/client",
        "/admin/config",
    ] {
        let (status, value) = request(&control, "GET", path, json!(null), &[ADMIN]).await;
        assert_eq!(status, StatusCode::OK, "{path}");
        for secret in [
            "provider-private-secret",
            "proxy-private-secret",
            "client-private-secret",
        ] {
            assert!(
                !value.to_string().contains(secret),
                "credential leaked at {path}"
            );
        }
    }
    let (status, value) = request(
        &control,
        "GET",
        "/admin/config/export",
        json!(null),
        &[ADMIN],
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(
        value["draft"]["config"]["llm"]["providers"]["p"]["api_key"],
        "provider-private-secret"
    );
    assert_eq!(
        request(&control, "GET", "/admin/config/export", json!(null), &[])
            .await
            .0,
        StatusCode::UNAUTHORIZED
    );
    finish(&control).await;
}

#[tokio::test]
async fn entity_crud_preserves_secrets_enforces_references_and_never_publishes() {
    let (_dir, control, _) = setup().await;
    let provider = json!({"kind":"openai","base_url":"http://example.test/v1","api_key":{"action":"set","value":"provider-secret"},"transport":{"proxy_url":{"action":"set","value":"http://user:proxy-secret@localhost:8080"}}});
    let (status, body) = request(
        &control,
        "POST",
        "/admin/providers",
        json!({"expected_revision":1,"id":"new/provider","value":provider}),
        &[ADMIN],
    )
    .await;
    assert_eq!(status, StatusCode::CREATED);
    assert_eq!(body["draft_revision"], 2);
    let (status, body) = request(
        &control,
        "GET",
        "/admin/providers/new%2Fprovider",
        json!(null),
        &[ADMIN],
    )
    .await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(body["item"]["id"], "new/provider");
    assert_eq!(body["item"]["value"]["has_api_key"], true);
    assert_eq!(body["item"]["value"]["transport"]["has_proxy_url"], true);
    let changed = json!({"kind":"openai","base_url":"http://changed.test/v1"});
    assert_eq!(
        request(
            &control,
            "PUT",
            "/admin/providers/new%2Fprovider",
            json!({"expected_revision":2,"value":changed}),
            &[ADMIN]
        )
        .await
        .0,
        StatusCode::OK
    );
    let raw = request(
        &control,
        "GET",
        "/admin/config/export",
        json!(null),
        &[ADMIN],
    )
    .await
    .1;
    assert_eq!(
        raw["draft"]["config"]["llm"]["providers"]["new/provider"]["api_key"],
        "provider-secret"
    );
    assert_eq!(
        raw["draft"]["config"]["llm"]["providers"]["new/provider"]["transport"]["proxy_url"],
        "http://user:proxy-secret@localhost:8080"
    );
    assert_eq!(request(&control,"POST","/admin/api-keys",json!({"expected_revision":3,"id":"client","value":{"secret":{"action":"set","value":"client-secret"}}}),&[ADMIN]).await.0,StatusCode::CREATED);
    let mut model = json!({"provider":"new/provider","upstream_model":"example","workloads":["chat"],"subjects":["client"]});
    assert_eq!(
        request(
            &control,
            "POST",
            "/admin/models",
            json!({"expected_revision":4,"id":"second","value":model}),
            &[ADMIN]
        )
        .await
        .0,
        StatusCode::CREATED
    );
    for path in ["/admin/providers/new%2Fprovider", "/admin/api-keys/client"] {
        let (status, body) = request(
            &control,
            "DELETE",
            path,
            json!({"expected_revision":5}),
            &[ADMIN],
        )
        .await;
        assert_eq!(status, StatusCode::CONFLICT);
        assert_eq!(body["error"]["code"], "entity_referenced");
    }
    model["subjects"] = json!([]);
    model["allow_anonymous"] = json!(true);
    assert_eq!(
        request(
            &control,
            "PUT",
            "/admin/models/second",
            json!({"expected_revision":5,"value":model}),
            &[ADMIN]
        )
        .await
        .0,
        StatusCode::OK
    );
    for (revision, path) in [
        (6, "/admin/api-keys/client"),
        (7, "/admin/models/second"),
        (8, "/admin/providers/new%2Fprovider"),
    ] {
        assert_eq!(
            request(
                &control,
                "DELETE",
                path,
                json!({"expected_revision":revision}),
                &[ADMIN]
            )
            .await
            .0,
            StatusCode::OK
        );
    }
    let state = request(&control, "GET", "/admin/config", json!(null), &[ADMIN])
        .await
        .1;
    assert_eq!(state["draft"]["revision"], 9);
    assert_eq!(state["published_revision"], 1);
    assert_eq!(state["active_revision"], 1);
    finish(&control).await;
}

#[tokio::test]
async fn invalid_entity_requests_do_not_mutate_or_disclose_the_payload() {
    let (_dir, control, _) = setup().await;
    for path in ["/admin/providers", "/admin/models", "/admin/api-keys"] {
        assert_eq!(
            request(
                &control,
                "POST",
                path,
                json!({"expected_revision":1,"id":"invalid","value":{"secret":"must-not-echo"}}),
                &[]
            )
            .await
            .0,
            StatusCode::UNAUTHORIZED
        );
        let (status, body) = request(
            &control,
            "POST",
            path,
            json!({"expected_revision":1,"id":"invalid","value":{"secret":"must-not-echo"}}),
            &[ADMIN],
        )
        .await;
        assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY);
        assert!(!body.to_string().contains("must-not-echo"));
    }
    assert_eq!(
        request(
            &control,
            "DELETE",
            "/admin/models/missing",
            json!({"expected_revision":1}),
            &[ADMIN]
        )
        .await
        .0,
        StatusCode::NOT_FOUND
    );
    assert_eq!(
        request(
            &control,
            "DELETE",
            "/admin/models/public",
            json!({"expected_revision":0}),
            &[ADMIN]
        )
        .await
        .0,
        StatusCode::CONFLICT
    );
    assert_eq!(
        request(
            &control,
            "DELETE",
            "/admin/models/public",
            json!({"expected_revision":1}),
            &[ADMIN]
        )
        .await
        .0,
        StatusCode::UNPROCESSABLE_ENTITY
    );
    assert_eq!(
        request(
            &control,
            "DELETE",
            "/admin/models/public",
            json!({"expected_revision":1,"extra":true}),
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
        1
    );
    finish(&control).await;
}
