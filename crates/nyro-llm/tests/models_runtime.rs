use std::sync::Arc;

use axum::{
    body::{Body, to_bytes},
    http::{Request, StatusCode},
};
use nyro_limit::ConcurrencyLimit;
use nyro_llm::{
    config,
    runtime::{Options, Runtime},
};
use nyro_security::{ApiKey, ApiKeys};
use serde_json::{Value, json};
use tokio_util::sync::CancellationToken;

fn config() -> config::Config {
    serde_json::from_value(json!({
        "providers": {"private-provider": {
            "kind": "openai", "base_url": "http://127.0.0.1:1/private",
            "api_key": "upstream-secret"
        }},
        "models": {
            "z-public": {"provider": "private-provider", "upstream_model": "private-z",
                "workloads": ["chat"], "allow_anonymous": true},
            "b-alice": {"provider": "private-provider", "upstream_model": "private-b",
                "workloads": ["chat", "embedding"], "subjects": ["alice"]},
            "a-public": {"provider": "private-provider", "upstream_model": "private-a",
                "workloads": ["embedding"], "allow_anonymous": true},
            "c-bob": {"provider": "private-provider", "upstream_model": "private-c",
                "workloads": ["chat"], "subjects": ["bob"]}
        }
    }))
    .unwrap()
}

fn runtime(config: config::Config, limit: ConcurrencyLimit) -> Runtime {
    let keys = ApiKeys::new(
        ["alice", "bob", "unbound"]
            .into_iter()
            .map(|id| ApiKey {
                id: id.into(),
                secret: format!("{id}-secret"),
            })
            .collect(),
    )
    .unwrap();
    Runtime::new(config, Arc::new(keys), limit, Options::default()).unwrap()
}

fn request(method: &str, path: &str, headers: &[(&str, &str)]) -> Request<Body> {
    let mut builder = Request::builder().method(method).uri(path);
    for (name, value) in headers {
        builder = builder.header(*name, *value);
    }
    builder.body(Body::empty()).unwrap()
}

async fn list(runtime: &Runtime, headers: &[(&str, &str)]) -> Value {
    let response = runtime
        .handle(
            request("GET", "/v1/models", headers),
            CancellationToken::new(),
        )
        .await;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(response.headers()["content-type"], "application/json");
    assert_eq!(response.headers()["cache-control"], "no-store");
    assert!(response.headers().contains_key("x-request-id"));
    serde_json::from_slice(&to_bytes(response.into_body(), 8192).await.unwrap()).unwrap()
}

#[tokio::test]
async fn lists_only_authorized_aliases_in_order_without_inference_admission() {
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(config(), limit.clone());
    // Discovery must remain available when the inference concurrency budget is occupied.
    let _held = limit.try_acquire().unwrap();
    for (headers, aliases) in [
        (vec![], vec!["a-public", "z-public"]),
        (
            vec![("authorization", "Bearer alice-secret")],
            vec!["a-public", "b-alice", "z-public"],
        ),
        (
            vec![("authorization", "bearer bob-secret")],
            vec!["a-public", "c-bob", "z-public"],
        ),
        (
            vec![("authorization", "Bearer unbound-secret")],
            vec!["a-public", "z-public"],
        ),
    ] {
        let expected: Vec<_> = aliases
            .into_iter()
            .map(|alias| {
                json!({
                    "id": alias, "object": "model", "created": 0, "owned_by": "Nyro"
                })
            })
            .collect();
        // Full schema equality catches accidental provider/configuration disclosure too.
        assert_eq!(
            list(&runtime, &headers).await,
            json!({"object": "list", "data": expected})
        );
    }
    assert_eq!(limit.available(), 0);
}

#[tokio::test]
async fn no_visible_models_returns_an_empty_list() {
    let mut config = config();
    for model in config.models.values_mut() {
        model.allow_anonymous = false;
    }
    let runtime = runtime(config, ConcurrencyLimit::new(1).unwrap());
    for headers in [vec![], vec![("authorization", "Bearer unbound-secret")]] {
        assert_eq!(
            list(&runtime, &headers).await,
            json!({"object": "list", "data": []})
        );
    }
}

#[tokio::test]
async fn invalid_or_ambiguous_credentials_cannot_fall_back_to_public_models() {
    let runtime = runtime(config(), ConcurrencyLimit::new(1).unwrap());
    for headers in [
        vec![("authorization", "Bearer wrong-secret")],
        vec![("authorization", "Basic alice-secret")],
        vec![("authorization", "Bearer ")],
        vec![("authorization", "Bearer alice-secret, Bearer bob-secret")],
        vec![
            ("authorization", "Bearer alice-secret"),
            ("authorization", "Bearer alice-secret"),
        ],
        vec![
            ("authorization", "Bearer alice-secret"),
            ("x-api-key", "alice-secret"),
        ],
        vec![
            ("authorization", "Bearer alice-secret"),
            ("x-goog-api-key", "alice-secret"),
        ],
        vec![("x-api-key", "alice-secret")],
        vec![("x-goog-api-key", "alice-secret")],
    ] {
        let response = runtime
            .handle(
                request("GET", "/v1/models", &headers),
                CancellationToken::new(),
            )
            .await;
        assert_eq!(response.status(), StatusCode::UNAUTHORIZED, "{headers:?}");
        let bytes = to_bytes(response.into_body(), 8192).await.unwrap();
        let error: Value = serde_json::from_slice(&bytes).unwrap();
        assert_eq!(error["error"]["code"], "authentication_error");
        assert!(!String::from_utf8_lossy(&bytes).contains("secret"));
        assert!(error.get("data").is_none());
    }
}

#[tokio::test]
async fn model_discovery_rejects_unsupported_methods_and_queries() {
    let runtime = runtime(config(), ConcurrencyLimit::new(1).unwrap());
    for (method, path, expected) in [
        ("POST", "/v1/models", StatusCode::METHOD_NOT_ALLOWED),
        ("DELETE", "/v1/models", StatusCode::METHOD_NOT_ALLOWED),
        ("GET", "/v1/models?limit=1", StatusCode::BAD_REQUEST),
        ("GET", "/v1/models?alt=sse", StatusCode::BAD_REQUEST),
        (
            "GET",
            "/v1/models?%6bey=alice-secret",
            StatusCode::UNAUTHORIZED,
        ),
        (
            "GET",
            "/v1/models?api_key=alice-secret",
            StatusCode::UNAUTHORIZED,
        ),
        (
            "GET",
            "/v1/models?access_token=alice-secret",
            StatusCode::UNAUTHORIZED,
        ),
    ] {
        let response = runtime
            .handle(request(method, path, &[]), CancellationToken::new())
            .await;
        assert_eq!(response.status(), expected, "{method} {path}");
    }
}
