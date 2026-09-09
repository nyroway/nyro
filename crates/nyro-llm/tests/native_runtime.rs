//! Native Chat exercises real HTTP boundaries without round-tripping fixtures through codecs.
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
        .uri("/v1/chat/completions")
        .header("content-type", "application/json")
        .header("authorization", "Bearer client-secret")
        .header("x-private-caller", "caller-metadata")
        .body(Body::from(body.to_string()))
        .unwrap()
}
fn input() -> Value {
    json!({"model":"public","messages":[{"role":"user","content":"Hello"}],"max_tokens":16})
}
fn extended_input() -> Value {
    json!({"model":"public","messages":[
        {"role":"user","content":"Weather?"},
        {"role":"assistant","content":null,"reasoning_content":"Check weather",
         "tool_calls":[{"id":"call-1","type":"function","function":{"name":"weather","arguments":"{}"}}]},
        {"role":"tool","tool_call_id":"call-1","content":"Sunny"}
    ],"max_tokens":16,"thinking":{"type":"enabled"},"vendor_option":{"nested":[true,7,null]}})
}
fn answer() -> Value {
    json!({"id":"answer","object":"chat.completion","created":1,"model":"private-model",
        "request_id":"vendor-request","vendor_field":{"ok":true},
        "choices":[{"index":0,"message":{"role":"assistant","content":"晴天","reasoning_content":"Consider weather"},"finish_reason":"stop"}],
        "usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"vendor_billable_tokens":9}})
}
fn frames() -> Vec<Value> {
    vec![
        json!({"id":"answer","object":"chat.completion.chunk","created":1,"model":"private-model","request_id":"vendor-request",
            "choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"想一想","content":"晴天"},"finish_reason":null}]}),
        json!({"id":"answer","object":"chat.completion.chunk","created":1,"model":"private-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}),
        json!({"id":"answer","object":"chat.completion.chunk","created":1,"model":"private-model","choices":[],"request_id":"usage-request",
            "usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"vendor_billable_tokens":9}}),
    ]
}
fn sse(done: bool) -> String {
    let mut text: String = frames().iter().map(|v| format!("data: {v}\n\n")).collect();
    if done {
        text.push_str("data: [DONE]\n\n");
    }
    text
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
async fn native_json_preserves_request_and_response_fields_without_forwarding_caller_headers() {
    let upstream = upstream(200, answer().to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(
        config(&[(&upstream, "openai", true)]),
        &limit,
        Options::default(),
    );
    let mut unauthorized = request(extended_input());
    unauthorized.headers_mut().remove("authorization");
    assert_eq!(
        runtime
            .handle(unauthorized, CancellationToken::new())
            .await
            .status(),
        StatusCode::UNAUTHORIZED
    );
    assert!(upstream.calls.lock().unwrap().is_empty());
    let response = invoke(&runtime, extended_input()).await;
    assert_eq!(response.status(), StatusCode::OK);
    let actual: Value = serde_json::from_str(&consume(response).await).unwrap();
    let mut expected = answer();
    expected["model"] = json!("public");
    assert_eq!(actual, expected);
    let calls = upstream.calls.lock().unwrap();
    assert_eq!(calls.len(), 1);
    let mut expected = extended_input();
    expected["model"] = json!("private-model");
    assert_eq!(calls[0]["body"], expected);
    assert_eq!(calls[0]["path"], "/v1/chat/completions");
    assert_eq!(
        calls[0]["headers"]["authorization"],
        "Bearer provider-secret"
    );
    assert!(calls[0]["headers"]["x-private-caller"].is_null());
    assert!(!calls[0]["headers"].to_string().contains("client-secret"));
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn native_sse_preserves_extensions_and_charges_usage_even_when_hidden() {
    for include_usage in [false, true] {
        let upstream = upstream(200, sse(true), true).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let mut config = config(&[(&upstream, "openai", true)]);
        config["models"]["public"]["quota"] = json!({"total_tokens":5,"reserve_tokens":1});
        let runtime = runtime(config, &limit, Options::default());
        let mut body = extended_input();
        body["stream"] = json!(true);
        body["stream_options"] = json!({"include_usage":include_usage,"vendor_stream_option":true});
        let response = invoke(&runtime, body.clone()).await;
        assert_eq!(response.status(), StatusCode::OK);
        assert_eq!(limit.available(), 0);
        let output = consume(response).await;
        assert_eq!(output.matches("data: [DONE]").count(), 1);
        let values: Vec<Value> = output
            .lines()
            .filter_map(|line| line.strip_prefix("data: "))
            .filter(|data| *data != "[DONE]")
            .map(|data| serde_json::from_str(data).unwrap())
            .collect();
        let mut expected = frames();
        if !include_usage {
            expected.pop();
        }
        for frame in &mut expected {
            frame["model"] = json!("public");
        }
        assert_eq!(values, expected);
        body["model"] = json!("private-model");
        body["stream_options"]["include_usage"] = json!(true);
        assert_eq!(upstream.calls.lock().unwrap()[0]["body"], body);
        assert_eq!(limit.available(), 1);
        assert_eq!(
            invoke(&runtime, input()).await.status(),
            StatusCode::TOO_MANY_REQUESTS
        );
        assert_eq!(upstream.calls.lock().unwrap().len(), 1);
    }
}

#[tokio::test]
async fn strict_default_and_cross_protocol_backends_reject_original_extensions() {
    for kind in ["openai", "anthropic", "gemini"] {
        let upstream = upstream(200, answer().to_string(), false).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            config(&[(&upstream, kind, false)]),
            &limit,
            Options::default(),
        );
        let response = invoke(&runtime, extended_input()).await;
        assert_eq!(response.status(), StatusCode::BAD_REQUEST, "kind={kind}");
        assert!(upstream.calls.lock().unwrap().is_empty());
        drop(response);
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn native_retry_skips_incompatible_backends_and_sanitizes_upstream_errors() {
    let failing = upstream(
        503,
        "private provider-secret upstream failure".into(),
        false,
    )
    .await;
    let incompatible = upstream(200, answer().to_string(), false).await;
    let backup = upstream(200, answer().to_string(), false).await;
    for kind in ["openai", "anthropic", "gemini"] {
        let limit = ConcurrencyLimit::new(1).unwrap();
        let config = config(&[
            (&failing, "openai", true),
            (&incompatible, kind, false),
            (&backup, "openai", true),
        ]);
        let runtime = runtime(config, &limit, Options::default());
        let response = invoke(&runtime, extended_input()).await;
        assert_eq!(response.status(), StatusCode::OK);
        assert!(consume(response).await.contains("vendor-request"));
        assert!(incompatible.calls.lock().unwrap().is_empty());
        assert_eq!(limit.available(), 1);
    }
    assert_eq!(failing.calls.lock().unwrap().len(), 3);
    assert_eq!(backup.calls.lock().unwrap().len(), 3);
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(
        config(&[(&failing, "openai", true), (&incompatible, "openai", false)]),
        &limit,
        Options::default(),
    );
    let response = invoke(&runtime, extended_input()).await;
    assert_eq!(response.status(), StatusCode::SERVICE_UNAVAILABLE);
    let output = consume(response).await;
    assert!(!output.contains("private") && !output.contains("secret"));
    assert!(incompatible.calls.lock().unwrap().is_empty());
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn malformed_or_truncated_native_streams_never_retry_and_release_admission() {
    for (wire, frame_limit, immediate) in [
        ("data: {broken-json}\n\n".to_owned(), 4096, true),
        (
            "data: {\"choices\":\"wrong\"}\n\ndata: [DONE]\n\n".to_owned(),
            4096,
            true,
        ),
        (sse(true), 32, true),
        (sse(false), 4096, false),
    ] {
        let first = upstream(200, wire, true).await;
        let backup = upstream(200, sse(true), true).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            config(&[(&first, "openai", true), (&backup, "openai", true)]),
            &limit,
            Options {
                max_frame_bytes: frame_limit,
                ..Options::default()
            },
        );
        let mut input = extended_input();
        input["stream"] = json!(true);
        let response = invoke(&runtime, input).await;
        if immediate {
            assert_eq!(response.status(), StatusCode::BAD_GATEWAY);
            consume(response).await;
        } else {
            assert_eq!(response.status(), StatusCode::OK);
            let mut stream = response.into_body().into_data_stream();
            let mut output = Vec::new();
            let mut failed = false;
            while let Some(chunk) = stream.next().await {
                match chunk {
                    Ok(bytes) => output.extend_from_slice(&bytes),
                    Err(_) => {
                        failed = true;
                        break;
                    }
                }
            }
            assert!(
                failed,
                "EOF without [DONE] must fail even after finish_reason"
            );
            assert!(!String::from_utf8(output).unwrap().contains("[DONE]"));
            drop(stream);
        }
        assert_eq!(first.calls.lock().unwrap().len(), 1);
        assert!(backup.calls.lock().unwrap().is_empty());
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn dropping_native_body_releases_admission() {
    let upstream = upstream(200, sse(true), true).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(
        config(&[(&upstream, "openai", true)]),
        &limit,
        Options::default(),
    );
    let mut body = input();
    body["stream"] = json!(true);
    let response = invoke(&runtime, body).await;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(limit.available(), 0);
    drop(response);
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn native_requests_reject_malformed_routing_and_stream_controls_before_dispatch() {
    let upstream = upstream(200, answer().to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(
        config(&[(&upstream, "openai", true)]),
        &limit,
        Options::default(),
    );
    for (pointer, invalid) in [
        ("/model", json!(7)),
        ("/messages", json!([])),
        ("/messages/0", json!({"content":"Missing role"})),
        ("/stream", json!("true")),
        ("/stream_options", json!(false)),
        ("/stream_options", json!({"include_usage":"true"})),
        ("/stream_options", json!({"include_obfuscation":1})),
    ] {
        let mut body = input();
        body["stream"] = json!(false);
        body["stream_options"] = json!({});
        *body.pointer_mut(pointer).unwrap() = invalid;
        let response = invoke(&runtime, body).await;
        assert_eq!(
            response.status(),
            StatusCode::BAD_REQUEST,
            "pointer={pointer}"
        );
        drop(response);
        assert!(upstream.calls.lock().unwrap().is_empty());
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn native_json_rejects_malformed_envelopes_without_retry_or_refunding_unknown_usage() {
    for (pointer, invalid) in [
        ("/object", json!("wrong")),
        ("/choices", json!([])),
        ("/choices/0/message", json!("wrong")),
        ("/choices/0/message", json!({})),
        ("/choices/0/message/role", json!("user")),
        ("/choices/0/message/content", json!(17)),
        ("/usage/prompt_tokens", json!(-1)),
        ("/usage/completion_tokens", Value::Null),
        ("/usage/total_tokens", json!(1.5)),
    ] {
        let mut invalid_answer = answer();
        *invalid_answer.pointer_mut(pointer).unwrap() = invalid;
        let first = upstream(200, invalid_answer.to_string(), false).await;
        let backup = upstream(200, answer().to_string(), false).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let mut config = config(&[(&first, "openai", true), (&backup, "openai", true)]);
        config["models"]["public"]["quota"] = json!({"total_tokens":1,"reserve_tokens":1});
        let runtime = runtime(config, &limit, Options::default());
        let response = invoke(&runtime, extended_input()).await;
        assert_eq!(
            response.status(),
            StatusCode::BAD_GATEWAY,
            "pointer={pointer}"
        );
        let output = consume(response).await;
        assert!(!output.contains("private-model") && !output.contains("vendor-request"));
        assert_eq!(first.calls.lock().unwrap().len(), 1);
        assert!(backup.calls.lock().unwrap().is_empty());
        assert_eq!(limit.available(), 1);
        assert_eq!(
            invoke(&runtime, input()).await.status(),
            StatusCode::TOO_MANY_REQUESTS
        );
        assert_eq!(first.calls.lock().unwrap().len(), 1);
    }
}

#[tokio::test]
async fn portable_requests_retry_native_to_strict_but_disabled_native_cannot_enable_extensions() {
    let native = upstream(503, "unavailable".into(), false).await;
    let strict = upstream(200, json!({"id":"strict-answer","object":"chat.completion","created":1,"model":"private-model",
        "choices":[{"index":0,"message":{"role":"assistant","content":"Strict fallback"},"finish_reason":"stop"}],
        "usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}).to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let configuration = config(&[(&native, "openai", true), (&strict, "openai", false)]);
    let gateway = runtime(configuration.clone(), &limit, Options::default());
    let response = invoke(&gateway, input()).await;
    assert_eq!(response.status(), StatusCode::OK);
    let output: Value = serde_json::from_str(&consume(response).await).unwrap();
    assert_eq!(
        output["choices"][0]["message"]["content"],
        "Strict fallback"
    );
    assert_eq!(output["model"], "public");
    assert_eq!(native.calls.lock().unwrap().len(), 1);
    assert_eq!(strict.calls.lock().unwrap().len(), 1);
    let mut disabled = configuration;
    disabled["models"]["public"]["backends"][0]["weight"] = json!(0);
    let gateway = runtime(disabled, &limit, Options::default());
    let response = invoke(&gateway, extended_input()).await;
    assert_eq!(response.status(), StatusCode::BAD_REQUEST);
    drop(response);
    assert_eq!(native.calls.lock().unwrap().len(), 1);
    assert_eq!(strict.calls.lock().unwrap().len(), 1);
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn native_stream_accepts_whitespace_around_done_but_rejects_invalid_delta_fields() {
    for invalid_delta in [None, Some(json!({"role":7})), Some(json!({"content":17}))] {
        let wire = if let Some(delta) = &invalid_delta {
            let mut frame = frames().remove(0);
            frame["choices"][0]["delta"] = delta.clone();
            format!("data: {frame}\n\ndata: [DONE]\n\n")
        } else {
            sse(false) + "data:  [DONE] \n\n"
        };
        let upstream = upstream(200, wire, true).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            config(&[(&upstream, "openai", true)]),
            &limit,
            Options::default(),
        );
        let mut body = input();
        body["stream"] = json!(true);
        let response = invoke(&runtime, body).await;
        assert_eq!(
            response.status(),
            if invalid_delta.is_some() {
                StatusCode::BAD_GATEWAY
            } else {
                StatusCode::OK
            }
        );
        let output = consume(response).await;
        if invalid_delta.is_none() {
            assert!(output.ends_with("data: [DONE]\n\n"));
        }
        assert_eq!(limit.available(), 1);
    }
}
