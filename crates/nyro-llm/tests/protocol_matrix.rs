//! Local HTTP matrix: independent native wire fixtures exercise both sides of the IR.
use axum::{
    Router,
    body::{Body, to_bytes},
    http::{Request, StatusCode},
    routing::post,
};
use futures::StreamExt;
use nyro_limit::ConcurrencyLimit;
use nyro_llm::{
    config::{Config, ProviderKind},
    runtime::{Options, Runtime},
};
use nyro_security::{ApiKey, ApiKeys};
use serde_json::{Value, json};
use std::{
    collections::BTreeMap,
    sync::{Arc, Mutex},
    time::Duration,
};
use tokio_util::sync::CancellationToken;

const FORMATS: [&str; 3] = ["openai", "anthropic", "gemini"];

fn response(format: &str) -> Value {
    match format {
        "openai" => {
            json!({"id":"answer","object":"chat.completion","created":1,"model":"internal","choices":[{"index":0,"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}})
        }
        "anthropic" => {
            json!({"id":"answer","type":"message","role":"assistant","model":"internal","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":2}})
        }
        _ => {
            json!({"responseId":"answer","modelVersion":"internal","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}})
        }
    }
}

fn frames(format: &str, truncated: bool) -> String {
    let (first, ending) = match format {
        "openai" => (format!("data: {}\n\n", json!({"id":"answer","object":"chat.completion.chunk","created":1,"model":"internal","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"},"finish_reason":null}]})),
            format!("data: {}\n\ndata: [DONE]\n\n",json!({"id":"answer","object":"chat.completion.chunk","created":1,"model":"internal","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}))),
        "anthropic" => (concat!(
            "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"answer\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"internal\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":3,\"output_tokens\":0}}}\n\n",
            "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
            "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n\n"
        ).into(),concat!(
            "event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
            "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":2}}\n\n",
            "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
        ).into()),
        _ => (format!("data: {}\n\n",json!({"responseId":"answer","modelVersion":"internal","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Hello"}]}}]})),
            format!("data: {}\n\n",json!({"responseId":"answer","modelVersion":"internal","candidates":[{"index":0,"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}))),
    };
    if truncated { first } else { first + &ending }
}

struct Fixture {
    base: String,
    calls: Arc<Mutex<Vec<Value>>>,
    task: tokio::task::JoinHandle<()>,
}
impl Drop for Fixture {
    fn drop(&mut self) {
        self.task.abort();
    }
}
async fn upstream(format: &'static str, mode: &'static str) -> Fixture {
    let calls = Arc::new(Mutex::new(Vec::new()));
    let observed = calls.clone();
    let app = Router::new().fallback(post(move |request: Request<Body>| {
        let observed = observed.clone();
        async move {
            let path = request.uri().to_string();
            let headers: BTreeMap<String, String> = request
                .headers()
                .iter()
                .map(|(k, v)| (k.to_string(), v.to_str().unwrap().to_owned()))
                .collect();
            let body: Value =
                serde_json::from_slice(&to_bytes(request.into_body(), 1024 * 1024).await.unwrap())
                    .unwrap();
            let streaming = body["stream"] == true || path.contains(":streamGenerateContent");
            observed
                .lock()
                .unwrap()
                .push(json!({"path":path,"headers":headers,"body":body}));
            if mode == "redirect" {
                return axum::http::Response::builder()
                    .status(307)
                    .header("location", "/credential-sink")
                    .body(Body::empty())
                    .unwrap();
            }
            if streaming {
                let text = if mode == "tools" {
                    tool_frames(format)
                } else {
                    frames(format, mode != "normal")
                };
                // Split across arbitrary SSE and UTF-8 boundaries, as real transports do.
                let chunks: Vec<_> = text
                    .into_bytes()
                    .chunks(7)
                    .map(|chunk| Ok::<_, std::io::Error>(chunk.to_vec()))
                    .collect();
                let source = futures::stream::iter(chunks);
                let source = if mode == "hang" {
                    source.chain(futures::stream::pending()).boxed()
                } else {
                    source.boxed()
                };
                axum::http::Response::builder()
                    .header("content-type", "text/event-stream")
                    .body(Body::from_stream(source))
                    .unwrap()
            } else {
                axum::http::Response::builder()
                    .header("content-type", "application/json")
                    .body(Body::from(
                        if mode == "tools" {
                            tool_response(format)
                        } else {
                            response(format)
                        }
                        .to_string(),
                    ))
                    .unwrap()
            }
        }
    }));
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base = format!("http://{}", listener.local_addr().unwrap());
    let task = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    Fixture { base, calls, task }
}
fn runtime(fixture: &Fixture, format: &str, limit: ConcurrencyLimit, options: Options) -> Runtime {
    let config:Config=serde_json::from_value(json!({"providers":{"upstream":{"kind":format,"base_url":format!("{}/{}",fixture.base,if format=="gemini"{"v1beta"}else{"v1"}),"api_key":"upstream-secret"}},"models":{"public":{"provider":"upstream","upstream_model":"internal","workloads":["chat"],"subjects":["alice"]}}})).unwrap();
    Runtime::new(
        config,
        Arc::new(
            ApiKeys::new(vec![ApiKey {
                id: "alice".into(),
                secret: "client-secret".into(),
            }])
            .unwrap(),
        ),
        limit,
        options,
    )
    .unwrap()
}
fn request(format: &str, streaming: bool) -> Request<Body> {
    let (path, key, body) = match format {
        "openai" => (
            "/v1/chat/completions".into(),
            "authorization",
            json!({"model":"public","messages":[{"role":"user","content":"Hi"}],"max_tokens":32,"stream":streaming}),
        ),
        "anthropic" => (
            "/v1/messages".into(),
            "x-api-key",
            json!({"model":"public","messages":[{"role":"user","content":"Hi"}],"max_tokens":32,"stream":streaming}),
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
            "x-goog-api-key",
            json!({"contents":[{"role":"user","parts":[{"text":"Hi"}]}],"generationConfig":{"maxOutputTokens":32}}),
        ),
    };
    Request::builder()
        .method("POST")
        .uri(path)
        .header("content-type", "application/json")
        .header(
            key,
            if key == "authorization" {
                "Bearer client-secret"
            } else {
                "client-secret"
            },
        )
        .body(Body::from(body.to_string()))
        .unwrap()
}

#[tokio::test]
async fn three_by_three_chat_and_streaming_preserve_aliases_and_isolate_credentials() {
    for upstream_format in FORMATS {
        let fixture = upstream(upstream_format, "normal").await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(&fixture, upstream_format, limit.clone(), Options::default());
        for ingress in FORMATS {
            for streaming in [false, true] {
                let result = runtime
                    .handle(request(ingress, streaming), CancellationToken::new())
                    .await;
                assert_eq!(
                    result.status(),
                    StatusCode::OK,
                    "{ingress} -> {upstream_format}, stream={streaming}"
                );
                assert_eq!(limit.available(), 0);
                let bytes = to_bytes(result.into_body(), 1024 * 1024).await.unwrap();
                let output = String::from_utf8(bytes.to_vec()).unwrap();
                assert!(
                    output.contains("Hello"),
                    "{ingress} -> {upstream_format}: {output}"
                );
                assert!(output.contains("public"));
                assert!(!output.contains("internal"));
                if streaming {
                    if ingress == "openai" {
                        assert!(!output.contains("\"usage\""));
                    }
                    assert!(output.contains(match ingress {
                        "openai" => "[DONE]",
                        "anthropic" => "message_stop",
                        _ => "finishReason",
                    }));
                } else {
                    let output: Value = serde_json::from_str(&output).unwrap();
                    assert_eq!(
                        match ingress {
                            "openai" => &output["usage"]["total_tokens"],
                            "anthropic" => &output["usage"]["output_tokens"],
                            _ => &output["usageMetadata"]["totalTokenCount"],
                        },
                        &json!(if ingress == "anthropic" { 2 } else { 5 })
                    );
                }
                assert_eq!(limit.available(), 1);
            }
        }
        let calls = fixture.calls.lock().unwrap();
        assert_eq!(calls.len(), 6);
        if upstream_format == "openai" {
            for index in [3, 5] {
                assert_eq!(
                    calls[index]["body"]["stream_options"]["include_usage"],
                    true
                );
            }
        }
        for call in calls.iter() {
            let headers = &call["headers"];
            assert!(!headers.to_string().contains("client-secret"));
            let expected_header = match upstream_format {
                "openai" => "authorization",
                "anthropic" => "x-api-key",
                _ => "x-goog-api-key",
            };
            assert_eq!(
                headers[expected_header],
                if upstream_format == "openai" {
                    "Bearer upstream-secret"
                } else {
                    "upstream-secret"
                }
            );
            if upstream_format == "anthropic" {
                assert_eq!(headers["anthropic-version"], "2023-06-01");
            }
            if upstream_format == "gemini" {
                assert!(
                    call["path"]
                        .as_str()
                        .unwrap()
                        .starts_with("/v1beta/models/internal:")
                );
                assert!(!call["path"].as_str().unwrap().contains("key="));
            } else {
                assert_eq!(call["body"]["model"], "internal");
            }
        }
    }
}

#[tokio::test]
async fn native_credential_ambiguity_and_denial_never_dispatch() {
    let fixture = upstream("openai", "normal").await;
    let runtime = runtime(
        &fixture,
        "openai",
        ConcurrencyLimit::new(1).unwrap(),
        Options::default(),
    );
    for format in ["anthropic", "gemini"] {
        let mut ambiguous = request(format, false);
        ambiguous
            .headers_mut()
            .insert("authorization", "Bearer client-secret".parse().unwrap());
        assert_eq!(
            runtime
                .handle(ambiguous, CancellationToken::new())
                .await
                .status(),
            StatusCode::UNAUTHORIZED
        );
        let mut denied = request(format, false);
        denied.headers_mut().insert(
            if format == "anthropic" {
                "x-api-key"
            } else {
                "x-goog-api-key"
            },
            "invalid".parse().unwrap(),
        );
        assert_eq!(
            runtime
                .handle(denied, CancellationToken::new())
                .await
                .status(),
            StatusCode::UNAUTHORIZED
        );
    }
    assert!(fixture.calls.lock().unwrap().is_empty());
}

#[tokio::test]
async fn native_stream_truncation_timeout_drop_and_redirect_release_admission() {
    for format in ["anthropic", "gemini"] {
        for mode in ["truncated", "hang", "redirect"] {
            let fixture = upstream(format, mode).await;
            let limit = ConcurrencyLimit::new(1).unwrap();
            let runtime = runtime(
                &fixture,
                format,
                limit.clone(),
                Options {
                    request_timeout: Duration::from_millis(500),
                    ..Options::default()
                },
            );
            let result = runtime
                .handle(request("openai", true), CancellationToken::new())
                .await;
            if mode == "redirect" {
                assert_eq!(result.status(), StatusCode::BAD_GATEWAY);
                drop(result);
            } else {
                assert_eq!(result.status(), StatusCode::OK);
                assert!(to_bytes(result.into_body(), 1024 * 1024).await.is_err());
            }
            assert_eq!(limit.available(), 1);
            assert_eq!(fixture.calls.lock().unwrap().len(), 1);
        }
        let fixture = upstream(format, "hang").await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(&fixture, format, limit.clone(), Options::default());
        let result = runtime
            .handle(request("openai", true), CancellationToken::new())
            .await;
        assert_eq!(result.status(), StatusCode::OK);
        drop(result);
        assert_eq!(limit.available(), 1);
    }
}

#[test]
fn native_providers_reject_embedding_configuration() {
    for kind in ["anthropic", "gemini"] {
        let kind: ProviderKind = serde_json::from_value(json!(kind)).unwrap();
        let config:Config=serde_json::from_value(json!({"providers":{"p":{"kind":kind,"base_url":"https://example.test/v1"}},"models":{"m":{"provider":"p","upstream_model":"m","workloads":["embedding"]}}})).unwrap();
        assert!(config.validate().is_err());
    }
}

fn tool_response(format: &str) -> Value {
    let mut payload = response(format);
    match format {
        "openai" => {
            payload["choices"][0]["message"] = json!({"role":"assistant","tool_calls":[{"id":"call-next","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"next\"}"}}]});
            payload["choices"][0]["finish_reason"] = json!("tool_calls");
        }
        "anthropic" => {
            payload["content"] = json!([{"type":"tool_use","id":"call-next","name":"lookup","input":{"query":"next"}}]);
            payload["stop_reason"] = json!("tool_use");
        }
        _ => {
            payload["candidates"][0]["content"]["parts"] =
                json!([{"functionCall":{"id":"call-next","name":"lookup","args":{"query":"next"}}}])
        }
    }
    payload
}

fn tool_frames(format: &str) -> String {
    let response = tool_response(format);
    match format {
        "openai" => {
            let base = json!({"id":"answer","object":"chat.completion.chunk","created":1,"model":"internal","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call-next","type":"function","function":{"name":"lookup","arguments":"{\"query\":"}}]},"finish_reason":null}]});
            let second = json!({"id":"answer","object":"chat.completion.chunk","created":1,"model":"internal","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"next\"}"}}]},"finish_reason":null}]});
            let final_chunk = json!({"id":"answer","object":"chat.completion.chunk","created":1,"model":"internal","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}});
            format!("data: {base}\n\ndata: {second}\n\ndata: {final_chunk}\n\ndata: [DONE]\n\n")
        }
        "anthropic" => {
            let mut start = response.clone();
            start["content"] = json!([]);
            start["stop_reason"] = Value::Null;
            let events = [
                json!({"type":"message_start","message":start}),
                json!({"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call-next","name":"lookup","input":{}}}),
                json!({"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}),
                json!({"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"next\"}"}}),
                json!({"type":"content_block_stop","index":0}),
                json!({"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":2}}),
                json!({"type":"message_stop"}),
            ];
            events
                .iter()
                .map(|e| format!("event: {}\ndata: {e}\n\n", e["type"].as_str().unwrap()))
                .collect()
        }
        _ => format!("data: {response}\n\n"),
    }
}

