//! Multi-backend behavior against independent loopback wire fixtures.
use std::{
    collections::BTreeMap,
    sync::{Arc, Mutex},
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
fn canonical_routing_configuration_is_accepted() {
    let config = serde_json::from_value::<Config>(json!({
        "providers":{"p":{"kind":"openai","base_url":"http://127.0.0.1:1/v1"}},
        "models":{"public":{"backends":[{"id":"primary","provider":"p","upstream_model":"private"}],"workloads":["chat"]}}
    }));
    assert!(config.is_ok(), "{config:?}");
}

struct Upstream {
    base: String,
    format: &'static str,
    calls: Arc<Mutex<Vec<Value>>>,
    task: tokio::task::JoinHandle<()>,
}

impl Upstream {
    fn count(&self) -> usize {
        self.calls.lock().unwrap().len()
    }
}

impl Drop for Upstream {
    fn drop(&mut self) {
        self.task.abort();
    }
}

// Fixtures deliberately contain upstream aliases and never invoke the codecs under test.
fn answer(format: &str, tools: bool) -> Value {
    match format {
        "openai" => json!({
            "id":"route-answer","object":"chat.completion","created":7,"model":"upstream-private",
            "choices":[{"index":0,"message":if tools {
                json!({"role":"assistant","content":null,"tool_calls":[{"id":"call-route","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"Paris\"}"}}]})
            } else { json!({"role":"assistant","content":"Routed hello"}) },
            "finish_reason":if tools { "tool_calls" } else { "stop" }}],
            "usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}
        }),
        "anthropic" => json!({
            "id":"route-answer","type":"message","role":"assistant","model":"upstream-private",
            "content":if tools { json!([{"type":"tool_use","id":"call-route","name":"lookup","input":{"city":"Paris"}}]) }
                else { json!([{"type":"text","text":"Routed hello"}]) },
            "stop_reason":if tools { "tool_use" } else { "end_turn" },"stop_sequence":null,
            "usage":{"input_tokens":4,"output_tokens":2}
        }),
        "gemini" => json!({
            "responseId":"route-answer","modelVersion":"upstream-private",
            "candidates":[{"index":0,"content":{"role":"model","parts":if tools {
                json!([{"functionCall":{"id":"call-route","name":"lookup","args":{"city":"Paris"}}}])
            } else { json!([{"text":"Routed hello"}]) }},"finishReason":"STOP"}],
            "usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}
        }),
        _ => unreachable!(),
    }
}

