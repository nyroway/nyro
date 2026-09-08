//! Request-rate contracts against independent, hand-authored HTTP wire fixtures.
use std::{
    sync::{
        Arc, Mutex,
        atomic::{AtomicUsize, Ordering},
    },
    time::Duration,
};

use axum::{
    Router,
    body::{Body, to_bytes},
    http::{Request, Response, StatusCode},
    routing::post,
};
use futures::StreamExt;
use nyro_limit::ConcurrencyLimit;
use nyro_llm::{
    config::Config,
    runtime::{Options, Runtime},
};
use nyro_security::{ApiKey, ApiKeys};
use serde_json::{Value, json};
use tokio_util::sync::CancellationToken;

#[test]
fn model_rate_configuration_is_accepted() {
    let config = serde_json::from_value::<Config>(json!({
        "providers":{"p":{"kind":"openai","base_url":"http://127.0.0.1:1/v1"}},
        "models":{"public":{"provider":"p","upstream_model":"private","workloads":["chat"],
            "rate":{"requests":10,"period_ms":1000,"burst":2}}}
    }));
    assert!(config.is_ok(), "{config:?}");
}

#[derive(Clone, Copy)]
enum Reply {
    Good,
    Status(u16),
    Slow,
    HangingStream,
}

struct Upstream {
    base: String,
    calls: Arc<AtomicUsize>,
    reply: Arc<Mutex<Reply>>,
    task: tokio::task::JoinHandle<()>,
}

impl Upstream {
    fn count(&self) -> usize {
        self.calls.load(Ordering::SeqCst)
    }
    async fn wait_for_call(&self) {
        tokio::time::timeout(Duration::from_secs(3), async {
            while self.count() == 0 {
                tokio::task::yield_now().await;
            }
        })
        .await
        .expect("upstream should receive admitted request");
    }
}

impl Drop for Upstream {
    fn drop(&mut self) {
        self.task.abort();
    }
}

const CHAT: &str = r#"{"id":"answer","object":"chat.completion","created":1,"model":"private","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}"#;
const EMBEDDING: &str = r#"{"object":"list","model":"private","data":[{"object":"embedding","index":0,"embedding":[0.25]}],"usage":{"prompt_tokens":1,"total_tokens":1}}"#;
const ANTHROPIC: &str = r#"{"id":"answer","type":"message","role":"assistant","model":"private","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}"#;
const FRAME: &str = "data: {\"id\":\"answer\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"private\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n";

async fn upstream(reply: Reply) -> Upstream {
    let calls = Arc::new(AtomicUsize::new(0));
    let replies = Arc::new(Mutex::new(reply));
    let observed = calls.clone();
    let configured = replies.clone();
    let app = Router::new().fallback(post(move |request: Request<Body>| {
        let observed = observed.clone();
        let configured = configured.clone();
        async move {
            let path = request.uri().path().to_owned();
            let _: Value =
                serde_json::from_slice(&to_bytes(request.into_body(), 16384).await.unwrap())
                    .unwrap();
            let reply = *configured.lock().unwrap();
            observed.fetch_add(1, Ordering::SeqCst);
            if matches!(reply, Reply::Slow) {
                tokio::time::sleep(Duration::from_secs(30)).await;
            }
            if let Reply::Status(status) = reply {
                return Response::builder()
                    .status(status)
                    .body(Body::from("private provider-secret failure"))
                    .unwrap();
            }
            if matches!(reply, Reply::HangingStream) {
                let stream = futures::stream::once(async { Ok::<_, std::io::Error>(FRAME) })
                    .chain(futures::stream::pending());
                return Response::builder()
                    .header("content-type", "text/event-stream")
                    .body(Body::from_stream(stream))
                    .unwrap();
            }
            let body = if path.ends_with("embeddings") {
                EMBEDDING
            } else if path.ends_with("messages") {
                ANTHROPIC
            } else {
                CHAT
            };
            Response::builder()
                .header("content-type", "application/json")
                .body(Body::from(body))
                .unwrap()
        }
    }));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base = format!("http://{}/v1", listener.local_addr().unwrap());
    let task = tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });
    Upstream {
        base,
        calls,
        reply: replies,
        task,
    }
}

