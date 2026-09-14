//! Stateless Responses native fidelity and lifecycle over real HTTP.
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
async fn spawn_upstream(status: u16, text: String, streaming: bool) -> Upstream {
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
            if *kind == "openai" {
                provider["api"] = json!("responses");
            }
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
        .uri("/v1/responses")
        .header("content-type", "application/json")
        .header("authorization", "Bearer client-secret")
        .header("anthropic-beta", "caller-beta")
        .header("anthropic-version", "2099-01-01")
        .header("x-private-caller", "caller-metadata")
        .body(Body::from(body.to_string()))
        .unwrap()
}
fn input(extended: bool, stream: bool) -> Value {
    let mut body = json!({"model":"public","input":"Hello","stream":stream});
    if extended {
        body["prompt_cache_options"] = json!({"mode":"explicit","ttl":"30m"});
        body["input"] = json!([
            {"role":"user","content":[{"type":"input_text","text":"Describe","prompt_cache_breakpoint":{"mode":"explicit"}},{"type":"input_image","image_url":"https://example.com/image.png"}]},
            {"type":"reasoning","id":"rs_history","summary":[],"encrypted_content":"opaque-history"},
            {"type":"message","id":"msg_history","role":"assistant","phase":"commentary","content":[{"type":"output_text","text":"Checking","annotations":[]}]},
            {"type":"function_call","call_id":"call_1","name":"weather","arguments":"{}"},
            {"type":"function_call_output","call_id":"call_1","output":"Sunny"}]);
        body["include"] = json!(["reasoning.encrypted_content"]);
        body["reasoning"] = json!({"effort":"high","summary":"auto"});
        body["tools"] = json!([{"type":"function","name":"weather","parameters":{"type":"object"},"strict":true}]);
        body["vendor_option"] = json!({"nested":[true,7,null]});
    }
    body
}
fn answer() -> Value {
    json!({"id":"resp_answer","object":"response","model":"private-model","created_at":100,"status":"completed","error":null,"incomplete_details":null,
        "output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"想一想"}],"encrypted_content":"opaque-answer"},
            {"type":"message","id":"msg_1","role":"assistant","status":"completed","phase":"final_answer","content":[{"type":"output_text","text":"晴天","annotations":[{"type":"url_citation","url":"https://example.com"}],"logprobs":[]}]}],
        "usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15,"input_tokens_details":{"cached_tokens":6,"cache_write_tokens":4},"output_tokens_details":{"reasoning_tokens":3},"vendor_tokens":99},"vendor_field":{"preserve":true}})
}
fn frames() -> Vec<Value> {
    let mut start = answer();
    start["status"] = json!("in_progress");
    start["output"] = json!([]);
    start["usage"] = Value::Null;
    vec![
        json!({"type":"response.created","sequence_number":0,"response":start}),
        json!({"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}),
        json!({"type":"response.reasoning_summary_text.delta","sequence_number":2,"item_id":"rs_1","output_index":0,"summary_index":0,"delta":"想一想","obfuscation":"opaque"}),
        json!({"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":answer()["output"][0]}),
        json!({"type":"response.vendor_extension","sequence_number":4,"opaque":[1,true,null]}),
        json!({"type":"response.completed","sequence_number":5,"response":answer()}),
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
async fn native_json_preserves_history_and_response_extensions_with_isolated_auth() {
    let upstream = spawn_upstream(200, answer().to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let mut cfg = config(&[(&upstream, "openai", true)]);
    cfg["models"]["public"]["quota"] = json!({"total_tokens":15,"reserve_tokens":1});
    let gateway = runtime(cfg, &limit, Options::default());
    let mut unauthenticated = request(input(true, false));
    unauthenticated.headers_mut().remove("authorization");
    assert_eq!(
        gateway
            .handle(unauthenticated, CancellationToken::new())
            .await
            .status(),
        StatusCode::UNAUTHORIZED
    );
    assert!(upstream.calls.lock().unwrap().is_empty());
    let response = invoke(&gateway, input(true, false)).await;
    assert_eq!(response.status(), StatusCode::OK);
    let mut expected = answer();
    expected["model"] = json!("public");
    assert_eq!(
        serde_json::from_str::<Value>(&consume(response).await).unwrap(),
        expected
    );
    {
        let calls = upstream.calls.lock().unwrap();
        let mut expected = input(true, false);
        expected["model"] = json!("private-model");
        expected["store"] = json!(false);
        assert_eq!(calls[0]["body"], expected);
        assert_eq!(calls[0]["path"], "/v1/responses");
        assert_eq!(
            calls[0]["headers"]["authorization"],
            "Bearer provider-secret"
        );
        for name in [
            "x-api-key",
            "anthropic-beta",
            "anthropic-version",
            "x-private-caller",
        ] {
            assert!(calls[0]["headers"][name].is_null());
        }
    }
    assert_eq!(limit.available(), 1);
    assert_eq!(
        invoke(&gateway, input(false, false)).await.status(),
        StatusCode::TOO_MANY_REQUESTS
    );
    assert_eq!(upstream.calls.lock().unwrap().len(), 1);
}
#[tokio::test]
async fn native_stream_preserves_events_and_settles_terminal_usage_without_double_counting_details()
{
    let upstream = spawn_upstream(200, sse(&frames()), true).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let mut cfg = config(&[(&upstream, "openai", true)]);
    cfg["models"]["public"]["quota"] = json!({"total_tokens":30,"reserve_tokens":1});
    let gateway = runtime(cfg, &limit, Options::default());
    for _ in 0..2 {
        let response = invoke(&gateway, input(true, true)).await;
        assert_eq!(response.status(), StatusCode::OK);
        assert_eq!(limit.available(), 0);
        let output = consume(response).await;
        let mut expected = frames();
        expected[0]["response"]["model"] = json!("public");
        expected[5]["response"]["model"] = json!("public");
        assert_eq!(output, sse(&expected));
        assert!(!output.contains("[DONE]"));
        assert_eq!(limit.available(), 1);
    }
    assert_eq!(
        invoke(&gateway, input(false, false)).await.status(),
        StatusCode::TOO_MANY_REQUESTS
    );
}

#[tokio::test]
async fn native_request_rejects_hosted_state_and_invalid_controls_before_dispatch() {
    let upstream = spawn_upstream(200, answer().to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway = runtime(
        config(&[(&upstream, "openai", true)]),
        &limit,
        Options::default(),
    );
    for (field, value) in [
        ("store", json!(true)),
        ("store", json!("false")),
        ("background", json!(true)),
        ("previous_response_id", json!("resp_other")),
        ("conversation", json!({"id":"conv_other"})),
        ("stream", json!(1)),
        ("max_output_tokens", json!(0)),
        ("max_output_tokens", json!(-1)),
        ("input", json!(false)),
        ("input", json!([{}])),
        (
            "input",
            json!([{"type":"item_reference","id":"item_other"}]),
        ),
        ("tools", json!([{"type":"web_search"}])),
        ("tool_choice", json!({"type":"web_search"})),
        ("stream_options", json!({"include_obfuscation":true})),
        ("include", json!("reasoning.encrypted_content")),
    ] {
        let mut body = input(false, false);
        body[field] = value;
        let response = invoke(&gateway, body.clone()).await;
        assert_eq!(response.status(), StatusCode::BAD_REQUEST, "{body}");
    }
    assert!(upstream.calls.lock().unwrap().is_empty());
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn strict_and_other_protocol_candidates_cannot_consume_native_extensions() {
    for kind in ["openai", "anthropic", "gemini"] {
        let upstream = spawn_upstream(200, answer().to_string(), false).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let gateway = runtime(
            config(&[(&upstream, kind, kind != "openai")]),
            &limit,
            Options::default(),
        );
        assert_eq!(
            invoke(&gateway, input(true, false)).await.status(),
            StatusCode::BAD_REQUEST
        );
        assert!(upstream.calls.lock().unwrap().is_empty());
    }
    let first = spawn_upstream(503, "private provider-secret".into(), false).await;
    let strict = spawn_upstream(200, answer().to_string(), false).await;
    let other = spawn_upstream(200, answer().to_string(), false).await;
    let last = spawn_upstream(200, answer().to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway = runtime(
        config(&[
            (&first, "openai", true),
            (&strict, "openai", false),
            (&other, "anthropic", true),
            (&last, "openai", true),
        ]),
        &limit,
        Options::default(),
    );
    assert_eq!(
        invoke(&gateway, input(true, false)).await.status(),
        StatusCode::OK
    );
    assert_eq!(first.calls.lock().unwrap().len(), 1);
    assert!(strict.calls.lock().unwrap().is_empty());
    assert!(other.calls.lock().unwrap().is_empty());
    assert_eq!(last.calls.lock().unwrap().len(), 1);
}

#[tokio::test]
async fn native_inputs_can_fall_back_through_the_original_strict_responses_codec() {
    let first = spawn_upstream(503, String::new(), false).await;
    let second = spawn_upstream(200,json!({"id":"chat","object":"chat.completion","created":100,"model":"private-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}]}).to_string(),false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let mut cfg = config(&[(&first, "openai", true), (&second, "openai", true)]);
    cfg["providers"]["p1"]["api"] = json!("chat_completions");
    let gateway = runtime(cfg, &limit, Options::default());
    let response = invoke(&gateway, input(false, false)).await;
    assert_eq!(response.status(), StatusCode::OK);
    let actual: Value = serde_json::from_str(&consume(response).await).unwrap();
    assert_eq!(actual["object"], "response");
    assert_eq!(actual["model"], "public");
    let calls = second.calls.lock().unwrap();
    assert_eq!(calls[0]["path"], "/v1/chat/completions");
    assert_eq!(calls[0]["body"]["messages"][0]["content"], "Hello");
    assert_eq!(calls[0]["body"]["store"], false);
}

#[tokio::test]
async fn completed_and_incomplete_responses_allow_missing_usage_with_reservation_fallback() {
    for streaming in [false, true] {
        for status in ["completed", "incomplete"] {
            let mut end = answer();
            end["status"] = json!(status);
            end["usage"] = Value::Null;
            if status == "incomplete" {
                end["incomplete_details"] = json!({"reason":"max_output_tokens"});
            }
            let body = if streaming {
                let mut events = frames();
                events[5]["type"] = json!(format!("response.{status}"));
                events[5]["response"] = end;
                sse(&events)
            } else {
                end.to_string()
            };
            let upstream = spawn_upstream(200, body, streaming).await;
            let limit = ConcurrencyLimit::new(1).unwrap();
            let mut cfg = config(&[(&upstream, "openai", true)]);
            cfg["models"]["public"]["quota"] = json!({"total_tokens":4,"reserve_tokens":4});
            let gateway = runtime(cfg, &limit, Options::default());
            let response = invoke(&gateway, input(true, streaming)).await;
            assert_eq!(response.status(), StatusCode::OK);
            let text = consume(response).await;
            assert!(text.contains(status));
            assert_eq!(limit.available(), 1);
            assert_eq!(
                invoke(&gateway, input(false, false)).await.status(),
                StatusCode::TOO_MANY_REQUESTS
            );
        }
    }
}

#[tokio::test]
async fn invalid_json_envelopes_and_usage_fail_without_retry_or_secret_leaks() {
    let mut cases = Vec::new();
    for (field, value) in [
        ("id", json!("")),
        ("object", json!("chat.completion")),
        ("status", json!("failed")),
        ("status", json!("in_progress")),
        ("status", json!("incomplete")),
        ("output", json!({})),
        ("output", json!([null])),
        ("error", json!({"message":"private provider-secret"})),
        ("usage", json!({"input_tokens":10,"output_tokens":5})),
        (
            "usage",
            json!({"input_tokens":u64::MAX,"output_tokens":1,"total_tokens":0}),
        ),
    ] {
        let mut bad = answer();
        bad[field] = value;
        cases.push(bad);
    }
    for (field, value) in [
        ("input_tokens", json!(-1)),
        ("total_tokens", json!(16)),
        ("output_tokens", Value::Null),
        ("input_tokens_details", json!({"cached_tokens":11})),
        ("output_tokens_details", json!({"reasoning_tokens":6})),
        ("input_tokens_details", json!({"cached_tokens":-1})),
        (
            "input_tokens_details",
            json!({"cached_tokens":6,"cache_write_tokens":5}),
        ),
        (
            "input_tokens_details",
            json!({"cached_tokens":6,"cache_write_tokens":-1}),
        ),
        (
            "input_tokens_details",
            json!({"cached_tokens":6,"cache_write_tokens":null}),
        ),
        (
            "input_tokens_details",
            json!({"cached_tokens":6,"cache_write_tokens":18446744073709551615u64}),
        ),
    ] {
        let mut bad = answer();
        bad["usage"][field] = value;
        cases.push(bad);
    }
    for bad in cases {
        let upstream = spawn_upstream(200, bad.to_string(), false).await;
        let fallback = spawn_upstream(200, answer().to_string(), false).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let gateway = runtime(
            config(&[(&upstream, "openai", true), (&fallback, "openai", true)]),
            &limit,
            Options::default(),
        );
        let response = invoke(&gateway, input(true, false)).await;
        assert_eq!(response.status(), StatusCode::BAD_GATEWAY, "{bad}");
        let text = consume(response).await;
        assert!(!text.contains("provider-secret"));
        assert_eq!(upstream.calls.lock().unwrap().len(), 1);
        assert!(fallback.calls.lock().unwrap().is_empty());
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn invalid_sse_identity_sequence_terminal_and_truncation_fail_and_release() {
    let mut cases = Vec::new();
    let valid = frames();
    cases.push(sse(&valid[1..]));
    cases.push(sse(&valid[..5]));
    cases.push(sse(&valid).trim_end().to_string());
    for (index, field, value) in [
        (1, "sequence_number", json!(0)),
        (1, "sequence_number", json!(9)),
        (1, "sequence_number", Value::Null),
        (1, "type", json!("error")),
        (5, "type", json!("response.failed")),
        (5, "type", json!("response.incomplete")),
    ] {
        let mut events = valid.clone();
        events[index][field] = value;
        cases.push(sse(&events));
    }
    for (field, value) in [
        ("id", json!("other")),
        ("model", json!("other")),
        ("created_at", json!(101)),
        ("status", json!("in_progress")),
        ("usage", json!({"input_tokens":10,"output_tokens":5})),
    ] {
        let mut events = valid.clone();
        events[5]["response"][field] = value;
        cases.push(sse(&events));
    }
    for written in [json!(-1), Value::Null, json!(5), json!(u64::MAX)] {
        let mut events = valid.clone();
        events[5]["response"]["usage"]["input_tokens_details"]["cache_write_tokens"] = written;
        cases.push(sse(&events));
    }
    let mut duplicate = valid.clone();
    let mut last = duplicate[5].clone();
    last["sequence_number"] = json!(6);
    duplicate.push(last);
    cases.push(sse(&duplicate));
    cases.push(sse(&valid).replacen("event: response.created", "event: response.completed", 1));
    for body in cases {
        let upstream = spawn_upstream(200, body.clone(), true).await;
        let fallback = spawn_upstream(200, sse(&valid), true).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let gateway = runtime(
            config(&[(&upstream, "openai", true), (&fallback, "openai", true)]),
            &limit,
            Options::default(),
        );
        let response = invoke(&gateway, input(true, true)).await;
        if response.status() == StatusCode::OK {
            assert!(
                to_bytes(response.into_body(), 65536).await.is_err(),
                "{body}"
            );
        } else {
            assert_eq!(response.status(), StatusCode::BAD_GATEWAY, "{body}");
            assert!(!consume(response).await.contains("provider-secret"));
        }
        assert!(fallback.calls.lock().unwrap().is_empty());
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn dropped_stream_releases_admission_and_preserves_fallback_charge() {
    let upstream = spawn_upstream(200, sse(&frames()), true).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let mut cfg = config(&[(&upstream, "openai", true)]);
    cfg["models"]["public"]["quota"] = json!({"total_tokens":4,"reserve_tokens":4});
    let gateway = runtime(cfg, &limit, Options::default());
    let response = invoke(&gateway, input(true, true)).await;
    let mut stream = response.into_body().into_data_stream();
    assert!(stream.next().await.unwrap().is_ok());
    assert_eq!(limit.available(), 0);
    drop(stream);
    assert_eq!(limit.available(), 1);
    assert_eq!(
        invoke(&gateway, input(false, false)).await.status(),
        StatusCode::TOO_MANY_REQUESTS
    );
}

#[tokio::test]
async fn instructions_only_input_remains_compatible_with_strict_and_native_modes() {
    let body = json!({"model":"public","instructions":"Say hello","input":[]});
    assert!(nyro_llm::codec::openai::responses::decode_chat(body.clone()).is_ok());
    let answer = json!({"id":"resp_answer","object":"response","created_at":100,"model":"private-model","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello","annotations":[]}]}]});
    for native in [false, true] {
        let upstream = spawn_upstream(200, answer.to_string(), false).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let gateway = runtime(
            config(&[(&upstream, "openai", native)]),
            &limit,
            Options::default(),
        );
        let response = invoke(&gateway, body.clone()).await;
        assert_eq!(response.status(), StatusCode::OK, "native={native}");
        consume(response).await;
        if native {
            let mut expected = body.clone();
            expected["model"] = json!("private-model");
            expected["store"] = json!(false);
            assert_eq!(upstream.calls.lock().unwrap()[0]["body"], expected);
        }
    }
}

#[tokio::test]
async fn early_usage_is_not_settled_as_final_when_terminal_usage_is_missing() {
    let mut events = frames();
    events[0]["response"]["usage"] = json!({"input_tokens":1,"output_tokens":0,"total_tokens":1});
    events[5]["response"]["usage"] = Value::Null;
    let upstream = spawn_upstream(200, sse(&events), true).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let mut cfg = config(&[(&upstream, "openai", true)]);
    cfg["models"]["public"]["quota"] = json!({"total_tokens":4,"reserve_tokens":4});
    let gateway = runtime(cfg, &limit, Options::default());
    consume(invoke(&gateway, input(true, true)).await).await;
    assert_eq!(
        invoke(&gateway, input(false, false)).await.status(),
        StatusCode::TOO_MANY_REQUESTS
    );
    assert_eq!(limit.available(), 1);
}

fn strict_reasoning_input(streaming: bool) -> Value {
    json!({"model":"public","stream":streaming,"reasoning":{"effort":"high","summary":"auto","context":"all_turns"},"include":["reasoning.encrypted_content"],"input":[{"role":"user","content":"Hi"},{"type":"reasoning","id":"rs_history","summary":[],"encrypted_content":"opaque-history"},{"role":"user","content":"Continue"}]})
}
fn strict_reasoning_answer() -> Value {
    json!({"id":"resp_answer","object":"response","model":"private-model","created_at":100,"status":"completed","output":[{"type":"reasoning","id":"rs_original","summary":[{"type":"summary_text","text":"摘要"}],"encrypted_content":"opaque-final"}],"usage":{"input_tokens":3,"output_tokens":7,"total_tokens":10,"output_tokens_details":{"reasoning_tokens":5}}})
}
fn strict_reasoning_frames() -> Vec<Value> {
    let mut start = strict_reasoning_answer();
    start["status"] = json!("in_progress");
    start["output"] = json!([]);
    start["usage"] = Value::Null;
    let mut frames = vec![
        json!({"type":"response.created","response":start}),
        json!({"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_original","summary":[],"encrypted_content":"opaque-partial"}}),
        json!({"type":"response.reasoning_summary_part.added","output_index":0,"item_id":"rs_original","summary_index":0,"part":{"type":"summary_text","text":""}}),
        json!({"type":"response.reasoning_summary_text.delta","output_index":0,"item_id":"rs_original","summary_index":0,"delta":"摘要"}),
        json!({"type":"response.reasoning_summary_text.done","output_index":0,"item_id":"rs_original","summary_index":0,"text":"摘要"}),
        json!({"type":"response.reasoning_summary_part.done","output_index":0,"item_id":"rs_original","summary_index":0,"part":{"type":"summary_text","text":"摘要"}}),
        json!({"type":"response.output_item.done","output_index":0,"item":strict_reasoning_answer()["output"][0]}),
        json!({"type":"response.completed","response":strict_reasoning_answer()}),
    ];
    for (i, frame) in frames.iter_mut().enumerate() {
        frame["sequence_number"] = json!(i);
    }
    frames
}
#[tokio::test]
async fn strict_reasoning_retry_history_final_ciphertext_and_usage() {
    for native in [false, true] {
        for streaming in [false, true] {
            let failing = spawn_upstream(503, "unavailable".into(), false).await;
            let upstream = spawn_upstream(
                200,
                if streaming {
                    sse(&strict_reasoning_frames())
                } else {
                    strict_reasoning_answer().to_string()
                },
                streaming,
            )
            .await;
            let limit = ConcurrencyLimit::new(1).unwrap();
            let mut cfg = config(&[(&failing, "openai", native), (&upstream, "openai", native)]);
            cfg["models"]["public"]["quota"] = json!({"total_tokens":14,"reserve_tokens":5});
            let gateway = runtime(cfg, &limit, Options::default());
            let body = strict_reasoning_input(streaming);
            let response = invoke(&gateway, body.clone()).await;
            assert_eq!(
                response.status(),
                StatusCode::OK,
                "native={native} stream={streaming}"
            );
            let result = consume(response).await;
            if streaming {
                let mut framing = nyro_protocol::framing::Decoder::new(65536);
                let frames = framing.push(result.as_bytes()).unwrap();
                let terminal: Value = serde_json::from_str(&frames.last().unwrap().data).unwrap();
                assert_eq!(
                    terminal["response"]["output"][0],
                    strict_reasoning_answer()["output"][0]
                );
                assert_eq!(terminal["response"]["model"], "public");
            } else {
                let v: Value = serde_json::from_str(&result).unwrap();
                assert_eq!(v["output"][0], strict_reasoning_answer()["output"][0]);
                assert_eq!(v["model"], "public");
            }
            assert_eq!(limit.available(), 1);
            assert_eq!(failing.calls.lock().unwrap().len(), 1);
            let sent = upstream.calls.lock().unwrap()[0]["body"].clone();
            assert_eq!(sent["reasoning"], body["reasoning"]);
            assert_eq!(sent["input"][1], body["input"][1]);
            assert_eq!(sent["model"], "private-model");
            assert_eq!(sent["store"], false);
            assert_eq!(
                invoke(&gateway, body).await.status(),
                StatusCode::TOO_MANY_REQUESTS
            );
            assert_eq!(upstream.calls.lock().unwrap().len(), 1);
        }
    }
}
#[tokio::test]
async fn invalid_reasoning_config_never_dispatches() {
    let upstream = spawn_upstream(200, strict_reasoning_answer().to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway = runtime(
        config(&[(&upstream, "openai", false)]),
        &limit,
        Options::default(),
    );
    for config in [
        json!({"effort":"invalid"}),
        json!({"context":true}),
        json!({"summary":"none"}),
    ] {
        let mut body = strict_reasoning_input(false);
        body["reasoning"] = config;
        assert_eq!(
            invoke(&gateway, body).await.status(),
            StatusCode::BAD_REQUEST
        );
    }
    assert!(upstream.calls.lock().unwrap().is_empty());
    assert_eq!(limit.available(), 1);
}
#[tokio::test]
async fn malformed_strict_reasoning_stream_never_retries_after_delivery() {
    let mut frames = strict_reasoning_frames();
    frames[4]["text"] = json!("conflict");
    let upstream = spawn_upstream(200, sse(&frames), true).await;
    let fallback = spawn_upstream(200, sse(&strict_reasoning_frames()), true).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway = runtime(
        config(&[(&upstream, "openai", false), (&fallback, "openai", false)]),
        &limit,
        Options::default(),
    );
    let response = invoke(&gateway, strict_reasoning_input(true)).await;
    assert_eq!(response.status(), StatusCode::OK);
    assert!(to_bytes(response.into_body(), 65536).await.is_err());
    assert!(fallback.calls.lock().unwrap().is_empty());
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn effort_only_retry_skips_gemini_and_reaches_chat_without_losing_controls() {
    for native in [false, true] {
        let first = spawn_upstream(503, String::new(), false).await;
        let other = spawn_upstream(200, String::new(), false).await;
        let last=spawn_upstream(200,json!({"id":"r","object":"chat.completion","created":1,"model":"private-model","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5,"completion_tokens_details":{"reasoning_tokens":2}}}).to_string(),false).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let mut cfg = config(&[
            (&first, "openai", native),
            (&other, "gemini", false),
            (&last, "openai", native),
        ]);
        cfg["providers"]["p2"]["api"] = json!("chat_completions");
        cfg["models"]["public"]["quota"] = json!({"total_tokens":5,"reserve_tokens":1});
        let gateway = runtime(cfg, &limit, Options::default());
        let mut body = input(false, false);
        body["reasoning"] = json!({"effort":"high"});
        let response = invoke(&gateway, body.clone()).await;
        assert_eq!(response.status(), StatusCode::OK, "native={native}");
        let result: Value = serde_json::from_str(&consume(response).await).unwrap();
        assert_eq!(result["model"], "public");
        assert_eq!(result["usage"]["total_tokens"], 5);
        assert_eq!(
            result["usage"]["output_tokens_details"]["reasoning_tokens"],
            2
        );
        assert_eq!(first.calls.lock().unwrap().len(), 1);
        assert!(other.calls.lock().unwrap().is_empty());
        let sent = last.calls.lock().unwrap()[0].clone();
        assert_eq!(sent["path"], "/v1/chat/completions");
        assert_eq!(sent["body"]["reasoning_effort"], "high");
        assert_eq!(sent["body"]["store"], false);
        assert_eq!(sent["body"]["model"], "private-model");
        assert_eq!(
            invoke(&gateway, body).await.status(),
            StatusCode::TOO_MANY_REQUESTS
        );
        assert_eq!(last.calls.lock().unwrap().len(), 1);
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn effort_mapping_never_drops_unrepresentable_reasoning_output_or_known_usage() {
    for streaming in [false, true] {
        let answer = strict_reasoning_answer();
        let payload = if streaming {
            sse(&[json!({"type":"response.completed","sequence_number":0,"response":answer})])
        } else {
            answer.to_string()
        };
        let up = spawn_upstream(200, payload, streaming).await;
        let fallback = spawn_upstream(200, String::new(), false).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let mut cfg = config(&[(&up, "openai", false), (&fallback, "openai", false)]);
        cfg["models"]["public"]["quota"] = json!({"total_tokens":10,"reserve_tokens":1});
        let gateway = runtime(cfg, &limit, Options::default());
        let make = || {
            let mut req = request(
                json!({"model":"public","messages":[{"role":"user","content":"Hi"}],"reasoning_effort":"high","stream":streaming}),
            );
            *req.uri_mut() = "/v1/chat/completions".parse().unwrap();
            req
        };
        let response = gateway.handle(make(), CancellationToken::new()).await;
        if streaming && response.status() == StatusCode::OK {
            assert!(to_bytes(response.into_body(), 65536).await.is_err());
        } else {
            assert_eq!(response.status(), StatusCode::BAD_GATEWAY);
            assert!(!consume(response).await.contains("opaque-final"));
        }
        assert!(fallback.calls.lock().unwrap().is_empty());
        assert_eq!(limit.available(), 1);
        assert_eq!(
            gateway
                .handle(make(), CancellationToken::new())
                .await
                .status(),
            StatusCode::TOO_MANY_REQUESTS
        );
        assert_eq!(up.calls.lock().unwrap().len(), 1);
    }
}