fn frames(format: &str, tools: bool, complete: bool) -> String {
    let payload = answer(format, tools);
    match format {
        "openai" => {
            let delta = if tools {
                json!({"role":"assistant","tool_calls":[{"index":0,"id":"call-route","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"Paris\"}"}}]})
            } else {
                json!({"role":"assistant","content":"Routed hello"})
            };
            let first = json!({"id":"route-answer","object":"chat.completion.chunk","created":7,"model":"upstream-private","choices":[{"index":0,"delta":delta,"finish_reason":null}]});
            let mut text = format!("data: {first}\n\n");
            if complete {
                let last = json!({"id":"route-answer","object":"chat.completion.chunk","created":7,"model":"upstream-private","choices":[{"index":0,"delta":{},"finish_reason":if tools {"tool_calls"} else {"stop"}}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}});
                text.push_str(&format!("data: {last}\n\ndata: [DONE]\n\n"));
            }
            text
        }
        "anthropic" => {
            let mut start = payload.clone();
            start["content"] = json!([]);
            start["stop_reason"] = Value::Null;
            start["usage"]["output_tokens"] = json!(0);
            let (block, delta) = if tools {
                (
                    json!({"type":"tool_use","id":"call-route","name":"lookup","input":{}}),
                    json!({"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}),
                )
            } else {
                (
                    json!({"type":"text","text":""}),
                    json!({"type":"text_delta","text":"Routed hello"}),
                )
            };
            let mut events = vec![
                json!({"type":"message_start","message":start}),
                json!({"type":"content_block_start","index":0,"content_block":block}),
                json!({"type":"content_block_delta","index":0,"delta":delta}),
            ];
            if complete {
                events.extend([
                    json!({"type":"content_block_stop","index":0}),
                    json!({"type":"message_delta","delta":{"stop_reason":payload["stop_reason"],"stop_sequence":null},"usage":{"output_tokens":2}}),
                    json!({"type":"message_stop"}),
                ]);
            }
            events
                .iter()
                .map(|event| {
                    format!(
                        "event: {}\ndata: {event}\n\n",
                        event["type"].as_str().unwrap()
                    )
                })
                .collect()
        }
        "gemini" => format!("data: {payload}\n\n"),
        _ => unreachable!(),
    }
}

async fn upstream(format: &'static str, mode: &'static str) -> Upstream {
    let calls = Arc::new(Mutex::new(Vec::new()));
    let observed = calls.clone();
    let app = Router::new().fallback(post(move |request: Request<Body>| {
        let observed = observed.clone();
        async move {
            let path = request.uri().to_string();
            let headers: BTreeMap<_, _> = request.headers().iter()
                .map(|(name, value)| (name.to_string(), value.to_str().unwrap().to_owned())).collect();
            let body: Value = serde_json::from_slice(&to_bytes(request.into_body(), 1024 * 1024).await.unwrap()).unwrap();
            let streaming = body["stream"] == true || path.contains(":streamGenerateContent");
            observed.lock().unwrap().push(json!({"path":path,"headers":headers,"body":body}));
            if mode == "slow" {
                tokio::time::sleep(Duration::from_secs(30)).await;
            }
            if mode == "error" {
                return Response::builder().status(503).body(Body::from("private failure secret")).unwrap();
            }
            if streaming {
                let text = if mode == "bad" { "data: {broken-json}\n\n".into() }
                    else { frames(format, mode == "tools", !matches!(mode, "truncated" | "hang")) };
                let chunks: Vec<_> = text.into_bytes().chunks(11)
                    .map(|chunk| Ok::<_, std::io::Error>(chunk.to_vec())).collect();
                let stream = futures::stream::iter(chunks);
                let stream = if mode == "hang" { stream.chain(futures::stream::pending()).boxed() } else { stream.boxed() };
                return Response::builder().header("content-type", "text/event-stream").body(Body::from_stream(stream)).unwrap();
            }
            let payload = if path.ends_with("/embeddings") {
                json!({"object":"list","model":"upstream-private","data":[{"object":"embedding","index":0,"embedding":[0.1,0.9]}],"usage":{"prompt_tokens":4,"total_tokens":4}})
            } else { answer(format, mode == "tools") };
            Response::builder().header("content-type", "application/json").body(Body::from(payload.to_string())).unwrap()
        }
    }));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base = format!(
        "http://{}/{}",
        listener.local_addr().unwrap(),
        if format == "gemini" { "v1beta" } else { "v1" }
    );
    let task = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    Upstream {
        base,
        format,
        calls,
        task,
    }
}

fn configuration(upstreams: &[&Upstream], weights: &[u32], workloads: &[&str]) -> Config {
    let mut providers = serde_json::Map::new();
    let mut backends = Vec::new();
    for (index, (upstream, weight)) in upstreams.iter().zip(weights).enumerate() {
        let id = format!("provider-{index}");
        providers.insert(id.clone(), json!({"kind":upstream.format,"base_url":upstream.base,"api_key":format!("provider-secret-{index}")}));
        backends.push(json!({"id":format!("backend-{index}"),"provider":id,"upstream_model":format!("private-{index}"),"weight":weight}));
    }
    serde_json::from_value(json!({"providers":providers,"models":{"public":{"backends":backends,"workloads":workloads,"subjects":["alice"]}}})).unwrap()
}

fn keys() -> Arc<ApiKeys> {
    Arc::new(
        ApiKeys::new(vec![
            ApiKey {
                id: "alice".into(),
                secret: "client-secret".into(),
            },
            ApiKey {
                id: "bob".into(),
                secret: "bob-secret".into(),
            },
        ])
        .unwrap(),
    )
}

fn runtime(config: Config, limit: &ConcurrencyLimit, options: Options) -> Runtime {
    Runtime::new(config, keys(), limit.clone(), options).unwrap()
}

fn request(path: &str, body: Value, credential: Option<&str>) -> Request<Body> {
    let mut request = Request::builder()
        .method("POST")
        .uri(path)
        .header("content-type", "application/json");
    if let Some(key) = credential {
        request = request.header("authorization", format!("Bearer {key}"));
    }
    request.body(Body::from(body.to_string())).unwrap()
}

fn chat(streaming: bool, tools: bool) -> Value {
    let mut body = json!({"model":"public","messages":[{"role":"user","content":"Where?"}],"stream":streaming});
    if tools {
        body["tools"] = json!([{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}}]);
        body["messages"].as_array_mut().unwrap().extend([
            json!({"role":"assistant","tool_calls":[{"id":"call-before","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"Rome\"}"}}]}),
            json!({"role":"tool","tool_call_id":"call-before","content":"{\"weather\":\"sunny\"}"}),
        ]);
    }
    body
}

