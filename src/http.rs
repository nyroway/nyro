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
        next.llm.models.get_mut("public").unwrap().upstream_model = "new-model".into();
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