async fn tool_request(format: &str, streaming: bool) -> Request<Body> {
    let original = request(format, streaming);
    let (parts, body) = original.into_parts();
    let mut value: Value =
        serde_json::from_slice(&to_bytes(body, 1024 * 1024).await.unwrap()).unwrap();
    let schema =
        json!({"type":"object","properties":{"query":{"type":"string"}},"required":["query"]});
    match format {
        "openai" => {
            value["tools"] =
                json!([{"type":"function","function":{"name":"lookup","parameters":schema}}]);
            value["messages"].as_array_mut().unwrap().extend([
                json!({"role":"assistant","tool_calls":[{"id":"call-before","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"before\"}"}}]}),
                json!({"role":"tool","tool_call_id":"call-before","content":"{\"result\":\"found\"}"}),
            ]);
        }
        "anthropic" => {
            value["tools"] = json!([{"name":"lookup","input_schema":schema}]);
            value["messages"].as_array_mut().unwrap().extend([
                json!({"role":"assistant","content":[{"type":"tool_use","id":"call-before","name":"lookup","input":{"query":"before"}}]}),
                json!({"role":"user","content":[{"type":"tool_result","tool_use_id":"call-before","content":"{\"result\":\"found\"}"}]}),
            ]);
        }
        _ => {
            value["tools"] =
                json!([{"functionDeclarations":[{"name":"lookup","parametersJsonSchema":schema}]}]);
            value["contents"].as_array_mut().unwrap().extend([
                json!({"role":"model","parts":[{"functionCall":{"id":"call-before","name":"lookup","args":{"query":"before"}}}]}),
                json!({"role":"user","parts":[{"functionResponse":{"id":"call-before","name":"lookup","response":{"result":"found"}}}]}),
            ]);
        }
    }
    Request::from_parts(parts, Body::from(value.to_string()))
}