async fn invoke(runtime: &Runtime, body: Value) -> Response<Body> {
    runtime
        .handle(
            request("/v1/chat/completions", body, Some("client-secret")),
            CancellationToken::new(),
        )
        .await
}

fn assert_wire(call: &Value, format: &str, index: usize, streaming: bool) {
    let path = match format {
        "openai" => "/v1/chat/completions".into(),
        "anthropic" => "/v1/messages".into(),
        "gemini" => format!(
            "/v1beta/models/private-{index}:{}",
            if streaming {
                "streamGenerateContent?alt=sse"
            } else {
                "generateContent"
            }
        ),
        _ => unreachable!(),
    };
    assert_eq!(call["path"], path);
    let headers = &call["headers"];
    let (name, secret) = match format {
        "openai" => ("authorization", format!("Bearer provider-secret-{index}")),
        "anthropic" => ("x-api-key", format!("provider-secret-{index}")),
        "gemini" => ("x-goog-api-key", format!("provider-secret-{index}")),
        _ => unreachable!(),
    };
    assert_eq!(headers[name], secret);
    assert!(!headers.to_string().contains("client-secret"));
    for other in ["authorization", "x-api-key", "x-goog-api-key"] {
        if other != name {
            assert!(headers.get(other).is_none());
        }
    }
    if format == "anthropic" {
        assert_eq!(headers["anthropic-version"], "2023-06-01");
    }
    if format != "gemini" {
        assert_eq!(call["body"]["model"], format!("private-{index}"));
    }
}

