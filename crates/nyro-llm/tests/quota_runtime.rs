//! Cumulative token accounting against independent local HTTP wire fixtures.
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
    runtime::{Options, Runtime, SharedResources},
};
use nyro_security::{ApiKey, ApiKeys};
use serde_json::{Value, json};
use std::{
    sync::{Arc, Mutex},
    time::Duration,
};
use tokio_util::sync::CancellationToken;

#[test]
fn model_quota_configuration_is_accepted() {
    let config = serde_json::from_value::<Config>(json!({
        "providers":{"p":{"kind":"openai","base_url":"http://127.0.0.1:1/v1"}},
        "models":{"public":{"provider":"p","upstream_model":"private","workloads":["chat"],
            "quota":{"total_tokens":100,"reserve_tokens":10}}}
    }));
    assert!(config.is_ok(), "{config:?}");
}

#[derive(Clone)]
enum Reply {
    Json(Value),
    Sse(String, bool),
    Status(u16),
    Slow,
}
struct Upstream {
    base: String,
    calls: Arc<Mutex<Vec<Value>>>,
    reply: Arc<Mutex<Reply>>,
    task: tokio::task::JoinHandle<()>,
}
impl Upstream {
    fn count(&self) -> usize {
        self.calls.lock().unwrap().len()
    }
    async fn wait_for_call(&self) {
        tokio::time::timeout(Duration::from_secs(3), async {
            while self.count() == 0 {
                tokio::task::yield_now().await;
            }
        })
        .await
        .expect("upstream receives admitted request");
    }
}
impl Drop for Upstream {
    fn drop(&mut self) {
        self.task.abort();
    }
}
async fn upstream(reply: Reply) -> Upstream {
    let calls = Arc::new(Mutex::new(Vec::new()));
    let replies = Arc::new(Mutex::new(reply));
    let observed = calls.clone();
    let configured = replies.clone();
    let app = Router::new().fallback(post(move |request: Request<Body>| {
        let observed = observed.clone();
        let configured = configured.clone();
        async move {
            let body: Value =
                serde_json::from_slice(&to_bytes(request.into_body(), 16384).await.unwrap())
                    .unwrap();
            observed.lock().unwrap().push(body);
            let reply = configured.lock().unwrap().clone();
            let (status, content_type, body) = match reply {
                Reply::Json(value) => (200, "application/json", Body::from(value.to_string())),
                Reply::Status(status) => (
                    status,
                    "application/json",
                    Body::from("private upstream-secret failure"),
                ),
                Reply::Slow => {
                    tokio::time::sleep(Duration::from_secs(30)).await;
                    (200, "application/json", Body::empty())
                }
                Reply::Sse(text, hang) => {
                    // Arbitrary transport chunks must not affect usage snapshots.
                    let chunks: Vec<_> = text
                        .as_bytes()
                        .chunks(17)
                        .map(|b| Ok::<_, std::io::Error>(b.to_vec()))
                        .collect();
                    let source = futures::stream::iter(chunks);
                    let source = if hang {
                        source.chain(futures::stream::pending()).boxed()
                    } else {
                        source.boxed()
                    };
                    (200, "text/event-stream", Body::from_stream(source))
                }
            };
            Response::builder()
                .status(status)
                .header("content-type", content_type)
                .body(body)
                .unwrap()
        }
    }));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base = format!("http://{}", listener.local_addr().unwrap());
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
fn native(format: &str) -> Value {
    match format {
        "openai" => {
            json!({"id":"answer","object":"chat.completion","created":1,"model":"private","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}})
        }
        "anthropic" => {
            json!({"id":"answer","type":"message","role":"assistant","model":"private","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":2}})
        }
        "gemini" => {
            json!({"responseId":"answer","modelVersion":"private","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}})
        }
        "responses" => {
            json!({"id":"answer","object":"response","created_at":1,"model":"private","status":"completed","error":null,"incomplete_details":null,"output":[{"type":"message","id":"msg","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}})
        }
        _ => panic!("unknown fixture"),
    }
}
fn chat_frames(totals: &[u64], done: bool) -> String {
    let first = json!({"id":"answer","object":"chat.completion.chunk","created":1,"model":"private","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]});
    let mut text = format!("data: {first}\n\n");
    for total in totals {
        let usage = json!({"id":"answer","object":"chat.completion.chunk","created":1,"model":"private","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":total,"total_tokens":total}});
        text.push_str(&format!("data: {usage}\n\n"));
    }
    if done {
        let finish = json!({"id":"answer","object":"chat.completion.chunk","created":1,"model":"private","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]});
        text.push_str(&format!("data: {finish}\n\ndata: [DONE]\n\n"));
    }
    text
}
fn native_frames(format: &str) -> String {
    match format {
        "openai" => chat_frames(&[3, 5, 5], true),
        "anthropic" => concat!(
            "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"answer\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"private\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n",
            "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
            "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n",
            "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
            "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":2}}\n\n",
            "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
        ).into(),
        "gemini" => format!("data: {}\n\n", native("gemini")),
        "responses" => {
            let terminal = native("responses");
            let mut start = terminal.clone();
            start["status"] = json!("in_progress"); start["output"] = json!([]); start["usage"] = Value::Null;
            let events = [
                json!({"type":"response.created","response":start}),
                json!({"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg","role":"assistant","status":"in_progress","content":[]}}),
                json!({"type":"response.content_part.added","output_index":0,"content_index":0,"item_id":"msg","part":{"type":"output_text","text":"","annotations":[]}}),
                json!({"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg","delta":"Hello"}),
                json!({"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"msg","text":"Hello"}),
                json!({"type":"response.content_part.done","output_index":0,"content_index":0,"item_id":"msg","part":terminal["output"][0]["content"][0]}),
                json!({"type":"response.output_item.done","output_index":0,"item":terminal["output"][0]}),
                json!({"type":"response.completed","response":terminal}),
            ];
            events.into_iter().enumerate().map(|(i, mut event)| { event["sequence_number"] = json!(i); format!("event: {}\ndata: {event}\n\n", event["type"].as_str().unwrap()) }).collect()
        }
        _ => panic!("unknown fixture"),
    }
}
fn configuration(upstreams: &[&Upstream], format: &str, total: u64, reserve: u64) -> Config {
    let providers: serde_json::Map<_, _> = upstreams.iter().enumerate().map(|(i, u)| {
        let mut value = json!({"kind":if format == "responses" {"openai"} else {format},"base_url":format!("{}/{}",u.base, if format == "gemini" {"v1beta"} else {"v1"}),"api_key":"upstream-secret"});
        if format == "responses" { value["api"] = json!("responses"); }
        (format!("p{i}"), value)
    }).collect();
    let backends: Vec<_> = upstreams.iter().enumerate().map(|(i, _)| json!({"id":format!("b{i}"),"provider":format!("p{i}"),"upstream_model":"private","priority":i})).collect();
    serde_json::from_value(json!({"providers":providers,"models":{"public":{"backends":backends,"max_attempts":upstreams.len(),"workloads":if format == "openai" {json!(["chat","embedding"])} else {json!(["chat"])},"subjects":["alice","bob"],"allow_anonymous":true,"quota":{"total_tokens":total,"reserve_tokens":reserve}}}})).unwrap()
}
fn runtime(
    config: Config,
    limit: &ConcurrencyLimit,
    options: Options,
    resources: &SharedResources,
) -> Runtime {
    Runtime::with_resources(
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
        resources.clone(),
    )
    .unwrap()
}
fn request(format: &str, streaming: bool) -> Request<Body> {
    let (path, body) = match format {
        "openai" => (
            "/v1/chat/completions".into(),
            json!({"model":"public","messages":[{"role":"user","content":"Hi"}],"max_tokens":16,"stream":streaming}),
        ),
        "responses" => (
            "/v1/responses".into(),
            json!({"model":"public","input":"Hi","max_output_tokens":16,"stream":streaming}),
        ),
        "anthropic" => (
            "/v1/messages".into(),
            json!({"model":"public","messages":[{"role":"user","content":"Hi"}],"max_tokens":16,"stream":streaming}),
        ),
        "embedding" => (
            "/v1/embeddings".into(),
            json!({"model":"public","input":"Hi"}),
        ),
        _ => (
            format!(
                "/v1beta/models/public:{}",
                if streaming {
                    "streamGenerateContent?alt=sse"
                } else {
                    "generateContent"
                }
            ),
            json!({"contents":[{"role":"user","parts":[{"text":"Hi"}]}],"generationConfig":{"maxOutputTokens":16}}),
        ),
    };
    Request::builder()
        .method("POST")
        .uri(path)
        .header("content-type", "application/json")
        .body(Body::from(body.to_string()))
        .unwrap()
}
async fn invoke(runtime: &Runtime) -> Response<Body> {
    runtime
        .handle(request("openai", false), CancellationToken::new())
        .await
}
fn balance(resources: &SharedResources, used: u128, reserved: u64) {
    let value = resources.quotas.snapshot("public").expect("bound quota");
    assert_eq!((value.used, value.reserved), (used, reserved));
}
async fn consume(response: Response<Body>) -> Value {
    serde_json::from_slice(&to_bytes(response.into_body(), 65536).await.unwrap()).unwrap()
}
async fn assert_quota(response: Response<Body>) {
    assert_eq!(response.status(), StatusCode::TOO_MANY_REQUESTS);
    assert!(!response.headers().contains_key("retry-after"));
    let value = consume(response).await;
    assert_eq!(value["error"]["code"], "quota_exceeded");
    assert!(!value.to_string().contains("private") && !value.to_string().contains("secret"));
}

#[tokio::test]
async fn json_settles_before_body_handoff_including_zero_missing_invalid_and_overage() {
    for (usage, status, charged) in [
        (
            json!({"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}),
            200,
            5,
        ),
        (
            json!({"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}),
            200,
            0,
        ),
        (Value::Null, 200, 10),
        (
            json!({"prompt_tokens":3,"completion_tokens":2,"total_tokens":4}),
            502,
            10,
        ),
        (
            json!({"prompt_tokens":u64::MAX,"completion_tokens":1,"total_tokens":0}),
            502,
            10,
        ),
        (
            json!({"prompt_tokens":11,"completion_tokens":12,"total_tokens":23}),
            200,
            23,
        ),
    ] {
        let mut value = native("openai");
        if usage.is_null() {
            value.as_object_mut().unwrap().remove("usage");
        } else {
            value["usage"] = usage;
        }
        let upstream = upstream(Reply::Json(value)).await;
        let resources = SharedResources::default();
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            configuration(&[&upstream], "openai", 20, 10),
            &limit,
            Options::default(),
            &resources,
        );
        let response = invoke(&runtime).await;
        assert_eq!(response.status().as_u16(), status);
        balance(&resources, charged, 0);
        drop(response);
        assert_eq!(limit.available(), 1);
        if charged > 20 {
            assert_quota(invoke(&runtime).await).await;
            assert_eq!(upstream.count(), 1);
        }
    }
}

#[tokio::test]
async fn native_json_and_streams_share_actual_usage_across_ingress_protocols_and_callers() {
    for format in ["openai", "anthropic", "gemini", "responses"] {
        let upstream = upstream(Reply::Json(native(format))).await;
        let resources = SharedResources::default();
        let runtime = runtime(
            configuration(&[&upstream], format, 100, 10),
            &ConcurrencyLimit::new(1).unwrap(),
            Options::default(),
            &resources,
        );
        for (i, ingress) in ["openai", "anthropic", "gemini", "responses"]
            .into_iter()
            .enumerate()
        {
            let mut input = request(ingress, false);
            if i % 2 == 0 {
                input
                    .headers_mut()
                    .insert("authorization", "Bearer bob-secret".parse().unwrap());
            }
            let response = runtime.handle(input, CancellationToken::new()).await;
            assert_eq!(response.status(), StatusCode::OK, "{format}/{ingress}");
            balance(&resources, 5 * (i as u128 + 1), 0);
            consume(response).await;
        }
        *upstream.reply.lock().unwrap() = Reply::Sse(native_frames(format), false);
        for (i, ingress) in ["openai", "anthropic", "gemini", "responses"]
            .into_iter()
            .enumerate()
        {
            let response = runtime
                .handle(request(ingress, true), CancellationToken::new())
                .await;
            assert_eq!(response.status(), StatusCode::OK, "{format}/{ingress}");
            let bytes = to_bytes(response.into_body(), 65536).await.unwrap();
            assert!(
                String::from_utf8_lossy(&bytes).contains("Hello"),
                "{format}/{ingress}"
            );
            balance(&resources, 25 + 5 * i as u128, 0);
        }
        assert_eq!(upstream.count(), 8);
    }
}

#[tokio::test]
async fn embedding_charges_input_tokens_and_rejects_inconsistent_usage() {
    let upstream = upstream(Reply::Json(json!({"object":"list","model":"private","data":[{"object":"embedding","index":0,"embedding":[0.25]}],"usage":{"prompt_tokens":3,"total_tokens":3}}))).await;
    let resources = SharedResources::default();
    let runtime = runtime(
        configuration(&[&upstream], "openai", 30, 10),
        &ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
        &resources,
    );
    let response = runtime
        .handle(request("embedding", false), CancellationToken::new())
        .await;
    assert_eq!(response.status(), StatusCode::OK);
    balance(&resources, 3, 0);
    drop(response);
    if let Reply::Json(value) = &mut *upstream.reply.lock().unwrap() {
        value["usage"]["total_tokens"] = json!(4);
    }
    let response = runtime
        .handle(request("embedding", false), CancellationToken::new())
        .await;
    assert_eq!(response.status(), StatusCode::BAD_GATEWAY);
    balance(&resources, 13, 0);
}

#[tokio::test]
async fn upstream_usage_is_forced_but_downstream_visibility_follows_caller() {
    let upstream = upstream(Reply::Sse(chat_frames(&[3, 5, 5], true), false)).await;
    let resources = SharedResources::default();
    let runtime = runtime(
        configuration(&[&upstream], "openai", 100, 10),
        &ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
        &resources,
    );
    for visible in [false, true] {
        let mut input = request("openai", true);
        if visible {
            let mut body: Value = serde_json::from_slice(
                &to_bytes(std::mem::replace(input.body_mut(), Body::empty()), 16384)
                    .await
                    .unwrap(),
            )
            .unwrap();
            body["stream_options"] = json!({"include_usage":true});
            *input.body_mut() = Body::from(body.to_string());
        }
        let response = runtime.handle(input, CancellationToken::new()).await;
        let bytes = to_bytes(response.into_body(), 65536).await.unwrap();
        let text = String::from_utf8(bytes.to_vec()).unwrap();
        assert!(text.contains("[DONE]"));
        assert_eq!(text.contains("\"total_tokens\""), visible);
        assert_eq!(
            upstream.calls.lock().unwrap().last().unwrap()["stream_options"]["include_usage"],
            true
        );
    }
    balance(&resources, 10, 0);
}

#[tokio::test]
async fn each_retry_reserves_again_and_quota_denial_stops_failover() {
    for total in [10, 20] {
        let first = upstream(Reply::Status(503)).await;
        let backup = upstream(Reply::Json(native("openai"))).await;
        let resources = SharedResources::default();
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            configuration(&[&first, &backup], "openai", total, 10),
            &limit,
            Options::default(),
            &resources,
        );
        let response = invoke(&runtime).await;
        if total == 10 {
            assert_eq!(limit.available(), 1);
            assert_quota(response).await;
            balance(&resources, 10, 0);
            assert_eq!(backup.count(), 0);
        } else {
            assert_eq!(response.status(), StatusCode::OK);
            balance(&resources, 15, 0);
            drop(response);
            assert_eq!(backup.count(), 1);
        }
        assert_eq!(first.count(), 1);
    }
}

#[tokio::test]
async fn known_connect_failure_refunds_reservation_for_backup() {
    let first = upstream(Reply::Json(native("openai"))).await;
    let backup = upstream(Reply::Json(native("openai"))).await;
    // Hold an unlistened local socket address only until configuration is built.
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let mut config = configuration(&[&first, &backup], "openai", 10, 10);
    config.providers.get_mut("p0").unwrap().base_url =
        format!("http://{}/v1", listener.local_addr().unwrap());
    drop(listener);
    let resources = SharedResources::default();
    let runtime = runtime(
        config,
        &ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
        &resources,
    );
    let response = invoke(&runtime).await;
    assert_eq!(response.status(), StatusCode::OK);
    balance(&resources, 5, 0);
    assert_eq!((first.count(), backup.count()), (0, 1));
}

#[tokio::test]
async fn concurrent_pending_streams_hold_reservations_and_denial_releases_permit() {
    let upstream = upstream(Reply::Sse(chat_frames(&[], false), true)).await;
    let resources = SharedResources::default();
    let limit = ConcurrencyLimit::new(3).unwrap();
    let runtime = runtime(
        configuration(&[&upstream], "openai", 20, 10),
        &limit,
        Options::default(),
        &resources,
    );
    let (a, b) = tokio::join!(
        runtime.handle(request("openai", true), CancellationToken::new()),
        runtime.handle(request("openai", true), CancellationToken::new())
    );
    assert_eq!(a.status(), StatusCode::OK);
    assert_eq!(b.status(), StatusCode::OK);
    balance(&resources, 0, 20);
    let denied = invoke(&runtime).await;
    assert_eq!(limit.available(), 1);
    balance(&resources, 0, 20);
    assert_quota(denied).await;
    drop(a);
    balance(&resources, 10, 10);
    drop(b);
    balance(&resources, 20, 0);
    assert_eq!(limit.available(), 3);
    assert_eq!(upstream.count(), 2);
}

#[tokio::test]
async fn stream_truncation_drop_cancel_and_timeout_charge_highest_observed_or_reservation() {
    for observed in [3, 23] {
        for ending in ["truncate", "drop", "cancel", "timeout"] {
            let upstream = upstream(Reply::Sse(
                chat_frames(&[observed], false),
                ending != "truncate",
            ))
            .await;
            let resources = SharedResources::default();
            let limit = ConcurrencyLimit::new(1).unwrap();
            let runtime = runtime(
                configuration(&[&upstream], "openai", 20, 10),
                &limit,
                Options {
                    request_timeout: Duration::from_millis(if ending == "timeout" {
                        300
                    } else {
                        5000
                    }),
                    ..Options::default()
                },
                &resources,
            );
            let token = CancellationToken::new();
            let mut input = request("openai", true);
            let mut value: Value = serde_json::from_slice(
                &to_bytes(std::mem::replace(input.body_mut(), Body::empty()), 16384)
                    .await
                    .unwrap(),
            )
            .unwrap();
            value["stream_options"] = json!({"include_usage":true});
            *input.body_mut() = Body::from(value.to_string());
            let response = runtime.handle(input, token.clone()).await;
            assert_eq!(response.status(), StatusCode::OK);
            let mut stream = response.into_body().into_data_stream();
            let mut text = String::new();
            while !text.contains("\"total_tokens\"") {
                text.push_str(&String::from_utf8_lossy(
                    &stream.next().await.unwrap().unwrap(),
                ));
            }
            balance(&resources, 0, 10);
            if ending == "drop" {
                drop(stream);
            } else {
                if ending == "cancel" {
                    token.cancel();
                }
                let mut failed = false;
                while let Some(chunk) = stream.next().await {
                    if chunk.is_err() {
                        failed = true;
                        break;
                    }
                }
                assert!(failed, "{ending}/{observed}");
                drop(stream);
            }
            balance(&resources, u128::from(observed.max(10)), 0);
            assert_eq!(limit.available(), 1);
            if observed > 20 {
                assert_quota(invoke(&runtime).await).await;
                assert_eq!(upstream.count(), 1);
            }
        }
    }
}

#[tokio::test]
async fn decreasing_stream_usage_is_invalid_and_preserves_highest_observation() {
    let upstream = upstream(Reply::Sse(chat_frames(&[23, 5], true), false)).await;
    let resources = SharedResources::default();
    let runtime = runtime(
        configuration(&[&upstream], "openai", 20, 10),
        &ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
        &resources,
    );
    let response = runtime
        .handle(request("openai", true), CancellationToken::new())
        .await;
    assert!(to_bytes(response.into_body(), 65536).await.is_err());
    balance(&resources, 23, 0);
    assert_quota(invoke(&runtime).await).await;
}

#[tokio::test]
async fn cancellation_or_future_drop_after_dispatch_charges_reservation() {
    for cancel in [false, true] {
        let upstream = upstream(Reply::Slow).await;
        let resources = SharedResources::default();
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            configuration(&[&upstream], "openai", 10, 10),
            &limit,
            Options::default(),
            &resources,
        );
        let token = CancellationToken::new();
        let mut future = Box::pin(runtime.handle(request("openai", false), token.clone()));
        tokio::select! { _ = &mut future => panic!("provider waits"), _ = upstream.wait_for_call() => {} }
        balance(&resources, 0, 10);
        if cancel {
            token.cancel();
            assert_eq!(future.await.status(), StatusCode::SERVICE_UNAVAILABLE);
        } else {
            drop(future);
        }
        balance(&resources, 10, 0);
        assert_eq!(limit.available(), 1);
        assert_quota(invoke(&runtime).await).await;
    }
}

#[tokio::test]
async fn no_network_admission_and_unhealthy_backends_do_not_charge_quota() {
    let upstream = upstream(Reply::Status(503)).await;
    let resources = SharedResources::default();
    let limit = ConcurrencyLimit::new(1).unwrap();
    let mut config = configuration(&[&upstream], "openai", 30, 10);
    config.models.get_mut("public").unwrap().health = Some(nyro_llm::config::HealthConfig {
        failure_threshold: 1,
        cooldown_ms: 60000,
    });
    config.models.get_mut("public").unwrap().allow_anonymous = false;
    let runtime = runtime(config, &limit, Options::default(), &resources);
    let denied = invoke(&runtime).await;
    assert_eq!(denied.status(), StatusCode::UNAUTHORIZED);
    drop(denied);
    let mut input = request("openai", false);
    input
        .headers_mut()
        .insert("authorization", "Bearer alice-secret".parse().unwrap());
    let permit = limit.try_acquire().unwrap();
    let response = runtime.handle(input, CancellationToken::new()).await;
    assert_eq!(response.status(), StatusCode::TOO_MANY_REQUESTS);
    drop(response);
    drop(permit);
    balance(&resources, 0, 0);
    assert_eq!(upstream.count(), 0);
    for _ in 0..2 {
        let mut input = request("openai", false);
        input
            .headers_mut()
            .insert("authorization", "Bearer alice-secret".parse().unwrap());
        let response = runtime.handle(input, CancellationToken::new()).await;
        assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
        drop(response);
        balance(&resources, 10, 0);
    }
    assert_eq!(upstream.count(), 1);
}

#[tokio::test]
async fn shared_generations_and_removal_preserve_consumed_balance() {
    let upstream = upstream(Reply::Json(native("openai"))).await;
    let resources = SharedResources::default();
    let limit = ConcurrencyLimit::new(2).unwrap();
    let config = configuration(&[&upstream], "openai", 20, 10);
    let old = runtime(config.clone(), &limit, Options::default(), &resources);
    let next = runtime(config.clone(), &limit, Options::default(), &resources);
    drop(invoke(&old).await);
    drop(invoke(&next).await);
    balance(&resources, 10, 0);
    drop(old);
    drop(next);
    let mut absent = config.clone();
    let mut other = absent.models.remove("public").unwrap();
    other.quota = None;
    absent.models.insert("other".into(), other);
    drop(runtime(absent, &limit, Options::default(), &resources));
    balance(&resources, 10, 0);
    let restored = runtime(config, &limit, Options::default(), &resources);
    drop(invoke(&restored).await);
    balance(&resources, 15, 0);
    assert_quota(invoke(&restored).await).await;
    assert_eq!(upstream.count(), 3);
}

#[tokio::test]
async fn completed_stream_settles_zero_or_overage_before_terminal_delivery() {
    for actual in [0, 23] {
        let upstream = upstream(Reply::Sse(chat_frames(&[actual], true), false)).await;
        let resources = SharedResources::default();
        let runtime = runtime(
            configuration(&[&upstream], "openai", 20, 10),
            &ConcurrencyLimit::new(1).unwrap(),
            Options::default(),
            &resources,
        );
        let response = runtime
            .handle(request("openai", true), CancellationToken::new())
            .await;
        assert_eq!(response.status(), StatusCode::OK);
        let mut stream = response.into_body().into_data_stream();
        loop {
            let chunk = stream.next().await.expect("terminal must arrive").unwrap();
            if String::from_utf8_lossy(&chunk).contains("[DONE]") {
                break;
            }
        }
        balance(&resources, u128::from(actual), 0);
        drop(stream);
        balance(&resources, u128::from(actual), 0);
        if actual > 20 {
            assert_quota(invoke(&runtime).await).await;
        }
    }
}

#[tokio::test]
async fn rate_admission_precedes_quota_and_protocol_errors_preserve_native_shape() {
    let upstream = upstream(Reply::Json(native("openai"))).await;
    let resources = SharedResources::default();
    let limit = ConcurrencyLimit::new(1).unwrap();
    let mut config = configuration(&[&upstream], "openai", 10, 10);
    config.models.get_mut("public").unwrap().rate = Some(nyro_llm::config::RateConfig {
        requests: 1,
        period_ms: 60000,
        burst: 2,
    });
    let limited = runtime(config, &limit, Options::default(), &resources);
    drop(invoke(&limited).await);
    balance(&resources, 5, 0);
    let denied = invoke(&limited).await;
    assert_eq!(limit.available(), 1);
    assert_quota(denied).await;
    let denied = invoke(&limited).await;
    assert_eq!(denied.status(), StatusCode::TOO_MANY_REQUESTS);
    assert!(denied.headers().contains_key("retry-after"));
    assert_eq!(
        consume(denied).await["error"]["code"],
        "rate_limit_exceeded"
    );
    balance(&resources, 5, 0);
    assert_eq!(upstream.count(), 1);

    let resources = SharedResources::default();
    let runtime = runtime(
        configuration(&[&upstream], "openai", 10, 10),
        &limit,
        Options::default(),
        &resources,
    );
    drop(invoke(&runtime).await);
    for format in ["openai", "responses", "anthropic", "gemini"] {
        let denied = runtime
            .handle(request(format, false), CancellationToken::new())
            .await;
        assert_eq!(denied.status(), StatusCode::TOO_MANY_REQUESTS);
        assert!(!denied.headers().contains_key("retry-after"));
        assert_eq!(limit.available(), 1);
        let body = consume(denied).await;
        match format {
            "anthropic" => {
                assert_eq!(body["type"], "error");
                assert_eq!(body["error"]["type"], "rate_limit_error");
            }
            "gemini" => {
                assert_eq!(body["error"]["code"], 429);
                assert_eq!(body["error"]["status"], "RESOURCE_EXHAUSTED");
            }
            _ => assert_eq!(body["error"]["code"], "quota_exceeded"),
        }
    }
    balance(&resources, 5, 0);
    assert_eq!(upstream.count(), 2);
}

#[tokio::test]
async fn invalid_requests_pre_dispatch_cancellation_and_body_timeout_leave_quota_empty() {
    let upstream = upstream(Reply::Json(native("openai"))).await;
    let resources = SharedResources::default();
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(
        configuration(&[&upstream], "openai", 10, 10),
        &limit,
        Options {
            request_timeout: Duration::from_millis(300),
            ..Options::default()
        },
        &resources,
    );
    let mut invalid = request("openai", false);
    *invalid.body_mut() = Body::from(r#"{"model":"public","messages":false}"#);
    assert_eq!(
        runtime
            .handle(invalid, CancellationToken::new())
            .await
            .status(),
        StatusCode::BAD_REQUEST
    );
    let cancelled = CancellationToken::new();
    cancelled.cancel();
    assert_eq!(
        runtime
            .handle(request("openai", false), cancelled)
            .await
            .status(),
        StatusCode::SERVICE_UNAVAILABLE
    );
    let mut pending = request("openai", false);
    *pending.body_mut() =
        Body::from_stream(futures::stream::pending::<Result<Vec<u8>, std::io::Error>>());
    assert_eq!(
        runtime
            .handle(pending, CancellationToken::new())
            .await
            .status(),
        StatusCode::GATEWAY_TIMEOUT
    );
    balance(&resources, 0, 0);
    assert_eq!(upstream.count(), 0);
    assert_eq!(limit.available(), 1);
    assert_eq!(invoke(&runtime).await.status(), StatusCode::OK);
    balance(&resources, 5, 0);
}

#[tokio::test]
async fn aliases_have_independent_quotas_and_omission_disables_accounting() {
    let upstream = upstream(Reply::Json(native("openai"))).await;
    let resources = SharedResources::default();
    let mut config = configuration(&[&upstream], "openai", 10, 10);
    config
        .models
        .insert("other".into(), config.models["public"].clone());
    let mut unlimited = config.models["public"].clone();
    unlimited.quota = None;
    config.models.insert("unlimited".into(), unlimited);
    let runtime = runtime(
        config,
        &ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
        &resources,
    );
    drop(invoke(&runtime).await);
    assert_quota(invoke(&runtime).await).await;
    for alias in ["other", "unlimited", "unlimited"] {
        let mut input = request("openai", false);
        *input.body_mut() = Body::from(
            json!({"model":alias,"messages":[{"role":"user","content":"Hi"}]}).to_string(),
        );
        assert_eq!(
            runtime
                .handle(input, CancellationToken::new())
                .await
                .status(),
            StatusCode::OK
        );
    }
    balance(&resources, 5, 0);
    assert_eq!(resources.quotas.snapshot("other").unwrap().used, 5);
    assert!(resources.quotas.snapshot("unlimited").is_none());
    assert_eq!(upstream.count(), 4);
}