#[tokio::test]
async fn three_by_three_tools_preserve_definitions_results_and_streamed_arguments() {
    for upstream_format in FORMATS {
        let fixture = upstream(upstream_format, "tools").await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(&fixture, upstream_format, limit.clone(), Options::default());
        for ingress in FORMATS {
            for streaming in [false, true] {
                let response = runtime
                    .handle(
                        tool_request(ingress, streaming).await,
                        CancellationToken::new(),
                    )
                    .await;
                assert_eq!(
                    response.status(),
                    StatusCode::OK,
                    "{ingress} -> {upstream_format}, stream={streaming}"
                );
                let output = String::from_utf8(
                    to_bytes(response.into_body(), 1024 * 1024)
                        .await
                        .unwrap()
                        .to_vec(),
                )
                .unwrap();
                assert!(
                    output.contains("call-next")
                        && output.contains("lookup")
                        && output.contains("next"),
                    "{ingress}->{upstream_format}: {output}"
                );
                if streaming {
                    assert!(output.contains(match ingress {
                        "openai" => "[DONE]",
                        "anthropic" => "message_stop",
                        _ => "finishReason",
                    }));
                }
                assert_eq!(limit.available(), 1);
            }
        }
        let calls = fixture.calls.lock().unwrap();
        assert_eq!(calls.len(), 6);
        for call in calls.iter() {
            let body = &call["body"];
            assert!(body["tools"].is_array());
            let body = body.to_string();
            assert!(
                body.contains("call-before") && body.contains("lookup") && body.contains("found")
            );
        }
    }
}