#[tokio::test]
async fn zero_weight_backends_are_never_called_and_selected_protocol_owns_the_wire() {
    for tools in [false, true] {
        let mode = if tools { "tools" } else { "normal" };
        let openai = upstream("openai", mode).await;
        let anthropic = upstream("anthropic", mode).await;
        let gemini = upstream("gemini", mode).await;
        let upstreams = [&openai, &anthropic, &gemini];
        for selected in 0..3 {
            let mut weights = [0; 3];
            weights[selected] = 1;
            let limit = ConcurrencyLimit::new(1).unwrap();
            let runtime = runtime(
                configuration(&upstreams, &weights, &["chat"]),
                &limit,
                Options::default(),
            );
            for streaming in [false, true] {
                let before: Vec<_> = upstreams.iter().map(|upstream| upstream.count()).collect();
                let mut input = chat(streaming, tools);
                input["max_tokens"] = json!(32);
                let result = invoke(&runtime, input).await;
                assert_eq!(
                    result.status(),
                    StatusCode::OK,
                    "selected={selected}, tools={tools}, streaming={streaming}"
                );
                assert_eq!(limit.available(), 0);
                let output = to_bytes(result.into_body(), 16384).await.unwrap();
                let output = String::from_utf8(output.to_vec()).unwrap();
                assert!(output.contains("public"), "{output}");
                assert!(!output.contains("upstream-private"));
                if tools {
                    assert!(
                        output.contains("lookup")
                            && output.contains("call-route")
                            && output.contains("Paris"),
                        "{output}"
                    );
                } else {
                    assert!(output.contains("Routed hello"));
                }
                if streaming {
                    assert_eq!(output.matches("[DONE]").count(), 1);
                }
                assert_eq!(limit.available(), 1);
                for (index, upstream) in upstreams.iter().enumerate() {
                    assert_eq!(
                        upstream.count(),
                        before[index] + usize::from(index == selected)
                    );
                }
                let calls = upstreams[selected].calls.lock().unwrap();
                let call = calls.last().unwrap();
                assert_wire(call, upstreams[selected].format, selected, streaming);
                if tools {
                    let body = call["body"].to_string();
                    assert!(
                        body.contains("call-before")
                            && body.contains("lookup")
                            && body.contains("sunny"),
                        "{body}"
                    );
                    assert!(call["body"]["tools"].is_array());
                }
            }
        }
    }
}

#[tokio::test]
async fn request_compatibility_filters_high_weight_backend_before_dispatch() {
    for tools in [false, true] {
        let mode = if tools { "tools" } else { "normal" };
        let anthropic = upstream("anthropic", mode).await;
        let openai = upstream("openai", mode).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            configuration(&[&anthropic, &openai], &[u32::MAX, 1], &["chat"]),
            &limit,
            Options::default(),
        );
        // Both backends are enabled. Only Anthropic requires explicit max_tokens.
        for streaming in [false, true] {
            let response = invoke(&runtime, chat(streaming, tools)).await;
            assert_eq!(response.status(), StatusCode::OK);
            let body = to_bytes(response.into_body(), 16384).await.unwrap();
            assert!(String::from_utf8_lossy(&body).contains(if tools {
                "lookup"
            } else {
                "Routed hello"
            }));
            assert_eq!(limit.available(), 1);
            assert_eq!(anthropic.count(), 0);
        }
        let calls = openai.calls.lock().unwrap();
        assert_eq!(calls.len(), 2);
        for (index, call) in calls.iter().enumerate() {
            assert_wire(call, "openai", 1, index == 1);
        }
    }
}

#[tokio::test]
async fn embedding_uses_one_backend_and_restores_public_alias() {
    let disabled = upstream("openai", "normal").await;
    let selected = upstream("openai", "normal").await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(
        configuration(&[&disabled, &selected], &[0, 100], &["chat", "embedding"]),
        &limit,
        Options::default(),
    );
    let response = runtime
        .handle(
            request(
                "/v1/embeddings",
                json!({"model":"public","input":["alpha","beta"]}),
                Some("client-secret"),
            ),
            CancellationToken::new(),
        )
        .await;
    assert_eq!(response.status(), StatusCode::OK);
    let body: Value =
        serde_json::from_slice(&to_bytes(response.into_body(), 16384).await.unwrap()).unwrap();
    assert_eq!(body["model"], "public");
    assert_eq!(body["data"][0]["embedding"], json!([0.1, 0.9]));
    assert_eq!(limit.available(), 1);
    assert_eq!(disabled.count(), 0);
    let calls = selected.calls.lock().unwrap();
    assert_eq!(calls.len(), 1);
    assert_eq!(calls[0]["path"], "/v1/embeddings");
    assert_eq!(
        calls[0]["headers"]["authorization"],
        "Bearer provider-secret-1"
    );
    assert_eq!(calls[0]["body"]["model"], "private-1");
    assert_eq!(calls[0]["body"]["input"], json!(["alpha", "beta"]));
}

