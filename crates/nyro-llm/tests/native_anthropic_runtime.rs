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
    runtime::{Options, Runtime},
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
    let config: Config = serde_json::from_value(config).unwrap();
    Runtime::new(
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
            "usage":{"input_tokens":3,"cache_creation_input_tokens":4,"cache_read_input_tokens":5,"output_tokens":0,"vendor_billable_tokens":99}},"vendor_trace":"trace"}),
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
