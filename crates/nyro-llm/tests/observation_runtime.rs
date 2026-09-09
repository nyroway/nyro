//! Request/attempt observations checked through HTTP and tracing boundaries.
use axum::{
    Router,
    body::{Body, to_bytes},
    http::{Request, Response},
    routing::post,
};
use futures::StreamExt;
use nyro_limit::ConcurrencyLimit;
use nyro_llm::{
    config::Config,
    runtime::{Options, Runtime, SharedResources},
};
use nyro_security::ApiKeys;
use serde_json::{Value, json};
use std::{
    collections::BTreeMap,
    sync::{Arc, Mutex},
    time::Duration,
};
use tokio_util::sync::CancellationToken;
use tracing::{
    Event, Subscriber,
    field::{Field, Visit},
};
use tracing_subscriber::{Layer, layer::Context, prelude::*};

#[derive(Clone, Debug)]
struct Observed {
    target: String,
    fields: BTreeMap<String, String>,
}
#[derive(Clone, Default)]
struct Events(Arc<Mutex<Vec<Observed>>>);
impl Visit for Observed {
    fn record_debug(&mut self, field: &Field, value: &dyn std::fmt::Debug) {
        self.fields
            .insert(field.name().into(), format!("{value:?}"));
    }
    fn record_str(&mut self, field: &Field, value: &str) {
        self.fields.insert(field.name().into(), value.into());
    }
}
impl<S: Subscriber> Layer<S> for Events {
    fn on_event(&self, event: &Event<'_>, _: Context<'_, S>) {
        if matches!(event.metadata().target(), "nyro::request" | "nyro::attempt") {
            assert_eq!(*event.metadata().level(), tracing::Level::INFO);
            let mut observed = Observed {
                target: event.metadata().target().into(),
                fields: BTreeMap::new(),
            };
            event.record(&mut observed);
            self.0.lock().unwrap().push(observed);
        }
    }
}
impl Events {
    fn target(&self, target: &str) -> Vec<Observed> {
        self.0
            .lock()
            .unwrap()
            .iter()
            .filter(|event| event.target == target)
            .cloned()
            .collect()
    }
    fn request(&self) -> Observed {
        let requests = self.target("nyro::request");
        assert_eq!(
            requests.len(),
            1,
            "exactly one request summary: {requests:?}"
        );
        requests.into_iter().next().unwrap()
    }
}
impl Observed {
    fn get(&self, field: &str) -> &str {
        self.fields
            .get(field)
            .unwrap_or_else(|| panic!("missing {field}: {self:?}"))
    }
    fn number(&self, field: &str) -> u128 {
        self.get(field).parse().unwrap()
    }
}