#[tokio::test]
async fn responses_ingress_keeps_stateless_chat_payload_and_stream_usage_after_filtering() {
    let anthropic = upstream("anthropic", "normal").await;
    let openai = upstream("openai", "normal").await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(
        configuration(&[&anthropic, &openai], &[u32::MAX, 1], &["chat"]),
        &limit,
        Options::default(),
    );
    for streaming in [false, true] {
        let response = runtime
            .handle(
                request(
                    "/v1/responses",
                    json!({"model":"public","input":"Where?","stream":streaming,"store":false}),
                    Some("client-secret"),
                ),
                CancellationToken::new(),
            )
            .await;
        assert_eq!(response.status(), StatusCode::OK);
        let bytes = to_bytes(response.into_body(), 16384).await.unwrap();
        let output = String::from_utf8_lossy(&bytes);
        assert!(output.contains("public") && output.contains("Routed hello"));
        if streaming {
            let terminal = output
                .lines()
                .filter_map(|line| line.strip_prefix("data: "))
                .map(|data| serde_json::from_str::<Value>(data).unwrap())
                .find(|event| event["type"] == "response.completed")
                .unwrap();
            assert_eq!(terminal["response"]["usage"]["total_tokens"], 6);
        }
        assert_eq!(limit.available(), 1);
    }
    assert_eq!(anthropic.count(), 0);
    let calls = openai.calls.lock().unwrap();
    assert_eq!(calls.len(), 2);
    for (index, call) in calls.iter().enumerate() {
        assert_wire(call, "openai", 1, index == 1);
        assert_eq!(call["body"]["store"], false);
    }
    assert_eq!(calls[1]["body"]["stream_options"]["include_usage"], true);
}