fn configuration(upstreams: &[&Upstream], burst: u32) -> Config {
    let providers: serde_json::Map<_, _> = upstreams
        .iter()
        .enumerate()
        .map(|(i, u)| {
            (
                format!("p{i}"),
                json!({"kind":"openai","base_url":u.base,"api_key":"provider-secret"}),
            )
        })
        .collect();
    let backends: Vec<_> = upstreams.iter().enumerate().map(|(i, _)| {
        json!({"id":format!("b{i}"),"provider":format!("p{i}"),"upstream_model":"private","priority":i})
    }).collect();
    serde_json::from_value(json!({"providers":providers,"models":{"public":{
        "backends":backends,"max_attempts":upstreams.len(),"workloads":["chat","embedding"],
        "subjects":["alice","bob"],"rate":{"requests":1,"period_ms":60000,"burst":burst}
    }}}))
    .unwrap()
}

fn runtime(config: Config, limit: &ConcurrencyLimit, options: Options) -> Runtime {
    Runtime::new(
        config,
        Arc::new(
            ApiKeys::new(vec![
                ApiKey {
                    id: "alice".into(),
                    secret: "alice-secret".into(),
                },
                ApiKey {
                    id: "bob".into(),
                    secret: "bob-secret".into(),
                },
            ])
            .unwrap(),
        ),
        limit.clone(),
        options,
    )
    .unwrap()
}

fn request(path: &str, body: Value, credential: Option<&str>) -> Request<Body> {
    let mut builder = Request::builder()
        .method("POST")
        .uri(path)
        .header("content-type", "application/json");
    if let Some(secret) = credential {
        builder = builder.header("authorization", format!("Bearer {secret}"));
    }
    builder.body(Body::from(body.to_string())).unwrap()
}

fn chat(model: &str, streaming: bool) -> Value {
    json!({"model":model,"messages":[{"role":"user","content":"Hi"}],"max_tokens":16,"stream":streaming})
}

async fn invoke(runtime: &Runtime) -> Response<Body> {
    runtime
        .handle(
            request(
                "/v1/chat/completions",
                chat("public", false),
                Some("alice-secret"),
            ),
            CancellationToken::new(),
        )
        .await
}

async fn consume(response: Response<Body>) -> Value {
    serde_json::from_slice(&to_bytes(response.into_body(), 16384).await.unwrap()).unwrap()
}

async fn assert_rate(response: Response<Body>) {
    assert_eq!(response.status(), StatusCode::TOO_MANY_REQUESTS);
    let retry: u64 = response.headers()["retry-after"]
        .to_str()
        .unwrap()
        .parse()
        .unwrap();
    assert!(retry > 0 && retry <= 60);
    let body = consume(response).await;
    assert_eq!(body["error"]["code"], "rate_limit_exceeded");
    assert!(!body.to_string().contains("private"));
    assert!(!body.to_string().contains("secret"));
}