#[derive(Clone)]
enum Reply {
    Json(Value),
    Status(u16),
    Sse(String, bool),
    Slow,
}
struct Upstream {
    base: String,
    calls: Arc<Mutex<Vec<Value>>>,
    task: tokio::task::JoinHandle<()>,
}
impl Drop for Upstream {
    fn drop(&mut self) {
        self.task.abort();
    }
}
impl Upstream {
    async fn called(&self) {
        tokio::time::timeout(Duration::from_secs(3), async {
            while self.calls.lock().unwrap().is_empty() {
                tokio::task::yield_now().await;
            }
        })
        .await
        .expect("upstream received request");
    }
}
async fn upstream(reply: Reply) -> Upstream {
    let calls = Arc::new(Mutex::new(Vec::new()));
    let observed = calls.clone();
    let app = Router::new().fallback(post(move |request: Request<Body>| {
        let observed = observed.clone();
        let reply = reply.clone();
        async move {
            let bytes = to_bytes(request.into_body(), 16384).await.unwrap();
            observed
                .lock()
                .unwrap()
                .push(serde_json::from_slice(&bytes).unwrap());
            let (status, content_type, body) = match reply {
                Reply::Json(value) => (200, "application/json", Body::from(value.to_string())),
                Reply::Status(status) => (
                    status,
                    "application/json",
                    Body::from("upstream-secret private response"),
                ),
                Reply::Slow => {
                    tokio::time::sleep(Duration::from_secs(30)).await;
                    (200, "application/json", Body::empty())
                }
                Reply::Sse(text, hang) => {
                    let source = futures::stream::iter(
                        text.as_bytes()
                            .chunks(17)
                            .map(|chunk| Ok::<_, std::io::Error>(chunk.to_vec()))
                            .collect::<Vec<_>>(),
                    );
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
    Upstream { base, calls, task }
}
fn answer(usage: Value) -> Value {
    let mut value = json!({"id":"answer-private", "object":"chat.completion", "created":1, "model":"private-model", "choices":[{"index":0,"message":{"role":"assistant","content":"response-secret"},"finish_reason":"stop"}]});
    if !usage.is_null() {
        value["usage"] = usage;
    }
    value
}
fn usage() -> Value {
    json!({"prompt_tokens":3,"completion_tokens":2,"total_tokens":5})
}
fn config(upstreams: &[&Upstream], quota: Option<u64>) -> Config {
    let providers: serde_json::Map<_, _> = upstreams.iter().enumerate().map(|(i, upstream)| (format!("p{i}"), json!({"kind":"openai","base_url":format!("{}/v1",upstream.base),"api_key":"upstream-secret"}))).collect();
    let backends: Vec<_> = upstreams.iter().enumerate().map(|(i, _)| json!({"id":format!("b{i}"),"provider":format!("p{i}"),"upstream_model":"private-model","priority":i})).collect();
    let mut value = json!({"providers":providers,"models":{"public":{"backends":backends,"max_attempts":upstreams.len(),"workloads":["chat","embedding"],"allow_anonymous":true}}});
    if let Some(total) = quota {
        value["models"]["public"]["quota"] = json!({"total_tokens":total,"reserve_tokens":10});
    }
    serde_json::from_value(value).unwrap()
}
fn runtime(config: Config, options: Options) -> Runtime {
    Runtime::with_resources(
        config,
        Arc::new(ApiKeys::new(vec![]).unwrap()),
        ConcurrencyLimit::new(2).unwrap(),
        options,
        SharedResources::default(),
    )
    .unwrap()
}
fn request(streaming: bool) -> Request<Body> {
    Request::builder().method("POST").uri("/v1/chat/completions").header("content-type", "application/json").header("x-request-id", "client-secret-id").body(Body::from(json!({"model":"public","messages":[{"role":"user","content":"prompt-secret"}],"stream":streaming}).to_string())).unwrap()
}
async fn consume(response: Response<Body>) -> String {
    String::from_utf8(
        to_bytes(response.into_body(), 65536)
            .await
            .unwrap()
            .to_vec(),
    )
    .unwrap()
}

#[tokio::test]
async fn json_emits_correlated_attempt_and_summary_only_after_delivery() {
    let events = Events::default();
    let _guard =
        tracing::subscriber::set_default(tracing_subscriber::registry().with(events.clone()));
    let upstream = upstream(Reply::Json(answer(usage()))).await;
    let runtime = runtime(config(&[&upstream], None), Options::default());
    let response = runtime
        .handle(request(false), CancellationToken::new())
        .await;
    assert_eq!(response.status(), 200);
    assert!(events.target("nyro::request").is_empty());
    let attempts = events.target("nyro::attempt");
    assert_eq!(
        attempts.len(),
        1,
        "attempt is finalized before body handoff"
    );
    let id = response
        .headers()
        .get("x-request-id")
        .expect("generated response ID")
        .to_str()
        .unwrap()
        .to_owned();
    assert_eq!(id.len(), 32);
    assert!(
        id.bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
    );
    consume(response).await;
    let summary = events.request();
    assert_eq!(summary.get("message"), "LLM request finished");
    assert_eq!(summary.get("request_id"), id);
    assert_eq!(summary.get("model"), "public");
    assert_eq!(summary.get("backend"), "b0");
    assert_eq!(summary.get("protocol"), "openai_chat");
    assert_eq!(summary.get("workload"), "chat");
    assert_eq!(summary.get("streaming"), "false");
    assert_eq!(summary.get("outcome"), "complete");
    assert_eq!(summary.get("delivery_outcome"), "complete");
    assert_eq!(summary.get("error_code"), "");
    assert_eq!(summary.get("usage_state"), "complete");
    assert_eq!(summary.number("attempts"), 1);
    assert_eq!(summary.number("status"), 200);
    assert_eq!(summary.number("input_tokens"), 3);
    assert_eq!(summary.number("output_tokens"), 2);
    assert_eq!(summary.number("total_tokens"), 5);
    assert_eq!(summary.number("quota_charged_tokens"), 0);
    summary.number("duration_ms");
    let attempt = &attempts[0];
    assert_eq!(attempt.get("message"), "LLM upstream attempt finished");
    assert_eq!(attempt.get("request_id"), id);
    assert_eq!(attempt.get("model"), "public");
    assert_eq!(attempt.get("provider"), "p0");
    assert_eq!(attempt.get("backend"), "b0");
    assert_eq!(attempt.get("protocol"), "openai_chat");
    assert_eq!(attempt.number("attempt"), 1);
    assert_eq!(attempt.number("upstream_status"), 200);
    assert_eq!(attempt.get("outcome"), "complete");
    assert_eq!(attempt.get("quota_outcome"), "disabled");
    assert!(!attempt.fields.contains_key("quota_charged_tokens"));
    assert_eq!(attempt.number("total_tokens"), 5);
    attempt.number("duration_ms");
    let serialized = format!("{:?}", events.0.lock().unwrap());
    for secret in [
        "private-model",
        "answer-private",
        "upstream-secret",
        "response-secret",
        "prompt-secret",
        "client-secret-id",
        &upstream.base,
    ] {
        assert!(!serialized.contains(secret), "leaked {secret}");
    }
}

fn frames(done: bool) -> String {
    let delta = json!({"id":"answer-private","object":"chat.completion.chunk","created":1,"model":"private-model","choices":[{"index":0,"delta":{"role":"assistant","content":"response-secret"},"finish_reason":null}]});
    let mut text = format!("data: {delta}\n\n");
    for total in [3, 5, 5] {
        let snapshot = json!({"id":"answer-private","object":"chat.completion.chunk","created":1,"model":"private-model","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":total,"total_tokens":total}});
        text.push_str(&format!("data: {snapshot}\n\n"));
    }
    if done {
        let finish = json!({"id":"answer-private","object":"chat.completion.chunk","created":1,"model":"private-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]});
        text.push_str(&format!("data: {finish}\n\ndata: [DONE]\n\n"));
    }
    text
}

#[tokio::test]
async fn streams_force_usage_without_quota_and_preserve_client_visibility() {
    for visible in [false, true] {
        let events = Events::default();
        let _guard =
            tracing::subscriber::set_default(tracing_subscriber::registry().with(events.clone()));
        // Protocol completion must finalize even when the transport remains open.
        let upstream = upstream(Reply::Sse(frames(true), true)).await;
        let runtime = runtime(config(&[&upstream], None), Options::default());
        let mut input = request(true);
        if visible {
            let bytes = to_bytes(std::mem::replace(input.body_mut(), Body::empty()), 16384)
                .await
                .unwrap();
            let mut body: Value = serde_json::from_slice(&bytes).unwrap();
            body["stream_options"] = json!({"include_usage":true});
            *input.body_mut() = Body::from(body.to_string());
        }
        let response = runtime.handle(input, CancellationToken::new()).await;
        assert!(events.target("nyro::request").is_empty());
        let text = tokio::time::timeout(Duration::from_secs(3), consume(response))
            .await
            .unwrap();
        assert!(text.contains("[DONE]"));
        assert_eq!(text.contains("\"total_tokens\""), visible);
        assert_eq!(
            upstream.calls.lock().unwrap()[0]["stream_options"]["include_usage"],
            true
        );
        let summary = events.request();
        assert_eq!(summary.get("streaming"), "true");
        assert_eq!(summary.get("outcome"), "complete");
        assert_eq!(summary.get("usage_state"), "complete");
        assert_eq!(
            summary.number("total_tokens"),
            5,
            "last snapshot, not sum of snapshots"
        );
        let attempts = events.target("nyro::attempt");
        assert_eq!(attempts.len(), 1);
        assert_eq!(attempts[0].get("outcome"), "complete");
        assert_eq!(attempts[0].number("total_tokens"), 5);
    }
}

#[tokio::test]
async fn zero_missing_invalid_usage_are_distinct_with_and_without_quota() {
    for quota in [None, Some(100)] {
        for (usage, state, status, charged) in [
            (
                json!({"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}),
                "complete",
                200,
                0,
            ),
            (Value::Null, "missing", 200, 10),
            (
                json!({"prompt_tokens":3,"completion_tokens":2,"total_tokens":4}),
                "invalid",
                if quota.is_some() { 502 } else { 200 },
                10,
            ),
        ] {
            let events = Events::default();
            let _guard = tracing::subscriber::set_default(
                tracing_subscriber::registry().with(events.clone()),
            );
            let upstream = upstream(Reply::Json(answer(usage))).await;
            let runtime = runtime(config(&[&upstream], quota), Options::default());
            let response = runtime
                .handle(request(false), CancellationToken::new())
                .await;
            assert_eq!(response.status(), status);
            consume(response).await;
            let summary = events.request();
            assert_eq!(summary.get("usage_state"), state);
            assert_eq!(
                summary.number("quota_charged_tokens"),
                if quota.is_some() { charged } else { 0 }
            );
            let attempts = events.target("nyro::attempt");
            assert_eq!(attempts.len(), 1);
            assert_eq!(attempts[0].get("usage_state"), state);
            if state == "complete" {
                assert_eq!(attempts[0].number("total_tokens"), 0);
            }
            if state == "missing" {
                assert!(!attempts[0].fields.contains_key("total_tokens"));
            }
            assert_eq!(
                attempts[0].get("quota_outcome"),
                if quota.is_none() {
                    "disabled"
                } else if state == "complete" {
                    "actual"
                } else {
                    "fallback"
                }
            );
        }
    }
}

#[tokio::test]
async fn retry_summary_correlates_attempts_and_separates_observed_from_charged_tokens() {
    let events = Events::default();
    let _guard =
        tracing::subscriber::set_default(tracing_subscriber::registry().with(events.clone()));
    let first = upstream(Reply::Status(503)).await;
    let second = upstream(Reply::Json(answer(usage()))).await;
    let runtime = runtime(config(&[&first, &second], Some(100)), Options::default());
    let response = runtime
        .handle(request(false), CancellationToken::new())
        .await;
    assert_eq!(response.status(), 200);
    consume(response).await;
    let summary = events.request();
    assert_eq!(summary.number("attempts"), 2);
    assert_eq!(summary.get("backend"), "b1");
    assert_eq!(summary.get("outcome"), "complete");
    assert_eq!(summary.get("usage_state"), "partial");
    assert_eq!(summary.number("total_tokens"), 5);
    assert_eq!(summary.number("quota_charged_tokens"), 15);
    let attempts = events.target("nyro::attempt");
    assert_eq!(attempts.len(), 2);
    for (index, attempt) in attempts.iter().enumerate() {
        assert_eq!(attempt.number("attempt"), index as u128 + 1);
        assert_eq!(attempt.get("request_id"), summary.get("request_id"));
        assert_eq!(attempt.get("backend"), format!("b{index}"));
    }
    assert_eq!(attempts[0].get("outcome"), "http_error");
    assert_eq!(attempts[0].number("upstream_status"), 503);
    assert_eq!(attempts[0].get("quota_outcome"), "fallback");
    assert_eq!(attempts[0].number("quota_charged_tokens"), 10);
    assert_eq!(attempts[1].get("quota_outcome"), "actual");
    assert_eq!(attempts[1].number("quota_charged_tokens"), 5);
}

#[tokio::test]
async fn local_rejections_have_no_attempt_and_do_not_log_untrusted_model_or_credentials() {
    for rejection in ["authentication", "unknown_model", "quota"] {
        let events = Events::default();
        let _guard =
            tracing::subscriber::set_default(tracing_subscriber::registry().with(events.clone()));
        let upstream = upstream(Reply::Json(answer(usage()))).await;
        let mut configuration = serde_json::to_value(config(
            &[&upstream],
            if rejection == "quota" { Some(10) } else { None },
        ))
        .unwrap();
        if rejection == "authentication" {
            configuration["models"]["public"]["allow_anonymous"] = json!(false);
        }
        let runtime = runtime(
            serde_json::from_value(configuration).unwrap(),
            Options::default(),
        );
        if rejection == "quota" {
            let response = runtime
                .handle(request(false), CancellationToken::new())
                .await;
            assert_eq!(response.status(), 200);
            consume(response).await;
            events.0.lock().unwrap().clear();
            upstream.calls.lock().unwrap().clear();
        }
        let mut input = request(false);
        if rejection == "unknown_model" {
            *input.body_mut() = Body::from(json!({"model":"untrusted-model-secret","messages":[{"role":"user","content":"prompt-secret"}]}).to_string());
        }
        if rejection == "authentication" {
            input
                .headers_mut()
                .insert("authorization", "Bearer credential-secret".parse().unwrap());
        }
        let response = runtime.handle(input, CancellationToken::new()).await;
        assert!(response.status().is_client_error());
        assert!(response.headers().contains_key("x-request-id"));
        consume(response).await;
        let summary = events.request();
        assert_eq!(summary.number("attempts"), 0);
        assert_eq!(summary.get("usage_state"), "not_attempted");
        assert_eq!(summary.get("outcome"), "error");
        assert_eq!(summary.get("delivery_outcome"), "complete");
        assert!(!summary.get("error_code").is_empty());
        if rejection == "unknown_model" {
            assert_eq!(summary.get("model"), "");
        }
        assert!(events.target("nyro::attempt").is_empty());
        assert!(upstream.calls.lock().unwrap().is_empty());
        let serialized = format!("{:?}", events.0.lock().unwrap());
        for secret in [
            "untrusted-model-secret",
            "credential-secret",
            "prompt-secret",
            "client-secret-id",
        ] {
            assert!(!serialized.contains(secret));
        }
    }
}

#[tokio::test]
async fn upstream_timeout_keeps_timeout_outcome_after_error_body_is_delivered() {
    let events = Events::default();
    let _guard =
        tracing::subscriber::set_default(tracing_subscriber::registry().with(events.clone()));
    let upstream = upstream(Reply::Slow).await;
    let runtime = runtime(
        config(&[&upstream], Some(100)),
        Options {
            request_timeout: Duration::from_millis(100),
            ..Options::default()
        },
    );
    let response = runtime
        .handle(request(false), CancellationToken::new())
        .await;
    assert_eq!(response.status(), 504);
    consume(response).await;
    let summary = events.request();
    assert_eq!(summary.get("outcome"), "timeout");
    assert_eq!(summary.get("delivery_outcome"), "complete");
    assert_eq!(summary.get("error_code"), "request_timeout");
    assert_eq!(summary.number("quota_charged_tokens"), 10);
    let attempts = events.target("nyro::attempt");
    assert_eq!(attempts.len(), 1);
    assert_eq!(attempts[0].get("outcome"), "timeout");
    assert_eq!(attempts[0].number("upstream_status"), 0);
}

#[tokio::test]
async fn aborting_handle_future_finalizes_request_and_attempt_once_before_headers() {
    let events = Events::default();
    let _guard =
        tracing::subscriber::set_default(tracing_subscriber::registry().with(events.clone()));
    let upstream = upstream(Reply::Slow).await;
    let runtime = runtime(config(&[&upstream], Some(100)), Options::default());
    let mut pending = Box::pin(runtime.handle(request(false), CancellationToken::new()));
    tokio::select! {
        _ = &mut pending => panic!("slow upstream must remain pending"),
        _ = upstream.called() => {},
    }
    drop(pending);
    let summary = events.request();
    assert_eq!(summary.number("status"), 0);
    assert_eq!(summary.get("outcome"), "cancelled");
    assert_eq!(summary.get("delivery_outcome"), "none");
    assert_eq!(summary.number("quota_charged_tokens"), 10);
    let attempts = events.target("nyro::attempt");
    assert_eq!(attempts.len(), 1);
    assert_eq!(attempts[0].get("outcome"), "cancelled");
}

#[tokio::test]
async fn completed_upstream_attempt_survives_downstream_body_drop() {
    let events = Events::default();
    let _guard =
        tracing::subscriber::set_default(tracing_subscriber::registry().with(events.clone()));
    let upstream = upstream(Reply::Json(answer(usage()))).await;
    let runtime = runtime(config(&[&upstream], Some(100)), Options::default());
    let response = runtime
        .handle(request(false), CancellationToken::new())
        .await;
    assert!(events.target("nyro::request").is_empty());
    drop(response);
    let summary = events.request();
    assert_eq!(summary.get("outcome"), "cancelled");
    assert_eq!(summary.get("delivery_outcome"), "cancelled");
    assert_eq!(summary.get("usage_state"), "complete");
    assert_eq!(summary.number("quota_charged_tokens"), 5);
    let attempts = events.target("nyro::attempt");
    assert_eq!(attempts.len(), 1);
    assert_eq!(attempts[0].get("outcome"), "complete");
    assert_eq!(attempts[0].get("quota_outcome"), "actual");
}

#[tokio::test]
async fn cancelling_stream_retains_last_usage_and_charges_fallback_once() {
    let events = Events::default();
    let _guard =
        tracing::subscriber::set_default(tracing_subscriber::registry().with(events.clone()));
    let upstream = upstream(Reply::Sse(frames(false), true)).await;
    let runtime = runtime(config(&[&upstream], Some(100)), Options::default());
    let cancellation = CancellationToken::new();
    let mut input = request(true);
    *input.body_mut() = Body::from(json!({"model":"public","messages":[{"role":"user","content":"prompt-secret"}],"stream":true,"stream_options":{"include_usage":true}}).to_string());
    let response = runtime.handle(input, cancellation.clone()).await;
    let mut body = response.into_body().into_data_stream();
    tokio::time::timeout(Duration::from_secs(3), async {
        let mut received = String::new();
        while !received.contains("\"total_tokens\":5") {
            let chunk = body.next().await.expect("usage before EOF").unwrap();
            received.push_str(&String::from_utf8_lossy(&chunk));
        }
    })
    .await
    .expect("last usage snapshot delivered");
    cancellation.cancel();
    assert!(
        body.next()
            .await
            .expect("cancelled body terminates with error")
            .is_err()
    );
    drop(body);
    let summary = events.request();
    assert_eq!(summary.get("outcome"), "cancelled");
    assert_eq!(summary.get("delivery_outcome"), "cancelled");
    assert_eq!(summary.number("total_tokens"), 5);
    assert_eq!(summary.number("quota_charged_tokens"), 10);
    let attempts = events.target("nyro::attempt");
    assert_eq!(attempts.len(), 1);
    assert_eq!(attempts[0].get("outcome"), "cancelled");
    assert_eq!(attempts[0].number("total_tokens"), 5);
    assert_eq!(attempts[0].get("quota_outcome"), "fallback");
}

#[tokio::test]
async fn embeddings_report_input_usage_and_embedding_workload() {
    let events = Events::default();
    let _guard =
        tracing::subscriber::set_default(tracing_subscriber::registry().with(events.clone()));
    let upstream = upstream(Reply::Json(json!({"object":"list","model":"private-model","data":[{"object":"embedding","index":0,"embedding":[0.25]}],"usage":{"prompt_tokens":3,"total_tokens":3}}))).await;
    let runtime = runtime(config(&[&upstream], None), Options::default());
    let input = Request::builder()
        .method("POST")
        .uri("/v1/embeddings")
        .header("content-type", "application/json")
        .body(Body::from(
            json!({"model":"public","input":"prompt-secret"}).to_string(),
        ))
        .unwrap();
    let response = runtime.handle(input, CancellationToken::new()).await;
    assert_eq!(response.status(), 200);
    consume(response).await;
    let summary = events.request();
    assert_eq!(summary.get("protocol"), "openai_embedding");
    assert_eq!(summary.get("workload"), "embedding");
    assert_eq!(summary.get("usage_state"), "complete");
    assert_eq!(summary.number("input_tokens"), 3);
    assert_eq!(summary.number("output_tokens"), 0);
    assert_eq!(summary.number("total_tokens"), 3);
    assert_eq!(events.target("nyro::attempt").len(), 1);
}

#[tokio::test]
async fn native_protocols_report_their_wire_api_and_usage_without_quota() {
    for format in ["anthropic", "gemini", "openai_responses"] {
        let events = Events::default();
        let _guard =
            tracing::subscriber::set_default(tracing_subscriber::registry().with(events.clone()));
        let (native, path, input) = match format {
            "anthropic" => (
                json!({"id":"answer","type":"message","role":"assistant","model":"private-model","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":2}}),
                "/v1/messages",
                json!({"model":"public","max_tokens":16,"messages":[{"role":"user","content":"Hi"}]}),
            ),
            "gemini" => (
                json!({"responseId":"answer","modelVersion":"private-model","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}),
                "/v1beta/models/public:generateContent",
                json!({"contents":[{"role":"user","parts":[{"text":"Hi"}]}]}),
            ),
            _ => (
                json!({"id":"answer","object":"response","created_at":1,"model":"private-model","status":"completed","error":null,"incomplete_details":null,"output":[{"type":"message","id":"msg","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}),
                "/v1/responses",
                json!({"model":"public","input":"Hi"}),
            ),
        };
        let upstream = upstream(Reply::Json(native)).await;
        let mut configuration = serde_json::to_value(config(&[&upstream], None)).unwrap();
        configuration["models"]["public"]["workloads"] = json!(["chat"]);
        if format == "openai_responses" {
            configuration["providers"]["p0"]["api"] = json!("responses");
        } else {
            configuration["providers"]["p0"]["kind"] = json!(format);
        }
        let runtime = runtime(
            serde_json::from_value(configuration).unwrap(),
            Options::default(),
        );
        let input = Request::builder()
            .method("POST")
            .uri(path)
            .header("content-type", "application/json")
            .body(Body::from(input.to_string()))
            .unwrap();
        let response = runtime.handle(input, CancellationToken::new()).await;
        assert_eq!(response.status(), 200, "{format}");
        consume(response).await;
        let summary = events.request();
        assert_eq!(summary.get("protocol"), format);
        assert_eq!(summary.get("usage_state"), "complete");
        assert_eq!(summary.number("total_tokens"), 5);
        let attempts = events.target("nyro::attempt");
        assert_eq!(attempts.len(), 1);
        assert_eq!(attempts[0].get("protocol"), format);
        assert_eq!(attempts[0].number("input_tokens"), 3);
        assert_eq!(attempts[0].number("output_tokens"), 2);
    }
}

#[tokio::test]
async fn connection_failure_reports_unknown_status_and_released_reservation() {
    let events = Events::default();
    let _guard =
        tracing::subscriber::set_default(tracing_subscriber::registry().with(events.clone()));
    let upstream = upstream(Reply::Slow).await;
    let mut configuration = serde_json::to_value(config(&[&upstream], Some(100))).unwrap();
    // A bound but non-listening socket reserves the port without accepting connections.
    let socket = tokio::net::TcpSocket::new_v4().unwrap();
    socket.bind("127.0.0.1:0".parse().unwrap()).unwrap();
    configuration["providers"]["p0"]["base_url"] =
        json!(format!("http://{}/v1", socket.local_addr().unwrap()));
    let runtime = runtime(
        serde_json::from_value(configuration).unwrap(),
        Options::default(),
    );
    let response = runtime
        .handle(request(false), CancellationToken::new())
        .await;
    assert_eq!(response.status(), 502);
    consume(response).await;
    let summary = events.request();
    assert_eq!(summary.number("quota_charged_tokens"), 0);
    assert_eq!(summary.get("outcome"), "error");
    let attempts = events.target("nyro::attempt");
    assert_eq!(attempts.len(), 1);
    assert_eq!(attempts[0].get("outcome"), "connect_error");
    assert_eq!(attempts[0].number("upstream_status"), 0);
    assert_eq!(attempts[0].get("quota_outcome"), "released");
    assert_eq!(attempts[0].number("quota_charged_tokens"), 0);
    assert!(upstream.calls.lock().unwrap().is_empty());
}

#[tokio::test]
async fn broken_transport_after_headers_is_not_a_protocol_error() {
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    for streaming in [false, true] {
        let events = Events::default();
        let _guard =
            tracing::subscriber::set_default(tracing_subscriber::registry().with(events.clone()));
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        let task = tokio::spawn(async move {
            let (mut socket, _) = listener.accept().await.unwrap();
            let mut request = Vec::new();
            let mut buffer = [0; 4096];
            loop {
                let count = socket.read(&mut buffer).await.unwrap();
                assert!(count > 0);
                request.extend_from_slice(&buffer[..count]);
                let text = String::from_utf8_lossy(&request);
                if let Some(end) = text.find("\r\n\r\n") {
                    let length: usize = text[..end]
                        .lines()
                        .find_map(|line| {
                            let (name, value) = line.split_once(':')?;
                            name.eq_ignore_ascii_case("content-length")
                                .then(|| value.trim().parse().unwrap())
                        })
                        .unwrap();
                    if request.len() >= end + 4 + length {
                        break;
                    }
                }
            }
            let content_type = if streaming {
                "text/event-stream"
            } else {
                "application/json"
            };
            let body = if streaming {
                frames(false)
            } else {
                answer(usage()).to_string()
            };
            // The body is valid protocol data, but the HTTP transport closes short.
            socket.write_all(format!("HTTP/1.1 200 OK\r\nContent-Type: {content_type}\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}", body.len() + 1000).as_bytes()).await.unwrap();
            socket.shutdown().await.unwrap();
        });
        let upstream = Upstream {
            base,
            task,
            calls: Arc::default(),
        };
        let runtime = runtime(config(&[&upstream], Some(100)), Options::default());
        let response = runtime
            .handle(request(streaming), CancellationToken::new())
            .await;
        if streaming {
            assert_eq!(response.status(), 200);
            assert!(to_bytes(response.into_body(), 65536).await.is_err());
        } else {
            assert_eq!(response.status(), 502);
            consume(response).await;
        }
        let attempts = events.target("nyro::attempt");
        assert_eq!(attempts.len(), 1);
        assert_eq!(attempts[0].number("upstream_status"), 200);
        assert_eq!(
            attempts[0].get("outcome"),
            "transport_error",
            "streaming={streaming}"
        );
        assert_eq!(attempts[0].get("quota_outcome"), "fallback");
        assert_eq!(events.request().get("outcome"), "error");
    }
}
