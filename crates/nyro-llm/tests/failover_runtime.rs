//! Failover contracts exercised with independent upstream HTTP wire fixtures.
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
    health::HealthRegistry,
    runtime::{Options, Runtime},
};
use nyro_security::{ApiKey, ApiKeys};
use serde_json::{Value, json};
use tokio_util::sync::CancellationToken;

#[test]
fn failover_configuration_is_accepted() {
    let config = serde_json::from_value::<Config>(json!({
        "providers":{"p":{"kind":"openai","base_url":"http://127.0.0.1:1/v1"}},
        "models":{"public":{"backends":[{"id":"primary","provider":"p","upstream_model":"private","priority":1}],
            "workloads":["chat"],"max_attempts":2,"health":{"failure_threshold":2,"cooldown_ms":100}}}
    }));
    assert!(config.is_ok(), "{config:?}");
}

#[derive(Clone, Copy)]
enum Reply {
    Good,
    Status(u16),
    BadJson,
    BadStream,
    TruncatedStream,
    HangingStream,
}

struct Upstream {
    base: String,
    calls: Arc<AtomicUsize>,
    requests: Arc<Mutex<Vec<Value>>>,
    reply: Arc<Mutex<(Reply, Duration)>>,
    task: tokio::task::JoinHandle<()>,
}

impl Upstream {
    fn count(&self) -> usize {
        self.calls.load(Ordering::SeqCst)
    }
    fn set(&self, reply: Reply) {
        *self.reply.lock().unwrap() = (reply, Duration::ZERO);
    }
    fn delay(&self, duration: Duration) {
        self.reply.lock().unwrap().1 = duration;
    }
    async fn wait_for(&self, count: usize) {
        tokio::time::timeout(Duration::from_secs(2), async {
            while self.count() < count {
                tokio::task::yield_now().await;
            }
        })
        .await
        .expect("upstream was not called");
    }
}

impl Drop for Upstream {
    fn drop(&mut self) {
        self.task.abort();
    }
}

// Hand-authored wire data: expected output is not produced with production codecs.
const ANSWER: &str = r#"{"id":"failover-answer","object":"chat.completion","created":1,"model":"upstream-private","choices":[{"index":0,"message":{"role":"assistant","content":"From selected backend"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}"#;
const FIRST_FRAME: &str = "data: {\"id\":\"failover-answer\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"upstream-private\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"From selected backend\"},\"finish_reason\":null}]}\n\n";
const LAST_FRAME: &str = "data: {\"id\":\"failover-answer\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"upstream-private\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\ndata: [DONE]\n\n";

async fn upstream(reply: Reply) -> Upstream {
    let calls = Arc::new(AtomicUsize::new(0));
    let requests = Arc::new(Mutex::new(Vec::new()));
    let replies = Arc::new(Mutex::new((reply, Duration::ZERO)));
    let observed = calls.clone();
    let configured = replies.clone();
    let wire = requests.clone();
    let app = Router::new().fallback(post(move |request: Request<Body>| {
        let observed = observed.clone();
        let configured = configured.clone();
        let wire = wire.clone();
        async move {
            let path = request.uri().path().to_owned();
            let input: Value =
                serde_json::from_slice(&to_bytes(request.into_body(), 16384).await.unwrap())
                    .unwrap();
            let (reply, delay) = *configured.lock().unwrap();
            wire.lock().unwrap().push(json!({"path":path,"body":input}));
            observed.fetch_add(1, Ordering::SeqCst);
            tokio::time::sleep(delay).await;
            if let Reply::Status(status) = reply {
                return Response::builder()
                    .status(status)
                    .body(Body::from("private provider failure secret"))
                    .unwrap();
            }
            if input["stream"] == true {
                let wire = match reply {
                    Reply::BadStream => "data: {broken-json}\n\n".to_owned(),
                    Reply::TruncatedStream | Reply::HangingStream => FIRST_FRAME.to_owned(),
                    _ => format!("{FIRST_FRAME}{LAST_FRAME}"),
                };
                let chunks: Vec<_> = wire
                    .into_bytes()
                    .chunks(13)
                    .map(|chunk| Ok::<_, std::io::Error>(chunk.to_vec()))
                    .collect();
                let stream = futures::stream::iter(chunks);
                let stream = if matches!(reply, Reply::HangingStream) {
                    stream.chain(futures::stream::pending()).boxed()
                } else {
                    stream.boxed()
                };
                return Response::builder()
                    .header("content-type", "text/event-stream")
                    .body(Body::from_stream(stream))
                    .unwrap();
            }
            Response::builder()
                .header("content-type", "application/json")
                .body(Body::from(if matches!(reply, Reply::BadJson) {
                    "{broken-json}"
                } else {
                    ANSWER
                }))
                .unwrap()
        }
    }));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base = format!("http://{}/v1", listener.local_addr().unwrap());
    let task = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    Upstream {
        base,
        calls,
        requests,
        reply: replies,
        task,
    }
}