#[tokio::test]
async fn native_paths_queries_and_unrepresentable_requests_fail_before_network() {
    let fixture = upstream("anthropic", "normal").await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(&fixture, "anthropic", limit.clone(), Options::default());
    for path in [
        "/v1beta/models/public:generateContent?key=client-secret",
        "/v1beta/models/public:streamGenerateContent?%6bey=client-secret",
    ] {
        let mut input = request("gemini", false);
        *input.uri_mut() = path.parse().unwrap();
        assert_eq!(
            runtime
                .handle(input, CancellationToken::new())
                .await
                .status(),
            StatusCode::UNAUTHORIZED
        );
    }
    let mut invalid = request("gemini", true);
    *invalid.uri_mut() = "/v1beta/models/public:streamGenerateContent?alt=json"
        .parse()
        .unwrap();
    assert_eq!(
        runtime
            .handle(invalid, CancellationToken::new())
            .await
            .status(),
        StatusCode::BAD_REQUEST
    );
    for payload in [
        json!({"model":"public","messages":[{"role":"user","content":"Hello"}]}),
        json!({"model":"public","messages":[{"role":"user","content":"Hello"}],"max_tokens":32,"frequency_penalty":1}),
    ] {
        let input = Request::builder()
            .method("POST")
            .uri("/v1/chat/completions")
            .header("authorization", "Bearer client-secret")
            .body(Body::from(payload.to_string()))
            .unwrap();
        let result = runtime.handle(input, CancellationToken::new()).await;
        assert_eq!(result.status(), StatusCode::BAD_REQUEST);
        drop(result);
    }
    assert_eq!(limit.available(), 1);
    assert!(fixture.calls.lock().unwrap().is_empty());
}

