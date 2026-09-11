//! Anthropic native fidelity, routing, stream validation, and accounting over real HTTP.
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
    collections::BTreeMap,
    sync::{Arc, Mutex},
};
use tokio_util::sync::CancellationToken;

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
async fn upstream(status: u16, text: String, streaming: bool) -> Upstream {
    let calls = Arc::new(Mutex::new(Vec::new()));
    let observed = calls.clone();
    let app = Router::new().fallback(post(move |request: Request<Body>| {
        let observed = observed.clone();
        let text = text.clone();
        async move {
            let path = request.uri().to_string();
            let headers: BTreeMap<_, _> = request
                .headers()
                .iter()
                .map(|(k, v)| (k.to_string(), v.to_str().unwrap().to_owned()))
                .collect();
            let body: Value =
                serde_json::from_slice(&to_bytes(request.into_body(), 16384).await.unwrap())
                    .unwrap();
            observed
                .lock()
                .unwrap()
                .push(json!({"path":path,"headers":headers,"body":body}));
            let body = if streaming {
                // Split JSON, framing, and multibyte text across transport chunks.
                let chunks: Vec<_> = text
                    .as_bytes()
                    .chunks(7)
                    .map(|chunk| Ok::<_, std::io::Error>(chunk.to_vec()))
                    .collect();
                Body::from_stream(futures::stream::iter(chunks))
            } else {
                Body::from(text)
            };
            Response::builder()
                .status(status)
                .header(
                    "content-type",
                    if streaming {
                        "text/event-stream"
                    } else {
                        "application/json"
                    },
                )
                .body(body)
                .unwrap()
        }
    }));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base = format!("http://{}/v1", listener.local_addr().unwrap());
    let task = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    Upstream { base, calls, task }
}
fn config(backends: &[(&Upstream, &str, bool)]) -> Value {
    let providers: serde_json::Map<_, _> = backends
        .iter()
        .enumerate()
        .map(|(i, (upstream, kind, native))| {
            let mut provider =
                json!({"kind":kind,"base_url":upstream.base,"api_key":"provider-secret"});
            if *native {
                provider["native_chat"] = json!(true);
            }
            (format!("p{i}"), provider)
        })
        .collect();
    let backends: Vec<_> = backends.iter().enumerate().map(|(i, _)|
        json!({"id":format!("b{i}"),"provider":format!("p{i}"),"upstream_model":"private-model","priority":i})).collect();
    json!({"providers":providers,"models":{"public":{"max_attempts":backends.len(),"backends":backends,"workloads":["chat"],"subjects":["alice"]}}})
}
fn runtime(config: Value, limit: &ConcurrencyLimit, options: Options) -> Runtime {
    runtime_with_resources(config, limit, options, SharedResources::default())
}
fn runtime_with_resources(
    config: Value,
    limit: &ConcurrencyLimit,
    options: Options,
    resources: SharedResources,
) -> Runtime {
    let config: Config = serde_json::from_value(config).unwrap();
    Runtime::with_resources(
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
        resources,
    )
    .unwrap()
}
fn request(body: Value) -> Request<Body> {
    Request::builder()
        .method("POST")
        .uri("/v1/messages")
        .header("content-type", "application/json")
        .header("x-api-key", "client-secret")
        .header("anthropic-beta", "caller-beta")
        .header("anthropic-version", "2099-01-01")
        .header("x-private-caller", "caller-metadata")
        .body(Body::from(body.to_string()))
        .unwrap()
}
fn input(extended: bool, stream: bool) -> Value {
    let mut body = json!({"model":"public","messages":[{"role":"user","content":"Hello"}],"max_tokens":16,"stream":stream});
    if extended {
        body["thinking"] = json!({"type":"enabled","budget_tokens":1024});
        body["vendor_option"] = json!({"nested":[true,7,null]});
        body["messages"][0]["content"] =
            json!([{"type":"text","text":"Hello","cache_control":{"type":"ephemeral","ttl":"1h"}}]);
        body["messages"].as_array_mut().unwrap().extend([
            json!({"role":"assistant","content":[
                {"type":"thinking","thinking":"Use the tool","signature":"history-signature"},
                {"type":"redacted_thinking","data":"history-redacted"},
                {"type":"tool_use","id":"history-tool","name":"weather","input":{"city":"北京"}}]}),
            json!({"role":"user","content":[{"type":"tool_result","tool_use_id":"history-tool","content":"Sunny"}]}),
        ]);
    }
    body
}
fn answer() -> Value {
    json!({"id":"answer","type":"message","role":"assistant","model":"private-model",
        "content":[{"type":"thinking","thinking":"想一想","signature":"opaque-signature"},
            {"type":"redacted_thinking","data":"opaque-data"},
            {"type":"text","text":"晴天"},
            {"type":"tool_use","id":"tool-1","name":"weather","input":{"city":"北京"}}],
        "stop_reason":"tool_use","stop_sequence":null,"vendor_field":{"ok":true},
        "usage":{"input_tokens":3,"cache_creation_input_tokens":4,"cache_read_input_tokens":5,"output_tokens":2,"cache_creation":{"ephemeral_1h_input_tokens":4,"ephemeral_5m_input_tokens":0},"vendor_billable_tokens":99}})
}
fn frames() -> Vec<Value> {
    vec![
        json!({"type":"message_start","message":{"id":"answer","type":"message","role":"assistant","model":"private-model","content":[],"stop_reason":null,"stop_sequence":null,
            "usage":{"input_tokens":3,"cache_creation_input_tokens":4,"cache_read_input_tokens":5,"output_tokens":0,"cache_creation":{"ephemeral_1h_input_tokens":4,"ephemeral_5m_input_tokens":0},"vendor_billable_tokens":99}},"vendor_trace":"trace"}),
        json!({"type":"ping"}),
        json!({"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}),
        json!({"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"想一想"}}),
        json!({"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"opaque-signature"}}),
        json!({"type":"content_block_stop","index":0}),
        json!({"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"opaque-data"}}),
        json!({"type":"content_block_stop","index":1}),
        json!({"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tool-1","name":"weather","input":{}}}),
        json!({"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"北京\"}"}}),
        json!({"type":"content_block_stop","index":2}),
        json!({"type":"content_block_start","index":3,"content_block":{"type":"vendor_block","opaque":[1,true]}}),
        json!({"type":"content_block_delta","index":3,"delta":{"type":"vendor_delta","opaque":{"value":17}}}),
        json!({"type":"content_block_stop","index":3}),
        json!({"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":1},"vendor_trace":"trace-2"}),
        json!({"type":"message_delta","delta":{},"usage":{"output_tokens":2}}),
        json!({"type":"message_stop"}),
    ]
}
fn sse(frames: &[Value]) -> String {
    frames
        .iter()
        .map(|v| format!("event: {}\ndata: {v}\n\n", v["type"].as_str().unwrap()))
        .collect()
}
async fn invoke(runtime: &Runtime, body: Value) -> Response<Body> {
    runtime
        .handle(request(body), CancellationToken::new())
        .await
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
async fn native_json_preserves_extensions_and_replaces_caller_authentication() {
    let upstream = upstream(200, answer().to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let mut config = config(&[(&upstream, "anthropic", true)]);
    config["models"]["public"]["quota"] = json!({"total_tokens":14,"reserve_tokens":1});
    let gateway = runtime(config, &limit, Options::default());
    let mut unauthorized = request(input(true, false));
    unauthorized.headers_mut().remove("x-api-key");
    assert_eq!(
        gateway
            .handle(unauthorized, CancellationToken::new())
            .await
            .status(),
        StatusCode::UNAUTHORIZED
    );
    assert!(upstream.calls.lock().unwrap().is_empty());
    let response = invoke(&gateway, input(true, false)).await;
    assert_eq!(response.status(), StatusCode::OK);
    let actual: Value = serde_json::from_str(&consume(response).await).unwrap();
    let mut expected = answer();
    expected["model"] = json!("public");
    assert_eq!(actual, expected);
    {
        let calls = upstream.calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        let mut expected = input(true, false);
        expected["model"] = json!("private-model");
        assert_eq!(calls[0]["body"], expected);
        assert_eq!(calls[0]["path"], "/v1/messages");
        assert_eq!(calls[0]["headers"]["x-api-key"], "provider-secret");
        assert_eq!(calls[0]["headers"]["anthropic-version"], "2023-06-01");
        for header in ["x-private-caller", "anthropic-beta", "authorization"] {
            assert!(calls[0]["headers"][header].is_null(), "{header}");
        }
        assert!(!calls[0]["headers"].to_string().contains("client-secret"));
    }
    assert_eq!(limit.available(), 1);
    assert_eq!(
        invoke(&gateway, input(false, false)).await.status(),
        StatusCode::TOO_MANY_REQUESTS
    );
    assert_eq!(upstream.calls.lock().unwrap().len(), 1);
}

#[tokio::test]
async fn native_sse_preserves_opaque_blocks_and_charges_cached_cumulative_usage_once() {
    let upstream = upstream(200, sse(&frames()), true).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let mut config = config(&[(&upstream, "anthropic", true)]);
    // Each request uses (3 + 4 + 5) input + 2 output = 14 tokens.
    config["models"]["public"]["quota"] = json!({"total_tokens":28,"reserve_tokens":1});
    let gateway = runtime(config, &limit, Options::default());
    for _ in 0..2 {
        let response = invoke(&gateway, input(true, true)).await;
        assert_eq!(response.status(), StatusCode::OK);
        assert_eq!(limit.available(), 0);
        let output = consume(response).await;
        let actual: Vec<Value> = output
            .lines()
            .filter_map(|line| line.strip_prefix("data: "))
            .map(|line| serde_json::from_str(line).unwrap())
            .collect();
        let mut expected = frames();
        expected[0]["message"]["model"] = json!("public");
        assert_eq!(actual, expected);
        assert_eq!(output.matches("event: message_stop\n").count(), 1);
        assert_eq!(limit.available(), 1);
    }
    assert_eq!(
        invoke(&gateway, input(false, false)).await.status(),
        StatusCode::TOO_MANY_REQUESTS
    );
    assert_eq!(upstream.calls.lock().unwrap().len(), 2);
}

#[tokio::test]
async fn original_extensions_cannot_cross_strict_or_native_other_protocol_backends() {
    for (kind, native) in [("anthropic", false), ("openai", true), ("gemini", false)] {
        let upstream = upstream(200, answer().to_string(), false).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let gateway = runtime(
            config(&[(&upstream, kind, native)]),
            &limit,
            Options::default(),
        );
        assert_eq!(
            invoke(&gateway, input(true, false)).await.status(),
            StatusCode::BAD_REQUEST,
            "{kind}"
        );
        assert!(upstream.calls.lock().unwrap().is_empty());
        assert_eq!(limit.available(), 1);
    }
    let first = upstream(503, "private provider-secret".into(), false).await;
    let incompatible = upstream(200, answer().to_string(), false).await;
    let backup = upstream(200, answer().to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway = runtime(
        config(&[
            (&first, "anthropic", true),
            (&incompatible, "openai", true),
            (&backup, "anthropic", true),
        ]),
        &limit,
        Options::default(),
    );
    let response = invoke(&gateway, input(true, false)).await;
    assert_eq!(response.status(), StatusCode::OK);
    consume(response).await;
    assert_eq!(first.calls.lock().unwrap().len(), 1);
    assert!(incompatible.calls.lock().unwrap().is_empty());
    assert_eq!(backup.calls.lock().unwrap().len(), 1);
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn malformed_native_streams_fail_without_retry_and_release_admission() {
    let valid = frames();
    let mut cases = vec![
        "event: message_start\ndata: {broken-json}\n\n".into(),
        sse(&valid[..valid.len() - 1]),
    ];
    for replacement in [
        json!({"type":"message_stop"}),
        json!({"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}),
        json!({"type":"error","error":{"type":"api_error","message":"private provider-secret"}}),
        json!({"type":"vendor_event","secret":"provider-secret"}),
    ] {
        cases.push(sse(&[replacement]));
    }
    for (index, replacement) in [
        (
            3,
            json!({"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"wrong index"}}),
        ),
        (
            3,
            json!({"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":17}}),
        ),
        (
            4,
            json!({"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":false}}),
        ),
        (
            9,
            json!({"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":{}}}),
        ),
        (5, json!({"type":"message_stop"})),
        (
            14,
            json!({"type":"message_delta","delta":{"stop_reason":null},"usage":{"output_tokens":1}}),
        ),
        (
            15,
            json!({"type":"error","error":{"message":"private provider-secret"}}),
        ),
        (
            15,
            json!({"type":"message_delta","delta":{},"usage":{"output_tokens":0}}),
        ),
        (
            15,
            json!({"type":"message_delta","delta":{},"usage":{"output_tokens":2,"cache_read_input_tokens":18446744073709551615u64}}),
        ),
    ] {
        let mut broken = valid.clone();
        broken[index] = replacement;
        cases.push(sse(&broken));
    }
    cases.push(sse(&valid).replacen("event: message_start", "event: ping", 1));
    for (index, wire) in cases.into_iter().enumerate() {
        let first = upstream(200, wire, true).await;
        let backup = upstream(200, sse(&valid), true).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let gateway = runtime(
            config(&[(&first, "anthropic", true), (&backup, "anthropic", true)]),
            &limit,
            Options::default(),
        );
        let response = invoke(&gateway, input(true, true)).await;
        if response.status() == StatusCode::BAD_GATEWAY {
            let output = consume(response).await;
            assert!(!output.contains("private") && !output.contains("secret"));
        } else {
            assert_eq!(response.status(), StatusCode::OK, "case {index}");
            let mut stream = response.into_body().into_data_stream();
            let mut failed = false;
            let mut output = Vec::new();
            while let Some(chunk) = stream.next().await {
                match chunk {
                    Ok(bytes) => output.extend_from_slice(&bytes),
                    Err(_) => {
                        failed = true;
                        break;
                    }
                }
            }
            assert!(failed, "case {index} completed malformed stream");
            let output = String::from_utf8(output).unwrap();
            assert!(!output.contains("event: message_stop") && !output.contains("provider-secret"));
            drop(stream);
        }
        assert_eq!(first.calls.lock().unwrap().len(), 1);
        assert!(backup.calls.lock().unwrap().is_empty());
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn malformed_json_and_overflow_keep_reserved_quota_and_never_retry() {
    for (pointer, invalid) in [
        ("/type", json!("wrong")),
        ("/role", json!("user")),
        ("/content", json!(null)),
        ("/usage/input_tokens", json!(-1)),
        ("/usage/output_tokens", json!(null)),
        ("/usage/cache_creation_input_tokens", json!(1.5)),
        ("/usage/cache_read_input_tokens", json!(u64::MAX)),
        ("/usage/cache_creation", json!(null)),
        (
            "/usage/cache_creation",
            json!({"ephemeral_5m_input_tokens":4}),
        ),
        ("/usage/cache_creation/ephemeral_1h_input_tokens", json!(3)),
        ("/usage/cache_creation/ephemeral_5m_input_tokens", json!(-1)),
    ] {
        let mut body = answer();
        *body.pointer_mut(pointer).unwrap() = invalid;
        let first = upstream(200, body.to_string(), false).await;
        let backup = upstream(200, answer().to_string(), false).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let mut config = config(&[(&first, "anthropic", true), (&backup, "anthropic", true)]);
        config["models"]["public"]["quota"] = json!({"total_tokens":1,"reserve_tokens":1});
        let gateway = runtime(config, &limit, Options::default());
        let response = invoke(&gateway, input(true, false)).await;
        assert_eq!(response.status(), StatusCode::BAD_GATEWAY, "{pointer}");
        let output = consume(response).await;
        assert!(!output.contains("private-model") && !output.contains("opaque-signature"));
        assert_eq!(
            invoke(&gateway, input(false, false)).await.status(),
            StatusCode::TOO_MANY_REQUESTS
        );
        assert_eq!(first.calls.lock().unwrap().len(), 1);
        assert!(backup.calls.lock().unwrap().is_empty());
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn dropping_native_stream_releases_permit_and_keeps_unknown_usage_reservation() {
    let upstream = upstream(200, sse(&frames()), true).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let mut config = config(&[(&upstream, "anthropic", true)]);
    config["models"]["public"]["quota"] = json!({"total_tokens":1,"reserve_tokens":1});
    let gateway = runtime(config, &limit, Options::default());
    let response = invoke(&gateway, input(true, true)).await;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(limit.available(), 0);
    drop(response);
    assert_eq!(limit.available(), 1);
    assert_eq!(
        invoke(&gateway, input(false, false)).await.status(),
        StatusCode::TOO_MANY_REQUESTS
    );
    assert_eq!(upstream.calls.lock().unwrap().len(), 1);
}

#[tokio::test]
async fn portable_requests_fall_back_to_strict_and_charge_failed_attempt_reservations() {
    let failed = upstream(503, "provider-secret unavailable".into(), false).await;
    let mut plain = answer();
    plain["content"] = json!([{"type":"text","text":"Strict fallback"}]);
    plain["stop_reason"] = json!("end_turn");
    plain.as_object_mut().unwrap().remove("vendor_field");
    plain["usage"] = json!({"input_tokens":3,"output_tokens":2});
    let strict = upstream(200, plain.to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let mut config = config(&[(&failed, "anthropic", true), (&strict, "anthropic", false)]);
    config["models"]["public"]["quota"] = json!({"total_tokens":20,"reserve_tokens":5});
    let gateway = runtime(config, &limit, Options::default());
    for _ in 0..2 {
        let response = invoke(&gateway, input(false, false)).await;
        assert_eq!(response.status(), StatusCode::OK);
        let actual: Value = serde_json::from_str(&consume(response).await).unwrap();
        assert_eq!(actual["model"], "public");
        assert_eq!(actual["content"][0]["text"], "Strict fallback");
        assert_eq!(limit.available(), 1);
    }
    assert_eq!(
        invoke(&gateway, input(false, false)).await.status(),
        StatusCode::TOO_MANY_REQUESTS
    );
    assert_eq!(failed.calls.lock().unwrap().len(), 2);
    assert_eq!(strict.calls.lock().unwrap().len(), 2);
}

#[tokio::test]
async fn native_requests_validate_routing_controls_before_dispatch() {
    let upstream = upstream(200, answer().to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway = runtime(
        config(&[(&upstream, "anthropic", true)]),
        &limit,
        Options::default(),
    );
    for (pointer, invalid) in [
        ("/model", json!(7)),
        ("/stream", json!("true")),
        ("/max_tokens", json!(0)),
        ("/max_tokens", json!(-1)),
        ("/messages", json!([])),
        ("/messages/0/role", json!(7)),
        ("/messages/0/role", json!("")),
        ("/messages/0/content", json!(17)),
    ] {
        let mut body = input(true, false);
        *body.pointer_mut(pointer).unwrap() = invalid;
        assert_eq!(
            invoke(&gateway, body).await.status(),
            StatusCode::BAD_REQUEST,
            "{pointer}"
        );
        assert!(upstream.calls.lock().unwrap().is_empty());
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn generated_native_stream_requires_delta_output_usage_and_retains_fallback_quota() {
    for explicit_zero in [false, true] {
        let mut frames = frames();
        for frame in &mut frames {
            if frame["type"] == "message_delta" {
                frame["usage"] = if explicit_zero {
                    json!({"output_tokens":0})
                } else {
                    json!({})
                };
            }
        }
        let upstream = upstream(200, sse(&frames), true).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let mut config = config(&[(&upstream, "anthropic", true)]);
        config["models"]["public"]["quota"] = json!({"total_tokens":32,"reserve_tokens":20});
        let gateway = runtime(config, &limit, Options::default());
        let response = invoke(&gateway, input(true, true)).await;
        assert_eq!(response.status(), StatusCode::OK);
        let result = to_bytes(response.into_body(), 65536).await;
        assert_eq!(
            result.is_ok(),
            explicit_zero,
            "missing output usage must not settle as zero"
        );
        // Explicit zero charges 12 input tokens; failure keeps the 20-token reservation.
        let next = invoke(&gateway, input(true, true)).await;
        assert_eq!(
            next.status(),
            if explicit_zero {
                StatusCode::OK
            } else {
                StatusCode::TOO_MANY_REQUESTS
            }
        );
        drop(next);
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn native_stream_rejects_explicitly_cleared_terminal_stop_reason() {
    let mut frames = frames();
    frames[14]["delta"]["stop_reason"] = json!("end_turn");
    frames[15]["delta"]["stop_reason"] = Value::Null;
    let upstream = upstream(200, sse(&frames), true).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway = runtime(
        config(&[(&upstream, "anthropic", true)]),
        &limit,
        Options::default(),
    );
    let response = invoke(&gateway, input(true, true)).await;
    assert_eq!(response.status(), StatusCode::OK);
    assert!(
        to_bytes(response.into_body(), 65536).await.is_err(),
        "explicit null must not reuse a previous terminal reason"
    );
    assert_eq!(limit.available(), 1);
}

fn strict_cache_answer() -> Value {
    json!({"id":"answer","type":"message","role":"assistant","model":"private-model",
        "content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","stop_sequence":null,
        "usage":{"input_tokens":3,"cache_read_input_tokens":4,"cache_creation_input_tokens":5,"output_tokens":2,
            "cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":3}}})
}

fn strict_cache_frames() -> Vec<Value> {
    let mut message = strict_cache_answer();
    message["content"] = json!([]);
    message["stop_reason"] = Value::Null;
    message["usage"]["output_tokens"] = json!(0);
    vec![
        json!({"type":"message_start","message":message}),
        json!({"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}),
        json!({"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}),
        json!({"type":"content_block_stop","index":0}),
        json!({"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}),
        json!({"type":"message_stop"}),
    ]
}

#[tokio::test]
async fn strict_cache_usage_preserves_ttl_and_settles_total_once_for_json_and_sse() {
    for streaming in [false, true] {
        let wire = if streaming {
            sse(&strict_cache_frames())
        } else {
            strict_cache_answer().to_string()
        };
        let upstream = upstream(200, wire, streaming).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let mut config = config(&[(&upstream, "anthropic", false)]);
        config["models"]["public"]["quota"] = json!({"total_tokens":28,"reserve_tokens":1});
        let resources = SharedResources::default();
        let quotas = resources.quotas.clone();
        let gateway = runtime_with_resources(config, &limit, Options::default(), resources);
        for used in [14, 28] {
            let response = invoke(&gateway, input(false, streaming)).await;
            assert_eq!(response.status(), StatusCode::OK, "streaming={streaming}");
            let output = consume(response).await;
            let usage = if streaming {
                assert_eq!(output.matches("event: message_stop\n").count(), 1);
                output
                    .lines()
                    .filter_map(|line| line.strip_prefix("data: "))
                    .map(|line| serde_json::from_str::<Value>(line).unwrap())
                    .find(|event| event["type"] == "message_delta")
                    .unwrap()["usage"]
                    .clone()
            } else {
                serde_json::from_str::<Value>(&output).unwrap()["usage"].clone()
            };
            assert_eq!(usage, strict_cache_answer()["usage"]);
            let balance = quotas.snapshot("public").unwrap();
            assert_eq!((balance.used, balance.reserved), (used, 0));
            assert_eq!(limit.available(), 1);
        }
        assert_eq!(
            invoke(&gateway, input(false, streaming)).await.status(),
            StatusCode::TOO_MANY_REQUESTS
        );
        assert_eq!(upstream.calls.lock().unwrap().len(), 2);
    }
}

#[tokio::test]
async fn strict_read_only_cache_usage_maps_to_all_client_protocols() {
    for (path, body, expected) in [
        (
            "/v1/messages",
            input(false, false),
            json!({"input_tokens":3,"output_tokens":2,"cache_read_input_tokens":4}),
        ),
        (
            "/v1/chat/completions",
            input(false, false),
            json!({"prompt_tokens":7,"completion_tokens":2,"total_tokens":9,"prompt_tokens_details":{"cached_tokens":4}}),
        ),
        (
            "/v1/responses",
            json!({"model":"public","input":"Hello","max_output_tokens":16}),
            json!({"input_tokens":7,"output_tokens":2,"total_tokens":9,"input_tokens_details":{"cached_tokens":4}}),
        ),
        (
            "/v1beta/models/public:generateContent",
            json!({"contents":[{"role":"user","parts":[{"text":"Hello"}]}],"generationConfig":{"maxOutputTokens":16}}),
            json!({"promptTokenCount":7,"candidatesTokenCount":2,"totalTokenCount":9,"cachedContentTokenCount":4}),
        ),
    ] {
        let mut answer = strict_cache_answer();
        answer["usage"] = json!({"input_tokens":3,"output_tokens":2,"cache_read_input_tokens":4});
        let upstream = upstream(200, answer.to_string(), false).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let mut config = config(&[(&upstream, "anthropic", false)]);
        config["models"]["public"]["quota"] = json!({"total_tokens":9,"reserve_tokens":1});
        let resources = SharedResources::default();
        let quotas = resources.quotas.clone();
        let gateway = runtime_with_resources(config, &limit, Options::default(), resources);
        let mut request = request(body);
        *request.uri_mut() = path.parse().unwrap();
        request.headers_mut().remove("x-api-key");
        request
            .headers_mut()
            .insert("authorization", "Bearer client-secret".parse().unwrap());
        let response = gateway.handle(request, CancellationToken::new()).await;
        assert_eq!(response.status(), StatusCode::OK, "{path}");
        let actual: Value = serde_json::from_str(&consume(response).await).unwrap();
        let usage = if path.contains("generateContent") {
            &actual["usageMetadata"]
        } else {
            &actual["usage"]
        };
        // Other optional fields may be serialized, but these counters must be preserved exactly.
        for (key, value) in expected.as_object().unwrap() {
            if let Some(details) = value.as_object() {
                for (detail, value) in details {
                    assert_eq!(&usage[key][detail], value, "{path}: {key}.{detail}");
                }
            } else {
                assert_eq!(&usage[key], value, "{path}: {key}");
            }
        }
        let balance = quotas.snapshot("public").unwrap();
        assert_eq!((balance.used, balance.reserved), (9, 0));
        assert_eq!(limit.available(), 1);
        assert_eq!(upstream.calls.lock().unwrap().len(), 1);
    }
}

#[tokio::test]
async fn unrepresentable_cache_writes_fail_without_retry_after_charging_known_usage() {
    for (streaming, include_usage) in [(false, false), (true, false), (true, true)] {
        let mut frames = strict_cache_frames();
        // Delay write counters until the final delta so all 14 tokens are known before output fails.
        frames[0]["message"]["usage"] =
            json!({"input_tokens":3,"cache_read_input_tokens":4,"output_tokens":0});
        frames[4]["usage"] = strict_cache_answer()["usage"].clone();
        let wire = if streaming {
            sse(&frames)
        } else {
            strict_cache_answer().to_string()
        };
        let first = upstream(200, wire.clone(), streaming).await;
        let backup = upstream(200, wire, streaming).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let mut config = config(&[(&first, "anthropic", false), (&backup, "anthropic", false)]);
        config["models"]["public"]["quota"] = json!({"total_tokens":14,"reserve_tokens":1});
        let resources = SharedResources::default();
        let quotas = resources.quotas.clone();
        let gateway = runtime_with_resources(config, &limit, Options::default(), resources);
        let mut body = input(false, streaming);
        if streaming {
            body["stream_options"] = json!({"include_usage":include_usage});
        }
        let mut request = request(body);
        *request.uri_mut() = "/v1/chat/completions".parse().unwrap();
        request.headers_mut().remove("x-api-key");
        request
            .headers_mut()
            .insert("authorization", "Bearer client-secret".parse().unwrap());
        let response = gateway.handle(request, CancellationToken::new()).await;
        if streaming {
            assert_eq!(response.status(), StatusCode::OK);
            let mut chunks = response.into_body().into_data_stream();
            let mut output = Vec::new();
            let mut failed = false;
            while let Some(chunk) = chunks.next().await {
                match chunk {
                    Ok(bytes) => output.extend_from_slice(&bytes),
                    Err(_) => {
                        failed = true;
                        break;
                    }
                }
            }
            assert!(failed, "include_usage={include_usage}");
            assert!(!String::from_utf8(output).unwrap().contains("[DONE]"));
            drop(chunks);
        } else {
            assert_eq!(response.status(), StatusCode::BAD_GATEWAY);
            consume(response).await;
        }
        let balance = quotas.snapshot("public").unwrap();
        assert_eq!((balance.used, balance.reserved), (14, 0));
        assert_eq!(
            invoke(&gateway, input(false, false)).await.status(),
            StatusCode::TOO_MANY_REQUESTS
        );
        assert_eq!(first.calls.lock().unwrap().len(), 1);
        assert!(backup.calls.lock().unwrap().is_empty());
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn invalid_cache_updates_terminate_strict_and_native_streams_with_conservative_quota() {
    for native in [false, true] {
        for (index, invalid) in [
            json!({"output_tokens":2,"cache_read_input_tokens":-1}),
            json!({"output_tokens":2,"cache_read_input_tokens":u64::MAX}),
            // Total increases, but one cumulative counter decreases.
            json!({"output_tokens":8,"cache_read_input_tokens":3}),
            json!({"output_tokens":2,"cache_creation":{"ephemeral_5m_input_tokens":2,"ephemeral_1h_input_tokens":4}}),
            // The TTL total remains five, but the one-hour counter decreases.
            json!({"output_tokens":2,"cache_creation":{"ephemeral_5m_input_tokens":3,"ephemeral_1h_input_tokens":2}}),
        ].into_iter().enumerate() {
            let mut frames = strict_cache_frames();
            frames[4]["usage"] = invalid.clone();
            let first = upstream(200, sse(&frames), true).await;
            let backup = upstream(200, sse(&strict_cache_frames()), true).await;
            let limit = ConcurrencyLimit::new(1).unwrap();
            let mut config = config(&[
                (&first, "anthropic", native),
                (&backup, "anthropic", native),
            ]);
            // The first valid frame knows 12 tokens; interruption charges max(known, reserve).
            let (reserve, charged) = if index == 0 { (20, 20) } else { (1, 12) };
            config["models"]["public"]["quota"] = json!({"total_tokens":charged,"reserve_tokens":reserve});
            let resources = SharedResources::default();
            let quotas = resources.quotas.clone();
            let gateway = runtime_with_resources(config, &limit, Options::default(), resources);
            let response = invoke(&gateway, input(false, true)).await;
            assert_eq!(response.status(), StatusCode::OK);
            let mut chunks = response.into_body().into_data_stream();
            let mut failed = false;
            let mut output = Vec::new();
            while let Some(chunk) = chunks.next().await {
                match chunk {
                    Ok(bytes) => output.extend_from_slice(&bytes),
                    Err(_) => {
                        failed = true;
                        break;
                    }
                }
            }
            assert!(failed, "native={native}, usage={invalid}");
            assert!(
                !String::from_utf8(output)
                    .unwrap()
                    .contains("event: message_stop")
            );
            drop(chunks);
            let balance = quotas.snapshot("public").unwrap();
            assert_eq!(
                (balance.used, balance.reserved),
                (charged, 0),
                "native={native}, usage={invalid}"
            );
            assert_eq!(limit.available(), 1);
            assert_eq!(
                invoke(&gateway, input(false, true)).await.status(),
                StatusCode::TOO_MANY_REQUESTS
            );
            assert_eq!(first.calls.lock().unwrap().len(), 1);
            assert!(backup.calls.lock().unwrap().is_empty());
        }
    }
}

// Isolate caching from thinking/vendor extensions so rejection cannot pass for
// an unrelated unsupported field. Each request has at most four breakpoints.
fn cache_inputs(streaming: bool) -> Vec<Value> {
    let mut automatic = input(false, streaming);
    automatic["cache_control"] = json!({"type":"ephemeral","ttl":"1h"});
    let mut placed = input(false, streaming);
    placed["tools"] = json!([{"name":"weather","input_schema":{"type":"object"},"cache_control":{"type":"ephemeral","ttl":"1h"}}]);
    placed["system"] = json!([
        {"type":"text","text":"Stable instructions","cache_control":{"type":"ephemeral","ttl":"1h"}},
        {"type":"text","text":"Uncached suffix"}
    ]);
    placed["messages"] = json!([
        {"role":"user","content":[
            {"type":"text","text":"Stable prefix"},
            {"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="},"cache_control":{"type":"ephemeral","ttl":"5m"}},
            {"type":"text","text":"Uncached question"}]},
        {"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"weather","input":{}}]},
        {"role":"user","content":[
            {"type":"tool_result","tool_use_id":"call-1","content":[{"type":"text","text":"Sunny"}],"cache_control":{"type":"ephemeral"}},
            {"type":"text","text":"Continue"}]}
    ]);
    // A marker on ordinary text independently exercises cache capability,
    // without depending on image or tool support.
    let mut text = input(false, streaming);
    text["messages"][0]["content"] = json!([
        {"type":"text","text":"Stable prefix","cache_control":{"type":"ephemeral","ttl":"1h"}},
        {"type":"text","text":"Uncached suffix"}
    ]);
    vec![automatic, placed, text]
}

#[tokio::test]
async fn cache_controls_keep_ttl_and_placement_across_native_retry() {
    for streaming in [false, true] {
        let first = upstream(503, "unavailable".into(), false).await;
        let incompatible = upstream(200, answer().to_string(), false).await;
        let backup = upstream(
            200,
            if streaming {
                sse(&frames())
            } else {
                answer().to_string()
            },
            streaming,
        )
        .await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        for body in cache_inputs(streaming) {
            let gateway = runtime(
                config(&[
                    (&first, "anthropic", true),
                    (&incompatible, "openai", true),
                    (&backup, "anthropic", true),
                ]),
                &limit,
                Options::default(),
            );
            let before = backup.calls.lock().unwrap().len();
            let response = invoke(&gateway, body.clone()).await;
            assert_eq!(response.status(), StatusCode::OK);
            let output = consume(response).await;
            assert!(output.contains(if streaming { "message_stop" } else { "晴天" }));
            assert_eq!(limit.available(), 1);
            assert!(incompatible.calls.lock().unwrap().is_empty());
            let mut expected = body.clone();
            expected["model"] = json!("private-model");
            assert_eq!(
                first.calls.lock().unwrap().last().unwrap()["body"],
                expected
            );
            assert_eq!(backup.calls.lock().unwrap().len(), before + 1);
            assert_eq!(
                backup.calls.lock().unwrap().last().unwrap()["body"],
                expected
            );
            for kind in ["openai", "gemini"] {
                let strict = runtime(
                    config(&[(&incompatible, kind, kind != "anthropic")]),
                    &limit,
                    Options::default(),
                );
                assert_eq!(
                    invoke(&strict, body.clone()).await.status(),
                    StatusCode::BAD_REQUEST
                );
                assert!(incompatible.calls.lock().unwrap().is_empty());
                assert_eq!(limit.available(), 1);
            }
        }
    }
}

#[tokio::test]
async fn strict_cache_controls_survive_json_sse_and_native_to_strict_retry() {
    for streaming in [false, true] {
        let first = upstream(503, "unavailable".into(), false).await;
        let incompatible = upstream(200, "must not dispatch".into(), false).await;
        let backup = upstream(
            200,
            if streaming {
                sse(&strict_cache_frames())
            } else {
                strict_cache_answer().to_string()
            },
            streaming,
        )
        .await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        for first_native in [false, true] {
            for body in cache_inputs(streaming) {
                let gateway = runtime(
                    config(&[
                        (&first, "anthropic", first_native),
                        (&incompatible, "openai", false),
                        (&incompatible, "gemini", false),
                        (&backup, "anthropic", false),
                    ]),
                    &limit,
                    Options::default(),
                );
                let response = invoke(&gateway, body.clone()).await;
                assert_eq!(response.status(), StatusCode::OK);
                let output = consume(response).await;
                assert!(output.contains(if streaming { "message_stop" } else { "answer" }));
                assert_eq!(limit.available(), 1);
                assert!(incompatible.calls.lock().unwrap().is_empty());
                let mut expected = body.clone();
                expected["model"] = json!("private-model");
                // Existing strict normalization turns the single string into one text block.
                if let Some(text) = expected["messages"][0]["content"].as_str() {
                    let text = text.to_owned();
                    expected["messages"][0]["content"] = json!([{"type":"text","text":text}]);
                }
                assert_eq!(
                    backup.calls.lock().unwrap().last().unwrap()["body"],
                    expected
                );
                if first_native {
                    let mut raw = body;
                    raw["model"] = json!("private-model");
                    assert_eq!(first.calls.lock().unwrap().last().unwrap()["body"], raw);
                } else {
                    assert_eq!(
                        first.calls.lock().unwrap().last().unwrap()["body"],
                        expected
                    );
                }
            }
        }
        assert_eq!(first.calls.lock().unwrap().len(), 6);
        assert_eq!(backup.calls.lock().unwrap().len(), 6);
    }
}

#[tokio::test]
async fn malformed_cache_controls_never_dispatch_and_release_admission() {
    let fixture = upstream(200, strict_cache_answer().to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway = runtime(
        config(&[(&fixture, "anthropic", false)]),
        &limit,
        Options::default(),
    );
    for streaming in [false, true] {
        for control in [
            json!({"type":"ephemeral","ttl":"30m"}),
            json!({"type":"ephemeral","ttl":null}),
            json!({"type":"unknown"}),
        ] {
            for top in [false, true] {
                let mut body = input(false, streaming);
                if top {
                    body["cache_control"] = control.clone();
                } else {
                    body["messages"][0]["content"] =
                        json!([{"type":"text","text":"prefix","cache_control":control}]);
                }
                assert_eq!(
                    invoke(&gateway, body).await.status(),
                    StatusCode::BAD_REQUEST
                );
                assert!(fixture.calls.lock().unwrap().is_empty());
                assert_eq!(limit.available(), 1);
            }
        }
    }
}