#[tokio::test]
async fn one_model_shares_rate_across_callers_protocols_and_workloads() {
    let upstream = upstream(Reply::Good).await;
    let mut config = configuration(&[&upstream], 4);
    config.models.get_mut("public").unwrap().allow_anonymous = true;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(config, &limit, Options::default());
    let inputs = [
        (
            "/v1/chat/completions",
            chat("public", false),
            Some("alice-secret"),
        ),
        (
            "/v1/embeddings",
            json!({"model":"public","input":"Hi"}),
            Some("bob-secret"),
        ),
        (
            "/v1/messages",
            json!({"model":"public","messages":[{"role":"user","content":"Hi"}],"max_tokens":16}),
            None,
        ),
        (
            "/v1beta/models/public:generateContent",
            json!({"contents":[{"role":"user","parts":[{"text":"Hi"}]}],"generationConfig":{"maxOutputTokens":16}}),
            Some("alice-secret"),
        ),
    ];
    for (path, body, credential) in &inputs {
        let response = runtime
            .handle(
                request(path, body.clone(), *credential),
                CancellationToken::new(),
            )
            .await;
        assert_eq!(response.status(), StatusCode::OK, "{path}");
        consume(response).await;
    }
    for (path, body, credential) in inputs {
        let response = runtime
            .handle(request(path, body, credential), CancellationToken::new())
            .await;
        assert_eq!(response.status(), StatusCode::TOO_MANY_REQUESTS, "{path}");
        assert!(response.headers().contains_key("retry-after"));
        assert_eq!(
            limit.available(),
            1,
            "unconsumed rate error must release permit"
        );
        let body = consume(response).await;
        if path == "/v1/messages" {
            assert_eq!(body["type"], "error");
            assert_eq!(body["error"]["type"], "rate_limit_error");
        } else if path.contains("generateContent") {
            assert_eq!(body["error"]["code"], 429);
            assert_eq!(body["error"]["status"], "RESOURCE_EXHAUSTED");
        } else {
            assert_eq!(body["error"]["code"], "rate_limit_exceeded");
        }
        assert!(!body.to_string().contains("private") && !body.to_string().contains("secret"));
    }
    let response = runtime
        .handle(
            request(
                "/v1/responses",
                json!({"model":"public","input":"Hi"}),
                Some("alice-secret"),
            ),
            CancellationToken::new(),
        )
        .await;
    assert_rate(response).await;
    assert_eq!(upstream.count(), 4);
}

#[tokio::test]
async fn separate_aliases_have_independent_buckets_and_omission_disables_rate() {
    let upstream = upstream(Reply::Good).await;
    let mut config = configuration(&[&upstream], 1);
    let model = config.models["public"].clone();
    config.models.insert("other".into(), model.clone());
    let mut unlimited = model;
    unlimited.rate = None;
    config.models.insert("unlimited".into(), unlimited);
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(config, &limit, Options::default());
    consume(invoke(&runtime).await).await;
    // Keep the denied response alive while admitting a different alias.
    let denied = invoke(&runtime).await;
    assert_eq!(limit.available(), 1);
    for alias in ["other", "unlimited", "unlimited", "unlimited"] {
        let response = runtime
            .handle(
                request(
                    "/v1/chat/completions",
                    chat(alias, false),
                    Some("bob-secret"),
                ),
                CancellationToken::new(),
            )
            .await;
        assert_eq!(response.status(), StatusCode::OK);
        consume(response).await;
    }
    assert_rate(denied).await;
    assert_rate(invoke(&runtime).await).await;
    assert_eq!(upstream.count(), 5);
}

#[tokio::test]
async fn burst_refills_continuously_but_never_accumulates_above_capacity() {
    let upstream = upstream(Reply::Good).await;
    let mut config = configuration(&[&upstream], 2);
    let rate = config
        .models
        .get_mut("public")
        .unwrap()
        .rate
        .as_mut()
        .unwrap();
    rate.requests = 2;
    rate.period_ms = 1000;
    let runtime = runtime(
        config,
        &ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
    );
    for _ in 0..2 {
        assert_eq!(invoke(&runtime).await.status(), StatusCode::OK);
    }
    assert_rate(invoke(&runtime).await).await;
    tokio::time::sleep(Duration::from_millis(1200)).await;
    for _ in 0..2 {
        assert_eq!(invoke(&runtime).await.status(), StatusCode::OK);
    }
    assert_rate(invoke(&runtime).await).await;
    tokio::time::sleep(Duration::from_millis(650)).await;
    assert_eq!(invoke(&runtime).await.status(), StatusCode::OK);
    assert_rate(invoke(&runtime).await).await;
    assert_eq!(upstream.count(), 5);
}

#[tokio::test]
async fn retry_after_rounds_fractional_seconds_up() {
    let upstream = upstream(Reply::Good).await;
    let mut config = configuration(&[&upstream], 1);
    config
        .models
        .get_mut("public")
        .unwrap()
        .rate
        .as_mut()
        .unwrap()
        .period_ms = 1750;
    let runtime = runtime(
        config,
        &ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
    );
    assert_eq!(invoke(&runtime).await.status(), StatusCode::OK);
    let denied = invoke(&runtime).await;
    assert_eq!(denied.headers()["retry-after"], "2");
    assert_rate(denied).await;
    assert_eq!(upstream.count(), 1);
}