#[test]
fn gemini_model_paths_cannot_escape_configured_endpoint() {
    for name in [
        "../secret",
        "models/../secret",
        "%2e%2e",
        "foo?key=secret",
        "foo#bar",
        ".",
        "..",
        "models/",
    ] {
        let config:Config=serde_json::from_value(json!({"providers":{"p":{"kind":"gemini","base_url":"https://example.test/v1beta"}},"models":{"m":{"provider":"p","upstream_model":name,"workloads":["chat"]}}})).unwrap();
        assert!(config.validate().is_err(), "{name}");
    }
    let config:Config=serde_json::from_value(json!({"providers":{"p":{"kind":"gemini","base_url":"https://example.test/v1beta"}},"models":{"m":{"provider":"p","upstream_model":"models/gemini-example","workloads":["chat"]}}})).unwrap();
    assert!(config.validate().is_ok());
}

#[tokio::test]
async fn openai_clients_can_request_usage_from_every_upstream() {
    for format in FORMATS {
        let fixture = upstream(format, "normal").await;
        let runtime = runtime(
            &fixture,
            format,
            ConcurrencyLimit::new(1).unwrap(),
            Options::default(),
        );
        let (parts, body) = request("openai", true).into_parts();
        let mut value: Value =
            serde_json::from_slice(&to_bytes(body, 1024 * 1024).await.unwrap()).unwrap();
        value["stream_options"] = json!({"include_usage":true});
        let input = Request::from_parts(parts, Body::from(value.to_string()));
        let response = runtime.handle(input, CancellationToken::new()).await;
        assert_eq!(response.status(), StatusCode::OK);
        let body = to_bytes(response.into_body(), 1024 * 1024).await.unwrap();
        let mut parser = nyro_protocol::framing::Decoder::new(1024 * 1024);
        let events = parser.push(&body).unwrap();
        let usage: Vec<Value> = events
            .iter()
            .filter(|event| event.data != "[DONE]")
            .map(|event| serde_json::from_str::<Value>(&event.data).unwrap())
            .filter(|chunk| chunk["usage"].is_object())
            .collect();
        assert_eq!(
            usage.last().unwrap()["usage"]["total_tokens"],
            5,
            "{format}"
        );
    }
}

