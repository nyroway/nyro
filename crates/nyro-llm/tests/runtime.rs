use std::{
    collections::BTreeMap,
    sync::{Arc, Mutex},
    time::Duration,
};

use axum::{
    Router,
    body::{Body, to_bytes},
    http::{Request, StatusCode},
    routing::post,
};
use nyro_limit::ConcurrencyLimit;
use nyro_llm::{
    Workload, config,
    runtime::{Options, Runtime},
};
use nyro_security::{ApiKey, ApiKeys};
use serde_json::{Value, json};
use tokio_util::sync::CancellationToken;

struct Upstream {
    url: String,
    calls: Arc<Mutex<Vec<Value>>>,
    task: tokio::task::JoinHandle<()>,
}

impl Drop for Upstream {
    fn drop(&mut self) {
        self.task.abort();
    }
}

async fn upstream(mode: &'static str) -> Upstream {
    let calls = Arc::new(Mutex::new(Vec::new()));
    let observed = calls.clone();
    let app = Router::new().route("/v1/*path", post(move |request: Request<Body>| {
        let observed = observed.clone();
        async move {
            let path = request.uri().path().to_owned();
            let authorization = request.headers().get("authorization").unwrap().to_str().unwrap().to_owned();
            let body: Value = serde_json::from_slice(&to_bytes(request.into_body(), 1024 * 1024).await.unwrap()).unwrap();
            observed.lock().unwrap().push(json!({"path":path,"authorization":authorization,"body":body}));
            if mode == "slow" { tokio::time::sleep(Duration::from_secs(30)).await; }
            if mode == "error" {
                return axum::http::Response::builder().status(429).body(Body::from("upstream-secret-error")).unwrap();
            }
            if mode == "large" { return axum::http::Response::new(Body::from("x".repeat(512))); }
            if body["stream"] == true {
                let chunk = json!({"id":"chat-1","object":"chat.completion.chunk","created":1,"model":"internal-model","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]});
                let first = format!("data: {chunk}\n\n");
                if mode == "hanging_stream" {
                    let stream = futures::stream::once(async { Ok::<_, std::io::Error>(first) }).chain(futures::stream::pending());
                    return axum::http::Response::builder().header("content-type", "text/event-stream").body(Body::from_stream(stream)).unwrap();
                }
                let ending = if mode == "truncated_stream" { "" } else { "data: [DONE]\n\n" };
                return axum::http::Response::builder().header("content-type", "text/event-stream").body(Body::from(format!("{first}{ending}"))).unwrap();
            }
            let result = if path.ends_with("embeddings") {
                json!({"object":"list","model":"internal-model","data":[{"object":"embedding","index":0,"embedding":[0.25,0.5]}],"usage":{"prompt_tokens":3,"total_tokens":3}})
            } else {
                json!({"id":"chat-1","object":"chat.completion","created":1,"model":"internal-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}})
            };
            axum::http::Response::builder().header("content-type", "application/json").body(Body::from(result.to_string())).unwrap()
        }
    }));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}/v1", listener.local_addr().unwrap());
    let task = tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });
    Upstream { url, calls, task }
}

use futures::StreamExt;

fn runtime(upstream: &Upstream, limit: ConcurrencyLimit, options: Options) -> Runtime {
    let config = config::Config {
        providers: BTreeMap::from([(
            "upstream".into(),
            config::Provider {
                native_chat: false,
                kind: config::ProviderKind::Openai,
                api: None,
                base_url: upstream.url.clone(),
                api_key: Some("upstream-secret".into()),
            },
        )]),
        models: BTreeMap::from([(
            "public-model".into(),
            config::Model {
                max_attempts: 1,
                health: None,
                rate: None,
                quota: None,
                backends: vec![config::Backend {
                    id: "default".into(),
                    provider: "upstream".into(),
                    upstream_model: "internal-model".into(),
                    weight: 100,
                    priority: 0,
                }],
                workloads: vec![Workload::Chat, Workload::Embedding],
                allow_anonymous: false,
                subjects: ["alice".into()].into(),
            },
        )]),
    };
    let keys = ApiKeys::new(vec![
        ApiKey {
            id: "alice".into(),
            secret: "client-secret".into(),
        },
        ApiKey {
            id: "bob".into(),
            secret: "other-secret".into(),
        },
    ])
    .unwrap();
    Runtime::new(config, Arc::new(keys), limit, options).unwrap()
}

fn request(path: &str, body: Value, credential: Option<&str>) -> Request<Body> {
    let mut builder = Request::builder()
        .method("POST")
        .uri(path)
        .header("content-type", "application/json");
    if let Some(key) = credential {
        builder = builder.header("authorization", format!("Bearer {key}"));
    }
    builder.body(Body::from(body.to_string())).unwrap()
}

fn chat(stream: bool) -> Value {
    json!({"model":"public-model","messages":[{"role":"user","content":"Hi"}],"stream":stream})
}

#[tokio::test(flavor = "current_thread")]
async fn cancellation_before_headers_releases_admission_and_records_a_terminal_event() {
    #[derive(Clone)]
    struct Writer(Arc<Mutex<Vec<u8>>>, std::thread::ThreadId);
    impl std::io::Write for Writer {
        fn write(&mut self, bytes: &[u8]) -> std::io::Result<usize> {
            if std::thread::current().id() == self.1 {
                self.0.lock().unwrap().extend_from_slice(bytes);
            }
            Ok(bytes.len())
        }
        fn flush(&mut self) -> std::io::Result<()> {
            Ok(())
        }
    }
    let output = Arc::new(Mutex::new(Vec::new()));
    let writer = Writer(output.clone(), std::thread::current().id());
    let subscriber = tracing_subscriber::fmt()
        .with_ansi(false)
        .without_time()
        .with_writer(move || writer.clone())
        .finish();
    // Other test threads can register the shared callsite first. A global subscriber
    // keeps their registration interested; the writer isolates this test’s events.
    tracing::subscriber::set_global_default(subscriber).unwrap();
    let upstream = upstream("slow").await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(&upstream, limit.clone(), Options::default());
    let mut future = Box::pin(runtime.handle(
        request("/v1/chat/completions", chat(false), Some("client-secret")),
        CancellationToken::new(),
    ));
    tokio::select! {
        _ = &mut future => panic!("upstream should still be pending"),
        result = tokio::time::timeout(Duration::from_secs(2), async {
            while upstream.calls.lock().unwrap().is_empty() { tokio::task::yield_now().await; }
        }) => result.unwrap(),
    }
    drop(future);
    assert_eq!(limit.available(), 1);
    let output = String::from_utf8(output.lock().unwrap().clone()).unwrap();
    assert_eq!(output.matches("LLM request finished").count(), 1);
    assert!(output.contains("outcome=\"cancelled\""));
    assert_eq!(output.matches("LLM upstream attempt finished").count(), 1);
    assert!(!output.contains("client-secret"));
    assert!(!output.contains("upstream-secret"));
}

#[tokio::test]
async fn typed_chat_and_embedding_replace_alias_and_isolate_credentials() {
    let upstream = upstream("normal").await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(&upstream, limit.clone(), Options::default());
    for (path, body) in [
        ("/v1/chat/completions", chat(false)),
        (
            "/v1/embeddings",
            json!({"model":"public-model","input":["hello","world"]}),
        ),
    ] {
        let response = runtime
            .handle(
                request(path, body, Some("client-secret")),
                CancellationToken::new(),
            )
            .await;
        assert_eq!(response.status(), StatusCode::OK);
        assert_eq!(limit.available(), 0, "permit must live through the body");
        let body: Value =
            serde_json::from_slice(&to_bytes(response.into_body(), 16384).await.unwrap()).unwrap();
        assert_eq!(body["model"], "public-model");
        assert_eq!(limit.available(), 1);
    }
    let calls = upstream.calls.lock().unwrap();
    assert_eq!(calls.len(), 2);
    for call in calls.iter() {
        assert_eq!(call["authorization"], "Bearer upstream-secret");
        assert_eq!(call["body"]["model"], "internal-model");
    }
    assert_eq!(calls[1]["body"]["input"], json!(["hello", "world"]));
    assert!(calls[1]["body"].get("messages").is_none());
}

#[tokio::test]
async fn denied_requests_never_reach_upstream() {
    let upstream = upstream("normal").await;
    let runtime = runtime(
        &upstream,
        ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
    );
    for (credential, status) in [
        (None, 401),
        (Some("wrong"), 401),
        (Some("other-secret"), 403),
    ] {
        let response = runtime
            .handle(
                request("/v1/chat/completions", chat(false), credential),
                CancellationToken::new(),
            )
            .await;
        assert_eq!(response.status().as_u16(), status);
    }
    assert!(upstream.calls.lock().unwrap().is_empty());
}

#[tokio::test]
async fn streaming_body_drop_releases_shared_admission_and_cancellation_terminates_wait() {
    let upstream = upstream("hanging_stream").await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(&upstream, limit.clone(), Options::default());
    let cancellation = CancellationToken::new();
    let response = runtime
        .handle(
            request("/v1/chat/completions", chat(true), Some("client-secret")),
            cancellation.clone(),
        )
        .await;
    assert_eq!(response.status(), StatusCode::OK);
    let denied = runtime
        .handle(
            request("/v1/chat/completions", chat(false), Some("client-secret")),
            CancellationToken::new(),
        )
        .await;
    assert_eq!(denied.status(), StatusCode::TOO_MANY_REQUESTS);
    assert_eq!(upstream.calls.lock().unwrap().len(), 1);
    cancellation.cancel();
    assert!(to_bytes(response.into_body(), 16384).await.is_err());
    assert_eq!(limit.available(), 1);
    let response = runtime
        .handle(
            request("/v1/chat/completions", chat(true), Some("client-secret")),
            CancellationToken::new(),
        )
        .await;
    assert_eq!(limit.available(), 0);
    drop(response);
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn streaming_alias_and_terminal_marker_are_preserved() {
    let upstream = upstream("normal").await;
    let runtime = runtime(
        &upstream,
        ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
    );
    let response = runtime
        .handle(
            request("/v1/chat/completions", chat(true), Some("client-secret")),
            CancellationToken::new(),
        )
        .await;
    assert_eq!(response.status(), StatusCode::OK);
    let body = String::from_utf8(
        to_bytes(response.into_body(), 16384)
            .await
            .unwrap()
            .to_vec(),
    )
    .unwrap();
    assert!(body.contains("public-model"));
    assert!(!body.contains("internal-model"));
    assert_eq!(body.matches("[DONE]").count(), 1);
}

#[tokio::test]
async fn truncated_stream_is_an_error_and_does_not_retry() {
    let upstream = upstream("truncated_stream").await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(&upstream, limit.clone(), Options::default());
    let response = runtime
        .handle(
            request("/v1/chat/completions", chat(true), Some("client-secret")),
            CancellationToken::new(),
        )
        .await;
    assert!(to_bytes(response.into_body(), 16384).await.is_err());
    assert_eq!(upstream.calls.lock().unwrap().len(), 1);
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn request_deadline_and_response_limits_release_permits() {
    for (mode, status) in [("slow", 504), ("large", 502), ("error", 429)] {
        let upstream = upstream(mode).await;
        let options = Options {
            request_timeout: Duration::from_millis(100),
            max_response_bytes: 128,
            ..Options::default()
        };
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(&upstream, limit.clone(), options);
        let response = runtime
            .handle(
                request("/v1/chat/completions", chat(false), Some("client-secret")),
                CancellationToken::new(),
            )
            .await;
        assert_eq!(response.status().as_u16(), status);
        let body = to_bytes(response.into_body(), 16384).await.unwrap();
        assert!(!String::from_utf8_lossy(&body).contains("upstream-secret"));
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn request_size_limit_rejects_before_dispatch() {
    let upstream = upstream("normal").await;
    let runtime = runtime(
        &upstream,
        ConcurrencyLimit::new(1).unwrap(),
        Options {
            max_body_bytes: 16,
            ..Options::default()
        },
    );
    let response = runtime
        .handle(
            request("/v1/chat/completions", chat(false), Some("client-secret")),
            CancellationToken::new(),
        )
        .await;
    assert_eq!(response.status(), StatusCode::PAYLOAD_TOO_LARGE);
    assert!(upstream.calls.lock().unwrap().is_empty());
}
