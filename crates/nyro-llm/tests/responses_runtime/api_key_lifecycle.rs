//! All protected HTTP surfaces must apply the same credential lifecycle rules.
use super::*;

#[tokio::test]
async fn inactive_keys_fail_before_admission_for_every_surface() {
    for native in [false, true] {
        for anonymous in [false, true] {
            let fixture = upstream("openai", "normal").await;
            let config: Config = serde_json::from_value(json!({
                "providers":{"p":{"kind":"openai","base_url":format!("{}/v1",fixture.base),"api_key":"upstream-secret","native_chat":native}},
                "models":{"public":{"provider":"p","upstream_model":"internal","workloads":["chat","embedding"],"subjects":["alice"],"allow_anonymous":anonymous,
                    "rate":{"requests":1,"period_ms":600000},"quota":{"total_tokens":5,"reserve_tokens":5}}}
            })).unwrap();
            let keys = ApiKeys::new(vec![
                ApiKey {
                    id: "disabled".into(),
                    secret: "disabled-secret".into(),
                    enabled: false,
                    expires_at: None,
                },
                ApiKey {
                    id: "expired".into(),
                    secret: "expired-secret".into(),
                    enabled: true,
                    expires_at: Some(0),
                },
                ApiKey {
                    id: "alice".into(),
                    secret: "client-secret".into(),
                    enabled: true,
                    expires_at: None,
                },
            ])
            .unwrap();
            let limit = ConcurrencyLimit::new(1).unwrap();
            let gateway =
                Runtime::new(config, Arc::new(keys), limit.clone(), Options::default()).unwrap();
            let held = limit.try_acquire().unwrap();
            for secret in ["disabled-secret", "expired-secret", "unknown-secret"] {
                for surface in [
                    "openai",
                    "anthropic",
                    "gemini",
                    "responses",
                    "embedding",
                    "models",
                ] {
                    for streaming in [false, true] {
                        if streaming && matches!(surface, "models" | "embedding") {
                            continue;
                        }
                        let mut request = match surface {
                            "models" => Request::builder()
                                .uri("/v1/models")
                                .body(Body::empty())
                                .unwrap(),
                            "embedding" => Request::builder()
                                .method("POST")
                                .uri("/v1/embeddings")
                                .header("content-type", "application/json")
                                .body(Body::from(
                                    json!({"model":"public","input":"hello"}).to_string(),
                                ))
                                .unwrap(),
                            _ => request(surface, streaming),
                        };
                        for header in ["authorization", "x-api-key", "x-goog-api-key"] {
                            request.headers_mut().remove(header);
                        }
                        let header = match surface {
                            "anthropic" => "x-api-key",
                            "gemini" => "x-goog-api-key",
                            _ => "authorization",
                        };
                        request.headers_mut().insert(
                            header,
                            if header == "authorization" {
                                format!("Bearer {secret}")
                            } else {
                                secret.into()
                            }
                            .parse()
                            .unwrap(),
                        );
                        let result = gateway.handle(request, CancellationToken::new()).await;
                        assert_eq!(
                            result.status(),
                            StatusCode::UNAUTHORIZED,
                            "{surface} native={native} anonymous={anonymous}"
                        );
                        let bytes = to_bytes(result.into_body(), 65536).await.unwrap();
                        assert!(!String::from_utf8_lossy(&bytes).contains(secret));
                        assert_eq!(limit.available(), 0);
                    }
                }
            }
            assert!(fixture.calls.lock().unwrap().is_empty());
            drop(held);
            let list = gateway
                .handle(
                    Request::builder()
                        .uri("/v1/models")
                        .header("authorization", "Bearer client-secret")
                        .body(Body::empty())
                        .unwrap(),
                    CancellationToken::new(),
                )
                .await;
            assert_eq!(list.status(), StatusCode::OK);
            let list: Value =
                serde_json::from_slice(&to_bytes(list.into_body(), 65536).await.unwrap()).unwrap();
            assert_eq!(list["data"][0]["id"], "public");
            // Rejected keys and model discovery must consume neither rate nor quota.
            let result = gateway
                .handle(request("openai", false), CancellationToken::new())
                .await;
            assert_eq!(result.status(), StatusCode::OK);
            to_bytes(result.into_body(), 65536).await.unwrap();
            assert_eq!(limit.available(), 1);
            assert_eq!(fixture.calls.lock().unwrap().len(), 1);
        }
    }
}

#[tokio::test]
async fn expiry_is_checked_after_a_slow_request_body_arrives() {
    use std::time::{SystemTime, UNIX_EPOCH};
    let fixture = upstream("openai", "normal").await;
    let expires_at = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_secs()
        + 5;
    let keys = ApiKeys::new(vec![ApiKey {
        id: "alice".into(),
        secret: "client-secret".into(),
        enabled: true,
        expires_at: Some(expires_at),
    }])
    .unwrap();
    let config = serde_json::from_value(json!({
        "providers":{"p":{"kind":"openai","base_url":format!("{}/v1",fixture.base)}},
        "models":{"public":{"provider":"p","upstream_model":"internal","subjects":["alice"],"workloads":["chat"]}}
    })).unwrap();
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway =
        Arc::new(Runtime::new(config, Arc::new(keys), limit.clone(), Options::default()).unwrap());
    let (parts, original) = request("openai", false).into_parts();
    let bytes = to_bytes(original, 65536).await.unwrap();
    let (started, received) = tokio::sync::oneshot::channel();
    let (release, wait) = tokio::sync::oneshot::channel();
    let body = Body::from_stream(futures::stream::once(async move {
        started.send(()).unwrap();
        wait.await.unwrap();
        Ok::<_, std::io::Error>(bytes)
    }));
    let pending = tokio::spawn(async move {
        gateway
            .handle(Request::from_parts(parts, body), CancellationToken::new())
            .await
    });
    tokio::time::timeout(Duration::from_secs(5), received)
        .await
        .unwrap()
        .unwrap();
    assert_eq!(limit.available(), 1);
    assert!(fixture.calls.lock().unwrap().is_empty());
    assert!(
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_secs()
            < expires_at,
        "request must start before expiry"
    );
    tokio::time::timeout(Duration::from_secs(10), async {
        while SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_secs()
            < expires_at
        {
            tokio::time::sleep(Duration::from_millis(10)).await;
        }
    })
    .await
    .unwrap();
    release.send(()).unwrap();
    let result = tokio::time::timeout(Duration::from_secs(5), pending)
        .await
        .unwrap()
        .unwrap();
    assert_eq!(result.status(), StatusCode::UNAUTHORIZED);
    to_bytes(result.into_body(), 65536).await.unwrap();
    assert!(fixture.calls.lock().unwrap().is_empty());
    assert_eq!(limit.available(), 1);
}