fn parallel_history_request(format: &str, streaming: bool, incomplete: bool) -> Request<Body> {
    let (path, mut body) = match format {
        "openai" => (
            "/v1/chat/completions",
            json!({"model":"public","max_tokens":32,"messages":[
            {"role":"user","content":"Compare"},
            {"role":"assistant","content":"Checking","tool_calls":[
                {"type":"function","id":"a","function":{"name":"lookup","arguments":"{\"city\":\"Paris\"}"}},
                {"type":"function","id":"b","function":{"name":"lookup","arguments":"{\"city\":\"Tokyo\"}"}}]},
            {"role":"tool","tool_call_id":"b","content":"Tokyo"},
            {"role":"tool","tool_call_id":"a","content":"Paris"},
            {"role":"user","content":"Summarize"}]}),
        ),
        "responses" => (
            "/v1/responses",
            json!({"model":"public","max_output_tokens":32,"input":[
            {"role":"user","content":"Compare"},
            {"role":"assistant","content":"Checking"},
            {"type":"function_call","call_id":"a","name":"lookup","arguments":"{\"city\":\"Paris\"}"},
            {"type":"function_call","call_id":"b","name":"lookup","arguments":"{\"city\":\"Tokyo\"}"},
            {"type":"function_call_output","call_id":"b","output":"Tokyo"},
            {"type":"function_call_output","call_id":"a","output":"Paris"},
            {"role":"user","content":"Summarize"}]}),
        ),
        "anthropic" => (
            "/v1/messages",
            json!({"model":"public","max_tokens":32,"messages":[
            {"role":"user","content":"Compare"},
            {"role":"assistant","content":[{"type":"text","text":"Checking"},
                {"type":"tool_use","id":"a","name":"lookup","input":{"city":"Paris"}},
                {"type":"tool_use","id":"b","name":"lookup","input":{"city":"Tokyo"}}]},
            {"role":"user","content":[{"type":"tool_result","tool_use_id":"b","content":"Tokyo"},
                {"type":"tool_result","tool_use_id":"a","content":"Paris"},{"type":"text","text":"Summarize"}]}]}),
        ),
        _ => (
            if streaming {
                "/v1beta/models/public:streamGenerateContent"
            } else {
                "/v1beta/models/public:generateContent"
            },
            json!({"generationConfig":{"maxOutputTokens":32},"contents":[
            {"role":"user","parts":[{"text":"Compare"}]},
            {"role":"model","parts":[{"text":"Checking"},
                {"functionCall":{"id":"a","name":"lookup","args":{"city":"Paris"}}},
                {"functionCall":{"id":"b","name":"lookup","args":{"city":"Tokyo"}}}]},
            {"role":"user","parts":[{"functionResponse":{"id":"b","name":"lookup","response":{"city":"Tokyo"}}},
                {"functionResponse":{"id":"a","name":"lookup","response":{"city":"Paris"}}}]},
            {"role":"user","parts":[{"text":"Summarize"}]}]}),
        ),
    };
    if format != "gemini" {
        body["stream"] = json!(streaming);
    }
    if incomplete {
        match format {
            "openai" => {
                body["messages"].as_array_mut().unwrap().remove(3);
            }
            "responses" => {
                body["input"].as_array_mut().unwrap().remove(5);
            }
            "anthropic" => {
                body["messages"][2]["content"]
                    .as_array_mut()
                    .unwrap()
                    .remove(1);
            }
            _ => {
                body["contents"][2]["parts"]
                    .as_array_mut()
                    .unwrap()
                    .remove(1);
            }
        }
    }
    Request::builder()
        .method("POST")
        .uri(path)
        .header("authorization", "Bearer client-secret")
        .header("content-type", "application/json")
        .body(Body::from(body.to_string()))
        .unwrap()
}