#[tokio::test]
async fn no_compatible_backend_returns_sanitized_400_without_dispatch() {
    let first = upstream("anthropic", "normal").await;
    let second = upstream("anthropic", "normal").await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(
        configuration(&[&first, &second], &[1, 100], &["chat"]),
        &limit,
        Options::default(),
    );
    let response = invoke(&runtime, chat(false, false)).await;
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    let body = to_bytes(response.into_body(), 16384).await.unwrap();
    let body = String::from_utf8_lossy(&body);
    assert!(!body.contains("private-") && !body.contains("secret"));
    assert_eq!(first.count() + second.count(), 0);
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn disabled_incompatible_workloads_still_fail_startup() {
    let openai = upstream("openai", "normal").await;
    let anthropic = upstream("anthropic", "normal").await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    for weight in [0, 1] {
        let config = configuration(
            &[&openai, &anthropic],
            &[100, weight],
            &["chat", "embedding"],
        );
        assert!(Runtime::new(config, keys(), limit.clone(), Options::default()).is_err());
    }
    assert_eq!(openai.count() + anthropic.count(), 0);
}

#[tokio::test]
async fn authentication_authorization_and_admission_deny_before_dispatch() {
    let first = upstream("openai", "normal").await;
    let second = upstream("openai", "normal").await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(
        configuration(&[&first, &second], &[1, 1], &["chat"]),
        &limit,
        Options::default(),
    );
    for (credential, status) in [(None, 401), (Some("wrong"), 401), (Some("bob-secret"), 403)] {
        let response = runtime
            .handle(
                request("/v1/chat/completions", chat(false, false), credential),
                CancellationToken::new(),
            )
            .await;
        assert_eq!(response.status().as_u16(), status);
        drop(response);
        assert_eq!(limit.available(), 1);
    }
    let permit = limit.try_acquire().unwrap();
    let response = invoke(&runtime, chat(false, false)).await;
    assert_eq!(response.status(), StatusCode::TOO_MANY_REQUESTS);
    drop(response);
    assert_eq!(first.count() + second.count(), 0);
    drop(permit);
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn selected_failure_and_invalid_streams_never_attempt_an_enabled_alternative() {
    for (mode, status) in [("error", 503), ("bad", 502), ("truncated", 200)] {
        // Equal behavior makes the assertion deterministic whichever backend is chosen.
        let first = upstream("openai", mode).await;
        let second = upstream("openai", mode).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            configuration(&[&first, &second], &[1, 1], &["chat"]),
            &limit,
            Options::default(),
        );
        let response = invoke(&runtime, chat(mode != "error", false)).await;
        assert_eq!(response.status().as_u16(), status, "{mode}");
        let body = to_bytes(response.into_body(), 16384).await;
        if mode == "truncated" {
            assert!(body.is_err());
        } else {
            assert!(!String::from_utf8_lossy(&body.unwrap()).contains("private failure"));
        }
        assert_eq!(first.count() + second.count(), 1, "{mode}");
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn stream_cancel_drop_and_whole_request_deadline_release_admission_without_retry() {
    for action in ["cancel", "drop", "deadline"] {
        let first = upstream("openai", "hang").await;
        let second = upstream("openai", "hang").await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let options = Options {
            request_timeout: Duration::from_millis(300),
            ..Options::default()
        };
        let runtime = runtime(
            configuration(&[&first, &second], &[1, 1], &["chat"]),
            &limit,
            options,
        );
        let cancellation = CancellationToken::new();
        let response = runtime
            .handle(
                request(
                    "/v1/chat/completions",
                    chat(true, false),
                    Some("client-secret"),
                ),
                cancellation.clone(),
            )
            .await;
        assert_eq!(response.status(), StatusCode::OK);
        assert_eq!(limit.available(), 0);
        match action {
            "drop" => drop(response),
            "cancel" => {
                cancellation.cancel();
                assert!(to_bytes(response.into_body(), 16384).await.is_err());
            }
            "deadline" => {
                let result = tokio::time::timeout(
                    Duration::from_secs(2),
                    to_bytes(response.into_body(), 16384),
                )
                .await
                .unwrap();
                assert!(result.is_err());
            }
            _ => unreachable!(),
        }
        assert_eq!(limit.available(), 1, "{action}");
        assert_eq!(first.count() + second.count(), 1, "{action}");
    }
}

#[tokio::test]
async fn pre_header_cancellation_drop_and_deadline_release_admission_without_retry() {
    for action in ["cancel", "drop", "deadline"] {
        let first = upstream("openai", "slow").await;
        let second = upstream("openai", "slow").await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            configuration(&[&first, &second], &[1, 1], &["chat"]),
            &limit,
            Options {
                request_timeout: Duration::from_millis(300),
                ..Options::default()
            },
        );
        let cancellation = CancellationToken::new();
        let mut pending = Box::pin(runtime.handle(
            request(
                "/v1/chat/completions",
                chat(false, false),
                Some("client-secret"),
            ),
            cancellation.clone(),
        ));
        tokio::select! {
            _ = &mut pending => panic!("fixture must remain pending before response headers"),
            result = tokio::time::timeout(Duration::from_secs(2), async {
                while first.count() + second.count() == 0 { tokio::task::yield_now().await; }
            }) => result.unwrap(),
        }
        assert_eq!(limit.available(), 0);
        if action == "drop" {
            drop(pending);
        } else {
            if action == "cancel" {
                cancellation.cancel();
            }
            let response = tokio::time::timeout(Duration::from_secs(2), pending)
                .await
                .unwrap();
            assert_eq!(
                response.status().as_u16(),
                if action == "cancel" { 503 } else { 504 }
            );
            drop(response);
        }
        assert_eq!(limit.available(), 1, "{action}");
        assert_eq!(first.count() + second.count(), 1, "{action}");
    }
}