fn configuration(upstreams: &[&Upstream], attempts: u32, health: Option<(u32, u64)>) -> Config {
    let providers: serde_json::Map<_, _> = upstreams
        .iter()
        .enumerate()
        .map(|(index, upstream)| {
            (
                format!("p{index}"),
                json!({"kind":"openai","base_url":upstream.base,"api_key":"provider-secret"}),
            )
        })
        .collect();
    let backends: Vec<_> = upstreams.iter().enumerate().map(|(index, _)| {
        json!({"id":format!("b{index}"),"provider":format!("p{index}"),"upstream_model":"private","priority":index})
    }).collect();
    let mut model = json!({"backends":backends,"max_attempts":attempts,"workloads":["chat"],"subjects":["alice"]});
    if let Some((threshold, cooldown)) = health {
        model["health"] = json!({"failure_threshold":threshold,"cooldown_ms":cooldown});
    }
    serde_json::from_value(json!({"providers":providers,"models":{"public":model}})).unwrap()
}

fn runtime(
    config: Config,
    limit: &ConcurrencyLimit,
    options: Options,
    health: &Arc<HealthRegistry>,
) -> Runtime {
    Runtime::with_health(
        config,
        Arc::new(
            ApiKeys::new(vec![ApiKey {
                id: "alice".into(),
                secret: "client-secret".into(),
            }])
            .unwrap(),
        ),
        limit.clone(),
        options,
        health.clone(),
    )
    .unwrap()
}

fn request(streaming: bool) -> Request<Body> {
    Request::builder().method("POST").uri("/v1/chat/completions")
        .header("content-type", "application/json").header("authorization", "Bearer client-secret")
        .body(Body::from(json!({"model":"public","messages":[{"role":"user","content":"Hello"}],"stream":streaming}).to_string())).unwrap()
}

async fn invoke(runtime: &Runtime, streaming: bool) -> Response<Body> {
    runtime
        .handle(request(streaming), CancellationToken::new())
        .await
}

async fn consume(response: Response<Body>) -> String {
    String::from_utf8(
        to_bytes(response.into_body(), 16384)
            .await
            .unwrap()
            .to_vec(),
    )
    .unwrap()
}

#[tokio::test]
async fn lower_priority_wins_before_weight_or_configuration_order() {
    let backup = upstream(Reply::Good).await;
    let primary = upstream(Reply::Good).await;
    let mut config = configuration(&[&backup, &primary], 2, None);
    let model = config.models.get_mut("public").unwrap();
    model.backends[0].priority = 99;
    model.backends[0].weight = u32::MAX;
    model.backends[1].priority = 0;
    model.backends[1].weight = 1;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(config, &limit, Options::default(), &Arc::default());
    assert!(
        consume(invoke(&runtime, false).await)
            .await
            .contains("From selected backend")
    );
    assert_eq!((primary.count(), backup.count()), (1, 0));
}

#[tokio::test]
async fn attempt_budget_includes_first_send_and_never_revisits_a_backend() {
    for attempts in [1, 2, 3, 20] {
        let first = upstream(Reply::Status(503)).await;
        let second = upstream(Reply::Status(502)).await;
        let third = upstream(Reply::Good).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            configuration(&[&first, &second, &third], attempts, None),
            &limit,
            Options::default(),
            &Arc::default(),
        );
        let response = invoke(&runtime, false).await;
        assert_eq!(
            response.status().as_u16(),
            match attempts {
                1 => 503,
                2 => 502,
                _ => 200,
            }
        );
        consume(response).await;
        assert_eq!(
            (first.count(), second.count(), third.count()),
            (1, usize::from(attempts > 1), usize::from(attempts > 2))
        );
        assert_eq!(limit.available(), 1);
    }
    let first = upstream(Reply::Status(503)).await;
    let second = upstream(Reply::Status(503)).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(
        configuration(&[&first, &second], 20, None),
        &limit,
        Options::default(),
        &Arc::default(),
    );
    assert_eq!(
        invoke(&runtime, false).await.status(),
        StatusCode::SERVICE_UNAVAILABLE
    );
    assert_eq!((first.count(), second.count()), (1, 1));
}