#[tokio::test]
async fn four_ingress_formats_preserve_parallel_history_as_one_anthropic_result_turn() {
    let fixture = upstream("anthropic", "normal").await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway = runtime(&fixture, "anthropic", limit.clone(), Options::default());
    for format in ["openai", "responses", "anthropic", "gemini"] {
        for streaming in [false, true] {
            let response = gateway
                .handle(
                    parallel_history_request(format, streaming, false),
                    CancellationToken::new(),
                )
                .await;
            assert_eq!(response.status(), StatusCode::OK, "{format} {streaming}");
            let bytes = to_bytes(response.into_body(), 65536).await.unwrap();
            assert!(String::from_utf8_lossy(&bytes).contains("Hello"));
            assert_eq!(limit.available(), 1);
            let calls = fixture.calls.lock().unwrap();
            let last = calls.last().unwrap();
            assert_eq!(last["path"], "/v1/messages");
            assert_eq!(last["headers"]["x-api-key"], "upstream-secret");
            assert!(last["headers"]["authorization"].is_null());
            assert_eq!(last["body"]["model"], "internal");
            let text = |city: &str| {
                if format == "gemini" {
                    json!({"city":city}).to_string()
                } else {
                    city.to_owned()
                }
            };
            assert_eq!(
                last["body"]["messages"],
                json!([
                {"role":"user","content":[{"type":"text","text":"Compare"}]},
                {"role":"assistant","content":[{"type":"text","text":"Checking"},
                    {"type":"tool_use","id":"a","name":"lookup","input":{"city":"Paris"}},
                    {"type":"tool_use","id":"b","name":"lookup","input":{"city":"Tokyo"}}]},
                {"role":"user","content":[
                    {"type":"tool_result","tool_use_id":"b","content":[{"type":"text","text":text("Tokyo")}]},
                    {"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":text("Paris")}]},
                    {"type":"text","text":"Summarize"}]}])
            );
        }
    }
    assert_eq!(fixture.calls.lock().unwrap().len(), 8);
}

#[tokio::test]
async fn incomplete_parallel_results_are_rejected_before_anthropic_dispatch() {
    let fixture = upstream("anthropic", "normal").await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway = runtime(&fixture, "anthropic", limit.clone(), Options::default());
    for format in ["openai", "responses", "anthropic", "gemini"] {
        let response = gateway
            .handle(
                parallel_history_request(format, false, true),
                CancellationToken::new(),
            )
            .await;
        assert_eq!(response.status(), StatusCode::BAD_REQUEST, "{format}");
        to_bytes(response.into_body(), 65536).await.unwrap();
        assert_eq!(limit.available(), 1);
    }
    assert!(fixture.calls.lock().unwrap().is_empty());
}