#[tokio::test]
async fn auth_invalid_requests_and_concurrency_denials_do_not_consume_rate() {
    let upstream = upstream(Reply::Good).await;
    let mut config = configuration(&[&upstream], 1);
    config
        .models
        .get_mut("public")
        .unwrap()
        .subjects
        .remove("bob");
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(config, &limit, Options::default());
    for (body, credential, status) in [
        (chat("public", false), None, 401),
        (chat("public", false), Some("wrong-secret"), 401),
        (chat("public", false), Some("bob-secret"), 403),
        (
            json!({"model":"public","messages":false}),
            Some("alice-secret"),
            400,
        ),
        (chat("missing", false), Some("alice-secret"), 404),
    ] {
        let response = runtime
            .handle(
                request("/v1/chat/completions", body, credential),
                CancellationToken::new(),
            )
            .await;
        assert_eq!(response.status().as_u16(), status);
        assert!(!response.headers().contains_key("retry-after"));
        consume(response).await;
    }
    let permit = limit.try_acquire().unwrap();
    let denied = invoke(&runtime).await;
    assert_eq!(
        consume(denied).await["error"]["code"],
        "concurrency_limit_exceeded"
    );
    drop(permit);
    let cancelled = CancellationToken::new();
    cancelled.cancel();
    let response = runtime
        .handle(
            request(
                "/v1/chat/completions",
                chat("public", false),
                Some("alice-secret"),
            ),
            cancelled,
        )
        .await;
    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
    drop(response);
    assert_eq!(upstream.count(), 0);
    assert_eq!(invoke(&runtime).await.status(), StatusCode::OK);
    assert_rate(invoke(&runtime).await).await;
    assert_eq!(upstream.count(), 1);
}

#[tokio::test]
async fn request_body_timeout_before_admission_does_not_consume_rate() {
    let upstream = upstream(Reply::Good).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(
        configuration(&[&upstream], 1),
        &limit,
        Options {
            request_timeout: Duration::from_millis(500),
            ..Options::default()
        },
    );
    let pending_body =
        Body::from_stream(futures::stream::pending::<Result<Vec<u8>, std::io::Error>>());
    let pending = Request::builder()
        .method("POST")
        .uri("/v1/chat/completions")
        .header("content-type", "application/json")
        .header("authorization", "Bearer alice-secret")
        .body(pending_body)
        .unwrap();
    let response = runtime.handle(pending, CancellationToken::new()).await;
    assert_eq!(response.status(), StatusCode::GATEWAY_TIMEOUT);
    assert!(!response.headers().contains_key("retry-after"));
    assert_eq!(limit.available(), 1);
    assert_eq!(upstream.count(), 0);
    assert_eq!(consume(response).await["error"]["code"], "request_timeout");

    let response = invoke(&runtime).await;
    assert_eq!(response.status(), StatusCode::OK);
    consume(response).await;
    assert_rate(invoke(&runtime).await).await;
    assert_eq!(limit.available(), 1);
    assert_eq!(upstream.count(), 1);
}

#[tokio::test]
async fn incompatible_backend_preparation_does_not_consume_rate() {
    let upstream = upstream(Reply::Good).await;
    let mut config = configuration(&[&upstream], 1);
    config.providers.get_mut("p0").unwrap().kind = nyro_llm::config::ProviderKind::Anthropic;
    config.models.get_mut("public").unwrap().workloads = vec![nyro_llm::Workload::Chat];
    let runtime = runtime(
        config,
        &ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
    );
    let mut incompatible = chat("public", false);
    incompatible["frequency_penalty"] = json!(1);
    let rejected = runtime
        .handle(
            request("/v1/chat/completions", incompatible, Some("alice-secret")),
            CancellationToken::new(),
        )
        .await;
    assert_eq!(rejected.status(), StatusCode::BAD_REQUEST);
    assert_eq!(
        consume(rejected).await["error"]["message"],
        "Request cannot be represented by any enabled backend"
    );
    assert_eq!(upstream.count(), 0);
    assert_eq!(invoke(&runtime).await.status(), StatusCode::OK);
    assert_rate(invoke(&runtime).await).await;
    assert_eq!(upstream.count(), 1);
}