#[tokio::test]
async fn same_priority_peers_are_each_tried_before_lower_priority_fallback() {
    let first = upstream(Reply::Status(503)).await;
    let second = upstream(Reply::Status(502)).await;
    let fallback = upstream(Reply::Good).await;
    let mut config = configuration(&[&first, &second, &fallback], 3, None);
    config.models.get_mut("public").unwrap().backends[1].priority = 0;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(config, &limit, Options::default(), &Arc::default());
    let response = invoke(&runtime, false).await;
    assert_eq!(response.status(), StatusCode::OK);
    assert!(consume(response).await.contains("From selected backend"));
    // Successful fallback ends dispatch; both failed peers must precede it, in either order.
    assert_eq!((first.count(), second.count(), fallback.count()), (1, 1, 1));
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn only_explicit_transient_statuses_allow_failover() {
    for status in [
        429, 500, 502, 503, 504, 529, 400, 401, 403, 404, 408, 409, 422, 501, 505,
    ] {
        let first = upstream(Reply::Status(status)).await;
        let backup = upstream(Reply::Good).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            configuration(&[&first, &backup], 2, None),
            &limit,
            Options::default(),
            &Arc::default(),
        );
        let response = invoke(&runtime, false).await;
        let retry = matches!(status, 429 | 500 | 502 | 503 | 504 | 529);
        assert_eq!(response.status().is_success(), retry, "status={status}");
        let output = consume(response).await;
        assert!(!output.contains("private provider failure secret"));
        assert_eq!(
            (first.count(), backup.count()),
            (1, usize::from(retry)),
            "status={status}"
        );
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn nonretryable_statuses_do_not_open_backend_health() {
    for status in [400, 401, 403, 302] {
        let primary = upstream(Reply::Status(status)).await;
        let backup = upstream(Reply::Good).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            configuration(&[&primary, &backup], 2, Some((1, 30_000))),
            &limit,
            Options::default(),
            &Arc::default(),
        );
        let response = invoke(&runtime, false).await;
        assert!(!response.status().is_success(), "status={status}");
        consume(response).await;
        assert_eq!((primary.count(), backup.count()), (1, 0), "status={status}");
        primary.set(Reply::Good);
        let response = invoke(&runtime, false).await;
        assert_eq!(response.status(), StatusCode::OK, "status={status}");
        assert!(consume(response).await.contains("From selected backend"));
        assert_eq!((primary.count(), backup.count()), (2, 0), "status={status}");
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn connection_refusal_allows_failover_but_malformed_http_does_not() {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    for malformed in [false, true] {
        let backup = upstream(Reply::Good).await;
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let base = format!("http://{}/v1", listener.local_addr().unwrap());
        let server = if malformed {
            Some(tokio::spawn(async move {
                let (mut socket, _) = listener.accept().await.unwrap();
                let mut request = [0; 4096];
                assert!(socket.read(&mut request).await.unwrap() > 0);
                socket.write_all(b"HTTP/1.1 invalid\r\n\r\n").await.unwrap();
            }))
        } else {
            drop(listener);
            None
        };
        let mut config = configuration(&[&backup, &backup], 2, None);
        config.providers.get_mut("p0").unwrap().base_url = base;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(config, &limit, Options::default(), &Arc::default());
        let response = invoke(&runtime, false).await;
        assert_eq!(response.status().is_success(), !malformed);
        consume(response).await;
        assert_eq!(backup.count(), usize::from(!malformed));
        if let Some(server) = server {
            server.await.unwrap();
        }
    }
}

#[tokio::test]
async fn successful_headers_commit_backend_even_before_first_decoded_frame() {
    for (reply, streaming) in [
        (Reply::BadJson, false),
        (Reply::BadStream, true),
        (Reply::TruncatedStream, true),
    ] {
        let first = upstream(reply).await;
        let backup = upstream(Reply::Good).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            configuration(&[&first, &backup], 2, None),
            &limit,
            Options::default(),
            &Arc::default(),
        );
        let response = invoke(&runtime, streaming).await;
        if matches!(reply, Reply::TruncatedStream) {
            assert_eq!(response.status(), StatusCode::OK);
            let mut body = response.into_body().into_data_stream();
            let mut output = Vec::new();
            let mut failed = false;
            while let Some(chunk) = body.next().await {
                match chunk {
                    Ok(bytes) => output.extend_from_slice(&bytes),
                    Err(_) => {
                        failed = true;
                        break;
                    }
                }
            }
            assert!(failed);
            let output = String::from_utf8(output).unwrap();
            assert!(output.contains("From selected backend"));
            assert!(!output.contains("[DONE]"));
        } else {
            assert_eq!(response.status(), StatusCode::BAD_GATEWAY);
            consume(response).await;
        }
        assert_eq!((first.count(), backup.count()), (1, 0));
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn response_size_failure_never_dispatches_backup() {
    let first = upstream(Reply::Good).await;
    let backup = upstream(Reply::Good).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(
        configuration(&[&first, &backup], 2, None),
        &limit,
        Options {
            max_response_bytes: 64,
            ..Options::default()
        },
        &Arc::default(),
    );
    let response = invoke(&runtime, false).await;
    assert_eq!(response.status(), StatusCode::BAD_GATEWAY);
    consume(response).await;
    assert_eq!((first.count(), backup.count()), (1, 0));
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn one_deadline_covers_all_attempts() {
    let first = upstream(Reply::Status(503)).await;
    first.delay(Duration::from_millis(120));
    let second = upstream(Reply::Good).await;
    second.delay(Duration::from_millis(120));
    let third = upstream(Reply::Good).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(
        configuration(&[&first, &second, &third], 3, None),
        &limit,
        Options {
            request_timeout: Duration::from_millis(200),
            ..Options::default()
        },
        &Arc::default(),
    );
    let response = invoke(&runtime, false).await;
    assert_eq!(response.status(), StatusCode::GATEWAY_TIMEOUT);
    consume(response).await;
    assert_eq!((first.count(), second.count(), third.count()), (1, 1, 0));
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn cancellation_or_drop_during_retry_releases_the_single_permit() {
    for cancel in [false, true] {
        let first = upstream(Reply::Status(503)).await;
        let second = upstream(Reply::Good).await;
        second.delay(Duration::from_secs(30));
        let third = upstream(Reply::Good).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            configuration(&[&first, &second, &third], 3, None),
            &limit,
            Options::default(),
            &Arc::default(),
        );
        let token = CancellationToken::new();
        let mut pending = Box::pin(runtime.handle(request(false), token.clone()));
        tokio::select! {
            _ = &mut pending => panic!("second attempt must wait for response headers"),
            _ = second.wait_for(1) => {}
        }
        assert_eq!(limit.available(), 0);
        assert_eq!(
            invoke(&runtime, false).await.status(),
            StatusCode::TOO_MANY_REQUESTS
        );
        if cancel {
            token.cancel();
            let response = tokio::time::timeout(Duration::from_secs(1), pending)
                .await
                .unwrap();
            drop(response);
        } else {
            drop(pending);
        }
        assert_eq!(limit.available(), 1);
        tokio::task::yield_now().await;
        assert_eq!((first.count(), second.count(), third.count()), (1, 1, 0));
    }
}

#[tokio::test]
async fn open_backend_is_skipped_and_only_one_recovery_stream_can_probe() {
    let first = upstream(Reply::Status(503)).await;
    let backup = upstream(Reply::Good).await;
    let limit = ConcurrencyLimit::new(4).unwrap();
    let runtime = runtime(
        configuration(&[&first, &backup], 2, Some((2, 60))),
        &limit,
        Options::default(),
        &Arc::default(),
    );
    for _ in 0..3 {
        assert!(
            consume(invoke(&runtime, false).await)
                .await
                .contains("From selected backend")
        );
    }
    assert_eq!((first.count(), backup.count()), (2, 3));
    tokio::time::sleep(Duration::from_millis(90)).await;
    first.set(Reply::HangingStream);
    let probe = invoke(&runtime, true).await;
    assert_eq!(probe.status(), StatusCode::OK);
    assert_eq!(first.count(), 3);
    consume(invoke(&runtime, false).await).await;
    assert_eq!(
        (first.count(), backup.count()),
        (3, 4),
        "unfinished stream must not close the breaker"
    );
    drop(probe);
    // Dropping a probe is neutral and makes the recovery slot immediately available.
    first.set(Reply::Good);
    consume(invoke(&runtime, true).await).await;
    assert_eq!(first.count(), 4);
    consume(invoke(&runtime, false).await).await;
    assert_eq!((first.count(), backup.count()), (5, 4));
    assert_eq!(limit.available(), 4);
}

#[tokio::test]
async fn observed_stream_failure_opens_backend_but_dropped_stream_is_neutral() {
    for reply in [
        Reply::BadStream,
        Reply::TruncatedStream,
        Reply::HangingStream,
    ] {
        let first = upstream(reply).await;
        let backup = upstream(Reply::Good).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            configuration(&[&first, &backup], 2, Some((1, 30_000))),
            &limit,
            Options::default(),
            &Arc::default(),
        );
        let response = invoke(&runtime, true).await;
        match reply {
            Reply::HangingStream => drop(response),
            Reply::TruncatedStream => {
                assert!(to_bytes(response.into_body(), 16384).await.is_err());
            }
            _ => {
                assert_eq!(response.status(), StatusCode::BAD_GATEWAY);
                consume(response).await;
            }
        }
        assert_eq!(
            backup.count(),
            0,
            "protocol errors must not retry the current request"
        );
        first.set(Reply::Good);
        consume(invoke(&runtime, false).await).await;
        let neutral = matches!(reply, Reply::HangingStream);
        assert_eq!(
            (first.count(), backup.count()),
            (1 + usize::from(neutral), usize::from(!neutral))
        );
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn fully_consumed_stream_resets_prior_failure_count() {
    let first = upstream(Reply::Status(503)).await;
    let backup = upstream(Reply::Good).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(
        configuration(&[&first, &backup], 2, Some((2, 30_000))),
        &limit,
        Options::default(),
        &Arc::default(),
    );
    consume(invoke(&runtime, false).await).await;
    first.set(Reply::Good);
    assert!(
        consume(invoke(&runtime, true).await)
            .await
            .contains("[DONE]")
    );
    first.set(Reply::Status(503));
    consume(invoke(&runtime, false).await).await;
    first.set(Reply::Good);
    consume(invoke(&runtime, false).await).await;
    assert_eq!((first.count(), backup.count()), (4, 2));
}

#[tokio::test]
async fn open_health_survives_generation_and_routing_changes_but_binding_changes_reset() {
    for change in ["routing", "credential", "upstream", "policy"] {
        let first = upstream(Reply::Status(503)).await;
        let backup = upstream(Reply::Good).await;
        let registry = Arc::default();
        let limit = ConcurrencyLimit::new(1).unwrap();
        let config = configuration(&[&first, &backup], 2, Some((1, 30_000)));
        let old = runtime(config.clone(), &limit, Options::default(), &registry);
        consume(invoke(&old, false).await).await;
        let mut next = config.clone();
        match change {
            "routing" => {
                let model = next.models.get_mut("public").unwrap();
                model.backends[0].weight = 1;
                model.backends[1].priority = 20;
            }
            "credential" => {
                next.providers.get_mut("p0").unwrap().api_key = Some("replacement-secret".into())
            }
            "upstream" => {
                next.models.get_mut("public").unwrap().backends[0].upstream_model =
                    "replacement-model".into()
            }
            "policy" => {
                next.models
                    .get_mut("public")
                    .unwrap()
                    .health
                    .as_mut()
                    .unwrap()
                    .failure_threshold = 2
            }
            _ => unreachable!(),
        }
        let new = runtime(next, &limit, Options::default(), &registry);
        drop(old);
        first.set(Reply::Good);
        consume(invoke(&new, false).await).await;
        let reuse = change == "routing";
        assert_eq!(
            (first.count(), backup.count()),
            (1 + usize::from(!reuse), 1 + usize::from(reuse)),
            "change={change}"
        );
    }
}

#[tokio::test]
async fn all_open_backends_return_sanitized_503_without_new_attempts() {
    let first = upstream(Reply::Status(503)).await;
    let second = upstream(Reply::Status(503)).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(
        configuration(&[&first, &second], 2, Some((1, 30_000))),
        &limit,
        Options::default(),
        &Arc::default(),
    );
    consume(invoke(&runtime, false).await).await;
    let response = invoke(&runtime, false).await;
    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
    let output = consume(response).await;
    assert!(!output.contains("secret") && !output.contains("private"));
    assert_eq!((first.count(), second.count()), (1, 1));
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn retry_switches_provider_codec_and_preserves_non_openai_ingress() {
    for ingress in ["anthropic", "gemini"] {
        for streaming in [false, true] {
            for succeeds in [false, true] {
                let first = upstream(Reply::Status(503)).await;
                let backup = upstream(if succeeds {
                    Reply::Good
                } else {
                    Reply::Status(503)
                })
                .await;
                let limit = ConcurrencyLimit::new(1).unwrap();
                let mut config = configuration(&[&first, &backup], 2, None);
                config.providers.get_mut("p0").unwrap().kind =
                    nyro_llm::config::ProviderKind::Anthropic;
                let model = config.models.get_mut("public").unwrap();
                model.backends[0].upstream_model = "private-anthropic".into();
                model.backends[1].upstream_model = "private-openai".into();
                let runtime = runtime(config, &limit, Options::default(), &Arc::default());
                let (path, body) = if ingress == "anthropic" {
                    (
                        "/v1/messages",
                        json!({"model":"public","messages":[{"role":"user","content":"Hello"}],"max_tokens":16,"stream":streaming}),
                    )
                } else {
                    (
                        if streaming {
                            "/v1beta/models/public:streamGenerateContent?alt=sse"
                        } else {
                            "/v1beta/models/public:generateContent"
                        },
                        json!({"contents":[{"role":"user","parts":[{"text":"Hello"}]}],"generationConfig":{"maxOutputTokens":16}}),
                    )
                };
                let request = Request::builder()
                    .method("POST")
                    .uri(path)
                    .header("content-type", "application/json")
                    .header("authorization", "Bearer client-secret")
                    .body(Body::from(body.to_string()))
                    .unwrap();
                let response = runtime.handle(request, CancellationToken::new()).await;
                assert_eq!(response.status().as_u16(), if succeeds { 200 } else { 503 });
                let output = consume(response).await;
                if succeeds {
                    assert!(output.contains("From selected backend"));
                    assert!(!output.contains("upstream-private"));
                    if streaming {
                        assert!(!output.contains("[DONE]"));
                        if ingress == "anthropic" {
                            assert_eq!(output.matches("event: message_stop").count(), 1);
                        } else {
                            assert!(output.contains("\"finishReason\":\"STOP\""));
                        }
                    } else {
                        let value: Value = serde_json::from_str(&output).unwrap();
                        if ingress == "anthropic" {
                            assert_eq!(value["type"], "message");
                            assert_eq!(value["model"], "public");
                        } else {
                            assert!(value["candidates"].is_array());
                        }
                    }
                } else {
                    let value: Value = serde_json::from_str(&output).unwrap();
                    if ingress == "anthropic" {
                        assert_eq!(value["type"], "error");
                    } else {
                        assert_eq!(value["error"]["code"], 503);
                    }
                    assert!(!output.contains("private") && !output.contains("secret"));
                }
                assert_eq!((first.count(), backup.count()), (1, 1));
                let first_wire = first.requests.lock().unwrap();
                let backup_wire = backup.requests.lock().unwrap();
                assert_eq!(first_wire[0]["path"], "/v1/messages");
                assert_eq!(first_wire[0]["body"]["model"], "private-anthropic");
                assert_eq!(backup_wire[0]["path"], "/v1/chat/completions");
                assert_eq!(backup_wire[0]["body"]["model"], "private-openai");
                assert_eq!(limit.available(), 1);
            }
        }
    }
}

#[tokio::test]
async fn stream_cancellation_and_deadline_are_neutral_for_health() {
    for cancel in [false, true] {
        let first = upstream(Reply::HangingStream).await;
        let backup = upstream(Reply::Good).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            configuration(&[&first, &backup], 2, Some((1, 30_000))),
            &limit,
            Options {
                request_timeout: if cancel {
                    Duration::from_secs(5)
                } else {
                    Duration::from_millis(100)
                },
                ..Options::default()
            },
            &Arc::default(),
        );
        let token = CancellationToken::new();
        let response = runtime.handle(request(true), token.clone()).await;
        assert_eq!(response.status(), StatusCode::OK);
        if cancel {
            token.cancel();
        }
        assert!(to_bytes(response.into_body(), 16384).await.is_err());
        assert_eq!(limit.available(), 1);
        first.set(Reply::Good);
        assert!(
            consume(invoke(&runtime, false).await)
                .await
                .contains("From selected backend")
        );
        assert_eq!((first.count(), backup.count()), (2, 0));
    }
}
