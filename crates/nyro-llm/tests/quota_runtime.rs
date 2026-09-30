//! Actual token accounting against independent local HTTP wire fixtures.
use axum::{
    Router,
    body::{Body, to_bytes},
    http::{Request, Response, StatusCode},
    routing::post,
};
use futures::StreamExt;
use nyro_authn::{KeyAuth, KeyCredential};
use nyro_limit::ConcurrencyLimit;
use nyro_limit::token::{Registry, Rule};
use nyro_llm::{
    config::Config,
    runtime::{Options, Policies, Runtime, SharedResources},
};
use serde_json::{Value, json};
use std::{
    sync::{Arc, Mutex},
    time::Duration,
};
use tokio_util::sync::CancellationToken;

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

#[derive(Clone)]
struct Case {
    config: Config,
    model_limit: Option<u64>,
    windows: Option<(u64, u64)>,
    request_limit: Option<u64>,
}
#[derive(Default)]
struct State {
    runtime: SharedResources,
    limits: Registry,
}
fn configuration(upstreams: &[&Upstream], format: &str, total: u64) -> Case {
    let providers: serde_json::Map<_, _> = upstreams.iter().enumerate().map(|(i, u)| {
        let mut value = json!({"kind":if format == "responses" {"openai"} else {format},"base_url":format!("{}/{}",u.base, if format == "gemini" {"v1beta"} else {"v1"}),"api_key":"upstream-secret"});
        if format == "responses" { value["api"] = json!("responses"); }
        (format!("p{i}"), value)
    }).collect();
    let backends: Vec<_> = upstreams.iter().enumerate().map(|(i, _)| json!({"id":format!("b{i}"),"provider":format!("p{i}"),"upstream_model":"private","priority":i})).collect();
    Case { config: serde_json::from_value(json!({"providers":providers,"models":{"public":{"backends":backends,"max_attempts":upstreams.len(),"workloads":if format == "openai" {json!(["chat","embedding"])} else {json!(["chat"])},"subjects":["alice","bob"]}}})).unwrap(), model_limit: Some(total), windows: None, request_limit: None }
}
fn rule(limit: u64, seconds: u64) -> Rule {
    Rule {
        limit,
        window: Duration::from_secs(seconds),
    }
}
fn runtime(case: Case, limit: &ConcurrencyLimit, options: Options, state: &State) -> Runtime {
    let mut policies = Policies {
        registry: state.limits.clone(),
        ..Default::default()
    };
    if let Some(limit) = case.model_limit {
        for id in case.config.models.keys() {
            policies.models.insert(
                id.clone(),
                state
                    .limits
                    .bind(id, vec![], vec![rule(limit, 3600)])
                    .unwrap(),
            );
        }
    }
    if let Some((minute, day)) = case.windows {
        let requests = case
            .request_limit
            .map(|limit| vec![rule(limit, 60), rule(limit, 86400)])
            .unwrap_or_default();
        policies.consumers.insert(
            "alice".into(),
            state
                .limits
                .bind("alice", requests, vec![rule(minute, 60), rule(day, 86400)])
                .unwrap(),
        );
    }
    Runtime::with_policies(
        case.config,
        Arc::new(
            KeyAuth::new(
                [("alice", "alice-secret"), ("bob", "bob-secret")]
                    .into_iter()
                    .map(|(id, secret)| KeyCredential {
                        id: id.into(),
                        secret: secret.into(),
                        enabled: true,
                        expires_at: None,
                    })
                    .collect(),
            )
            .unwrap(),
        ),
        limit.clone(),
        options,
        state.runtime.clone(),
        policies,
    )
    .unwrap()
}
fn used(state: &State, scope: &str, seconds: u64) -> u64 {
    // Probe public token-only admission without changing request counters.
    let (mut low, mut high) = (0u64, 100_000u64);
    while low < high {
        let middle = low + (high - low) / 2 + 1;
        let policy = state
            .limits
            .bind(scope, vec![], vec![rule(middle, seconds)])
            .unwrap();
        if state.limits.admit(&[&policy]).is_err() {
            low = middle;
        } else {
            high = middle - 1;
        }
    }
    low
}
fn balance(state: &State, expected: u64) {
    assert_eq!(used(state, "public", 3600), expected);
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
async fn consume(response: Response<Body>) -> Value {
    serde_json::from_slice(&to_bytes(response.into_body(), 65536).await.unwrap()).unwrap()
}

#[path = "quota_runtime/token_windows.rs"]
mod token_windows;
