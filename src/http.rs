use axum::{
    Router,
    extract::{Request, State},
    http::StatusCode,
    response::{IntoResponse, Response},
    routing::get,
};
use nyro_kernel::Host;
use nyro_llm::runtime::Runtime;
use std::{sync::Arc, time::Duration};
use tokio::time::Instant;
use tokio_util::task::TaskTracker;

mod body;

#[derive(Clone)]
struct App {
    host: Arc<Host<Runtime>>,
    tracker: TaskTracker,
}

pub(crate) fn router(host: Arc<Host<Runtime>>, tracker: TaskTracker) -> Router {
    Router::new()
        .route("/healthz", get(|| async { StatusCode::OK }))
        .route(
            "/readyz",
            get(|State(app): State<App>| async move {
                if app.host.status().accepting {
                    StatusCode::OK
                } else {
                    StatusCode::SERVICE_UNAVAILABLE
                }
            }),
        )
        .fallback(dispatch)
        .with_state(App { host, tracker })
}

async fn dispatch(State(app): State<App>, request: Request) -> Response {
    let lease = match app.host.acquire() {
        Ok(lease) => lease,
        Err(_) => return (StatusCode::SERVICE_UNAVAILABLE, axum::Json(serde_json::json!({"error":{"type":"unavailable","message":"Gateway is not ready"}}))).into_response(),
    };
    let deadline = Instant::now() + lease.value().request_timeout();
    let response = lease.value().handle(request, lease.cancellation()).await;
    let deadline = if response.status().is_success() {
        deadline
    } else {
        Instant::now() + Duration::from_secs(5)
    };
    let (parts, source) = response.into_parts();
    Response::from_parts(parts, body::retain(source, lease, deadline, &app.tracker))
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::bootstrap;
    use axum::{
        body::{Body, to_bytes},
        http::{Request, StatusCode},
        routing::post,
    };
    use nyro_config::Config;
    use nyro_kernel::Context;
    use serde_json::{Value, json};
    use std::time::Duration;
    use tokio::time::Instant;
    use tokio_util::sync::CancellationToken;
    use tower::ServiceExt;

    fn config(url: &str) -> Config {
        Config::from_yaml(&format!(
            r#"
llm:
  providers:
    upstream: {{kind: openai, base_url: '{url}'}}
  models:
    public:
      provider: upstream
      upstream_model: old-model
      workloads: [chat]
      allow_anonymous: true
limit: {{concurrency: 1}}
"#
        ))
        .unwrap()
    }

    fn request() -> Request<Body> {
        Request::builder()
            .method("POST")
            .uri("/v1/chat/completions")
            .header("content-type", "application/json")
            .body(Body::from(
                json!({"model":"public","messages":[{"role":"user","content":"Hello"}]})
                    .to_string(),
            ))
            .unwrap()
    }

    #[tokio::test]
    async fn quota_balance_survives_generation_changes_and_model_removal() {
        use std::sync::atomic::{AtomicUsize, Ordering};
        let calls = Arc::new(AtomicUsize::new(0));
        let observed = calls.clone();
        let upstream = Router::new().route("/v1/chat/completions", post(move || {
            let calls = observed.clone();
            async move {
                calls.fetch_add(1, Ordering::SeqCst);
                axum::Json(json!({"id":"quota","object":"chat.completion","created":1,"model":"private",
                    "choices":[{"index":0,"message":{"role":"assistant","content":"Hi"},"finish_reason":"stop"}],
                    "usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}))
            }
        }));
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let mut config = config(&format!("http://{}/v1", listener.local_addr().unwrap()));
        config.llm.models.get_mut("public").unwrap().quota = Some(nyro_llm::config::QuotaConfig {
            total_tokens: 8,
            reserve_tokens: 5,
        });
        let task = tokio::spawn(async move { axum::serve(listener, upstream).await.unwrap() });
        let resources = bootstrap::Resources::new(&config).unwrap();
        let host = bootstrap::host(&config, &resources).await.unwrap();
        let tracker = TaskTracker::new();
        let router = router(host.clone(), tracker.clone());
        for step in 0..2 {
            if step == 1 {
                config.llm.models.get_mut("public").unwrap().backends[0].upstream_model =
                    "replacement".into();
                host.activate(
                    resources.candidate(&config).unwrap(),
                    Context {
                        deadline: Instant::now() + Duration::from_secs(1),
                        cancellation: CancellationToken::new(),
                    },
                )
                .await
                .unwrap();
            }
            let response = router.clone().oneshot(request()).await.unwrap();
            assert_eq!(response.status(), StatusCode::OK);
            to_bytes(response.into_body(), 16384).await.unwrap();
        }
        let mut removed = config.clone();
        let mut other = removed.llm.models.remove("public").unwrap();
        other.quota = None;
        removed.llm.models.insert("other".into(), other);
        for next in [&removed, &config] {
            host.activate(
                resources.candidate(next).unwrap(),
                Context {
                    deadline: Instant::now() + Duration::from_secs(1),
                    cancellation: CancellationToken::new(),
                },
            )
            .await
            .unwrap();
        }
        let denied = router.clone().oneshot(request()).await.unwrap();
        assert_eq!(denied.status(), StatusCode::TOO_MANY_REQUESTS);
        assert!(!denied.headers().contains_key("retry-after"));
        let body = to_bytes(denied.into_body(), 16384).await.unwrap();
        assert_eq!(
            serde_json::from_slice::<Value>(&body).unwrap()["error"]["code"],
            "quota_exceeded"
        );
        assert_eq!(calls.load(Ordering::SeqCst), 2);
        config
            .llm
            .models
            .get_mut("public")
            .unwrap()
            .quota
            .as_mut()
            .unwrap()
            .total_tokens = 100;
        assert!(resources.candidate(&config).is_err());
        host.shutdown().await.unwrap();
        tracker.close();
        tracker.wait().await;
        task.abort();
    }

    #[tokio::test]
    async fn root_resources_preserve_rate_and_reject_active_policy_changes() {
        use std::sync::atomic::{AtomicUsize, Ordering};

        let calls = Arc::new(AtomicUsize::new(0));
        let observed = calls.clone();
        let upstream = Router::new().route(
            "/v1/chat/completions",
            post(move || {
                let calls = observed.clone();
                async move {
                    calls.fetch_add(1, Ordering::SeqCst);
                    axum::Json(json!({"id":"rate","object":"chat.completion","created":1,"model":"private",
                        "choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}]}))
                }
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let mut config = config(&format!("http://{}/v1", listener.local_addr().unwrap()));
        config.llm.models.get_mut("public").unwrap().rate = Some(nyro_llm::config::RateConfig {
            requests: 1,
            period_ms: 600_000,
            burst: 1,
        });
        let task = tokio::spawn(async move { axum::serve(listener, upstream).await.unwrap() });
        let resources = bootstrap::Resources::new(&config).unwrap();
        let host = bootstrap::host(&config, &resources).await.unwrap();
        let tracker = TaskTracker::new();
        let router = router(host.clone(), tracker.clone());
        let first = router.clone().oneshot(request()).await.unwrap();
        assert_eq!(first.status(), StatusCode::OK);
        to_bytes(first.into_body(), 16384).await.unwrap();

        config.llm.models.get_mut("public").unwrap().backends[0].upstream_model =
            "replacement".into();
        host.activate(
            resources.candidate(&config).unwrap(),
            Context {
                deadline: Instant::now() + Duration::from_secs(1),
                cancellation: CancellationToken::new(),
            },
        )
        .await
        .unwrap();

        // A rejected candidate must leave the current generation's depleted bucket intact.
        config
            .llm
            .models
            .get_mut("public")
            .unwrap()
            .rate
            .as_mut()
            .unwrap()
            .burst = 2;
        assert!(resources.candidate(&config).is_err());
        assert!(host.status().accepting);
        let denied = router.clone().oneshot(request()).await.unwrap();
        assert_eq!(denied.status(), StatusCode::TOO_MANY_REQUESTS);
        assert!(denied.headers().contains_key("retry-after"));
        let body = to_bytes(denied.into_body(), 16384).await.unwrap();
        assert_eq!(
            serde_json::from_slice::<Value>(&body).unwrap()["error"]["code"],
            "rate_limit_exceeded"
        );
        assert_eq!(calls.load(Ordering::SeqCst), 1);
        host.shutdown().await.unwrap();
        tracker.close();
        tracker.wait().await;
        task.abort();
    }

    #[tokio::test]
    async fn root_resources_preserve_open_health_across_generation_changes() {
        use std::sync::atomic::{AtomicUsize, Ordering};

        let calls = Arc::new(AtomicUsize::new(0));
        let observed = calls.clone();
        let upstream = Router::new().route(
            "/v1/chat/completions",
            post(move || {
                let calls = observed.clone();
                async move {
                    calls.fetch_add(1, Ordering::SeqCst);
                    StatusCode::SERVICE_UNAVAILABLE
                }
            }),
        );
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let mut config = config(&format!("http://{}/v1", listener.local_addr().unwrap()));
        config.llm.models.get_mut("public").unwrap().health =
            Some(nyro_llm::config::HealthConfig {
                failure_threshold: 1,
                cooldown_ms: 30_000,
            });
        let task = tokio::spawn(async move { axum::serve(listener, upstream).await.unwrap() });
        let resources = bootstrap::Resources::new(&config).unwrap();
        let host = bootstrap::host(&config, &resources).await.unwrap();
        let tracker = TaskTracker::new();
        let router = router(host.clone(), tracker.clone());
        for step in 0..3 {
            if step > 0 {
                let backend = &mut config.llm.models.get_mut("public").unwrap().backends[0];
                if step == 1 {
                    backend.weight = 20;
                    backend.priority = 5;
                } else {
                    backend.upstream_model = "replacement".into();
                }
                host.activate(
                    resources.candidate(&config).unwrap(),
                    Context {
                        deadline: Instant::now() + Duration::from_secs(1),
                        cancellation: CancellationToken::new(),
                    },
                )
                .await
                .unwrap();
            }
            let response = router.clone().oneshot(request()).await.unwrap();
            assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
            let body = to_bytes(response.into_body(), 16384).await.unwrap();
            let payload: Value = serde_json::from_slice(&body).unwrap();
            assert_eq!(
                payload["error"]["code"],
                if step == 1 {
                    "backends_unavailable"
                } else {
                    "upstream_error"
                }
            );
            assert_eq!(calls.load(Ordering::SeqCst), if step == 2 { 2 } else { 1 });
        }
        host.shutdown().await.unwrap();
        tracker.close();
        tracker.wait().await;
        task.abort();
    }

    #[tokio::test]
    async fn ready_and_dispatch_use_the_active_generation_and_shared_limit() {
        let upstream = Router::new().route("/v1/chat/completions", post(|axum::Json(input):axum::Json<Value>| async move {
            axum::Json(json!({"id":"test","object":"chat.completion","created":1,"model":input["model"],"choices":[{"index":0,"message":{"role":"assistant","content":input["model"]},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}))
        }));
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let config = config(&format!("http://{}/v1", listener.local_addr().unwrap()));
        let task = tokio::spawn(async move { axum::serve(listener, upstream).await.unwrap() });
        let resources = bootstrap::Resources::new(&config).unwrap();
        let host = bootstrap::host(&config, &resources).await.unwrap();
        let tracker = TaskTracker::new();
        let router = router(host.clone(), tracker.clone());
        assert_eq!(
            router
                .clone()
                .oneshot(
                    Request::builder()
                        .uri("/readyz")
                        .body(Body::empty())
                        .unwrap()
                )
                .await
                .unwrap()
                .status(),
            StatusCode::OK
        );
        let old = router.clone().oneshot(request()).await.unwrap();
        assert_eq!(old.status(), StatusCode::OK);
        assert_eq!(host.status().active.unwrap().leases, 1);
        let mut next = config.clone();
        next.llm.models.get_mut("public").unwrap().backends[0].upstream_model = "new-model".into();
        host.activate(
            resources.candidate(&next).unwrap(),
            Context {
                deadline: Instant::now() + Duration::from_secs(1),
                cancellation: CancellationToken::new(),
            },
        )
        .await
        .unwrap();
        assert_eq!(host.status().retiring.len(), 1);
        let denied = router.clone().oneshot(request()).await.unwrap();
        assert_eq!(denied.status(), StatusCode::TOO_MANY_REQUESTS);
        drop(denied);
        let payload: Value =
            serde_json::from_slice(&to_bytes(old.into_body(), 16384).await.unwrap()).unwrap();
        assert_eq!(payload["choices"][0]["message"]["content"], "old-model");
        let next = router.clone().oneshot(request()).await.unwrap();
        let payload: Value =
            serde_json::from_slice(&to_bytes(next.into_body(), 16384).await.unwrap()).unwrap();
        assert_eq!(payload["model"], "public");
        assert_eq!(payload["choices"][0]["message"]["content"], "new-model");
        host.shutdown().await.unwrap();
        assert_eq!(
            router
                .oneshot(
                    Request::builder()
                        .uri("/readyz")
                        .body(Body::empty())
                        .unwrap()
                )
                .await
                .unwrap()
                .status(),
            StatusCode::SERVICE_UNAVAILABLE
        );
        tracker.close();
        tracker.wait().await;
        task.abort();
    }
}