#[tokio::test]
async fn retries_cost_one_token_and_failed_calls_are_not_refunded() {
    let first = upstream(Reply::Status(429)).await;
    let backup = upstream(Reply::Good).await;
    let config = configuration(&[&first, &backup], 2);
    let runtime = runtime(
        config,
        &ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
    );
    assert_eq!(invoke(&runtime).await.status(), StatusCode::OK);
    assert_eq!((first.count(), backup.count()), (1, 1));
    *backup.reply.lock().unwrap() = Reply::Status(502);
    let response = invoke(&runtime).await;
    assert_eq!(response.status(), StatusCode::BAD_GATEWAY);
    assert!(!response.headers().contains_key("retry-after"));
    consume(response).await;
    assert_rate(invoke(&runtime).await).await;
    assert_eq!((first.count(), backup.count()), (2, 2));
}

#[tokio::test]
async fn health_blocked_logical_calls_still_consume_one_token() {
    let upstream = upstream(Reply::Status(503)).await;
    let mut config = configuration(&[&upstream], 2);
    config.models.get_mut("public").unwrap().health = Some(nyro_llm::config::HealthConfig {
        failure_threshold: 1,
        cooldown_ms: 60000,
    });
    let runtime = runtime(
        config,
        &ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
    );
    for _ in 0..2 {
        assert_eq!(
            invoke(&runtime).await.status(),
            StatusCode::SERVICE_UNAVAILABLE
        );
    }
    assert_eq!(
        upstream.count(),
        1,
        "second admission finds every backend unhealthy"
    );
    assert_rate(invoke(&runtime).await).await;
    assert_eq!(upstream.count(), 1);
}

#[tokio::test]
async fn cancellation_and_dropped_future_after_dispatch_never_refund_rate() {
    for cancel in [false, true] {
        let upstream = upstream(Reply::Slow).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(configuration(&[&upstream], 1), &limit, Options::default());
        let token = CancellationToken::new();
        let mut pending = Box::pin(runtime.handle(
            request(
                "/v1/chat/completions",
                chat("public", false),
                Some("alice-secret"),
            ),
            token.clone(),
        ));
        tokio::select! {
            _ = &mut pending => panic!("upstream must wait for headers"),
            _ = upstream.wait_for_call() => {}
        }
        if cancel {
            token.cancel();
            assert_eq!(pending.await.status(), StatusCode::SERVICE_UNAVAILABLE);
        } else {
            drop(pending);
        }
        assert_eq!(limit.available(), 1);
        assert_rate(invoke(&runtime).await).await;
        assert_eq!(upstream.count(), 1);
    }
}

#[tokio::test]
async fn stream_drop_cancel_and_deadline_never_refund_admitted_rate() {
    for termination in ["drop", "cancel", "deadline"] {
        let upstream = upstream(Reply::HangingStream).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            configuration(&[&upstream], 1),
            &limit,
            Options {
                request_timeout: if termination == "deadline" {
                    Duration::from_millis(300)
                } else {
                    Duration::from_secs(5)
                },
                ..Options::default()
            },
        );
        let token = CancellationToken::new();
        let response = runtime
            .handle(
                request(
                    "/v1/chat/completions",
                    chat("public", true),
                    Some("alice-secret"),
                ),
                token.clone(),
            )
            .await;
        assert_eq!(response.status(), StatusCode::OK);
        assert_eq!(limit.available(), 0);
        if termination == "drop" {
            drop(response);
        } else {
            if termination == "cancel" {
                token.cancel();
            }
            assert!(to_bytes(response.into_body(), 16384).await.is_err());
        }
        assert_eq!(limit.available(), 1);
        assert_rate(invoke(&runtime).await).await;
        assert_eq!(upstream.count(), 1);
    }
}
