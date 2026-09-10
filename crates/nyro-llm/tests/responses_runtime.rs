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
    config::Config,
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

const FORMATS: [&str; 4] = ["openai", "anthropic", "gemini", "responses"];

// Native fixtures deliberately do not pass through nyro's codec or canonical IR.
fn native_response(tools: bool, incomplete: bool) -> Value {
    json!({
        "id":"answer", "object":"response", "created_at":1, "model":"internal",
        "status":if incomplete { "incomplete" } else { "completed" },
        "error":null,
        "text":{"format":{"type":"text"},"verbosity":"medium"},"service_tier":"default",
        "incomplete_details":if incomplete { json!({"reason":"max_output_tokens"}) } else { Value::Null },
        "output":if tools {
            json!([{"type":"function_call","id":"fc_native","call_id":"call-next","name":"lookup","arguments":"{\"query\":\"next\"}","status":"completed"}])
        } else {
            json!([{"type":"message","id":"msg_native","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Hello","annotations":[]}]}])
        },
        "usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5,"input_tokens_details":{"cached_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}
    })
}

fn native_frames(mode: &str) -> String {
    let tools = mode == "tools";
    let mut terminal = if matches!(mode, "refusal" | "filtered") {
        safety_response("responses", mode)
    } else {
        native_response(tools, mode == "length")
    };
    let mut start = terminal.clone();
    start["status"] = json!("in_progress");
    start["output"] = json!([]);
    start["usage"] = Value::Null;
    start["incomplete_details"] = Value::Null;
    let final_item = terminal["output"][0].clone();
    let mut initial_item = final_item.clone();
    initial_item["status"] = json!("in_progress");
    let mut events = vec![
        json!({"type":"response.created","response":start}),
        json!({"type":"response.in_progress","response":start}),
    ];
    if tools {
        initial_item["arguments"] = json!("");
        events.extend([
            json!({"type":"response.output_item.added","output_index":0,"item":initial_item}),
            json!({"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_native","delta":"{\"query\":"}),
            json!({"type":"response.function_call_arguments.delta","output_index":0,"item_id":"fc_native","delta":"\"next\"}"}),
            json!({"type":"response.function_call_arguments.done","output_index":0,"item_id":"fc_native","arguments":"{\"query\":\"next\"}"}),
        ]);
    } else {
        initial_item["content"] = json!([]);
        events.extend([
            json!({"type":"response.output_item.added","output_index":0,"item":initial_item}),
            json!({"type":"response.content_part.added","output_index":0,"content_index":0,"item_id":"msg_native","part":{"type":"output_text","text":"","annotations":[]}}),
            json!({"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_native","delta":"Hel","logprobs":[]}),
            json!({"type":"response.output_text.delta","output_index":0,"content_index":0,"item_id":"msg_native","delta":"lo","logprobs":[]}),
            json!({"type":"response.output_text.done","output_index":0,"content_index":0,"item_id":"msg_native","text":"Hello","logprobs":[]}),
            json!({"type":"response.content_part.done","output_index":0,"content_index":0,"item_id":"msg_native","part":final_item["content"][0]}),
        ]);
    }
    events.push(json!({"type":"response.output_item.done","output_index":0,"item":final_item}));
    if mode == "snapshot_conflict" {
        terminal["output"][0]["content"][0]["text"] = json!("different");
    }
    if !matches!(mode, "truncated" | "hang") {
        events.push(if mode == "error" {
            json!({"type":"error","code":"server_error","message":"upstream failure","param":null})
        } else if mode == "failed" {
            terminal["status"] = json!("failed");
            terminal["error"] = json!({"code":"server_error","message":"upstream failure"});
            json!({"type":"response.failed","response":terminal})
        } else {
            json!({"type":if matches!(mode, "length" | "filtered") {"response.incomplete"} else {"response.completed"},"response":terminal})
        });
    }
    let mut result = String::new();
    for (index, mut event) in events.into_iter().enumerate() {
        event["sequence_number"] = json!(index);
        if mode == "refusal" {
            match event["type"].as_str().unwrap() {
                "response.content_part.added" => {
                    event["part"] = json!({"type":"refusal","refusal":""})
                }
                "response.output_text.delta" => {
                    event["type"] = json!("response.refusal.delta");
                    event["delta"] = json!(if index == 4 { "Cannot " } else { "help" });
                    event.as_object_mut().unwrap().remove("logprobs");
                }
                "response.output_text.done" => {
                    event["type"] = json!("response.refusal.done");
                    event["refusal"] = json!("Cannot help");
                    event.as_object_mut().unwrap().remove("text");
                    event.as_object_mut().unwrap().remove("logprobs");
                }
                _ => {}
            }
        }
        if mode == "wrong_item" && index == 4 {
            event["item_id"] = json!("unknown-item");
        }
        if mode == "wrong_sequence" && index == 5 {
            event["sequence_number"] = json!(1);
        }
        let name = if mode == "wrong_event" && index == 4 {
            "response.completed"
        } else {
            event["type"].as_str().unwrap()
        };
        result.push_str(&format!("event: {name}\ndata: {event}\n\n"));
    }
    result
}

fn response(format: &str) -> Value {
    match format {
        "responses" => native_response(false, false),
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
    if format == "responses" {
        return native_frames(if truncated { "truncated" } else { "normal" });
    }
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
fn echo_cache_options(response: &mut Value, request: &Value, format: &str) {
    if request.get("prompt_cache_options").is_none() {
        return;
    }
    if format == "responses" {
        response["prompt_cache_options"] = request["prompt_cache_options"].clone();
    }
    if !response["usage"].is_null() {
        let details = if format == "responses" {
            "input_tokens_details"
        } else {
            "prompt_tokens_details"
        };
        response["usage"][details] = json!({"cached_tokens":1,"cache_write_tokens":2});
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
                let text = if format == "responses" {
                    native_frames(mode)
                } else if matches!(mode, "refusal" | "filtered") {
                    safety_frames(format, mode)
                } else if mode == "tools" {
                    tool_frames(format)
                } else if mode == "length" {
                    frames(format, false).replace("\"stop\"", "\"length\"")
                } else {
                    frames(format, mode != "normal")
                };
                // Responses echoes request cache options on response envelopes.
                let text = if matches!(format, "responses" | "openai")
                    && body.get("prompt_cache_options").is_some()
                {
                    text.split_inclusive('\n')
                        .map(|line| {
                            if let Some(data) = line
                                .strip_prefix("data: ")
                                .filter(|data| data.trim() != "[DONE]")
                            {
                                let mut event: Value = serde_json::from_str(data).unwrap();
                                if format == "openai" {
                                    echo_cache_options(&mut event, &body, format);
                                } else if let Some(response) = event.get_mut("response") {
                                    echo_cache_options(response, &body, format);
                                }
                                format!("data: {event}\n")
                            } else {
                                line.to_owned()
                            }
                        })
                        .collect()
                } else {
                    text
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
                        if matches!(mode, "refusal" | "filtered") {
                            safety_response(format, mode)
                        } else if mode == "tools" {
                            tool_response(format)
                        } else if mode == "length" && format == "responses" {
                            native_response(false, true)
                        } else if mode == "length" {
                            let mut value = response(format);
                            value["choices"][0]["finish_reason"] = json!("length");
                            value
                        } else {
                            let mut response = response(format);
                            if matches!(format, "responses" | "openai")
                                && body.get("prompt_cache_options").is_some()
                            {
                                echo_cache_options(&mut response, &body, format);
                            }
                            response
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
    let mut provider = json!({"kind":if format == "responses" { "openai" } else { format },"base_url":format!("{}/{}",fixture.base,if format=="gemini"{"v1beta"}else{"v1"}),"api_key":"upstream-secret"});
    if format == "responses" {
        provider["api"] = json!("responses");
    }
    let config: Config = serde_json::from_value(json!({"providers":{"upstream":provider},"models":{"public":{"provider":"upstream","upstream_model":"internal","workloads":["chat"],"subjects":["alice"]}}})).unwrap();
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
        "responses" => (
            "/v1/responses".into(),
            "authorization",
            json!({"model":"public","input":"Hi","max_output_tokens":32,"stream":streaming,"store":false}),
        ),
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

fn tool_response(format: &str) -> Value {
    if format == "responses" {
        return native_response(true, false);
    }
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
    if format == "responses" {
        return native_frames("tools");
    }
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
        "responses" => {
            value["tools"] =
                json!([{"type":"function","name":"lookup","parameters":schema,"strict":false}]);
            value["input"] = json!([
                {"role":"user","content":"Hi"},
                {"type":"function_call","call_id":"call-before","name":"lookup","arguments":"{\"query\":\"before\"}"},
                {"type":"function_call_output","call_id":"call-before","output":"{\"result\":\"found\"}"}
            ]);
        }
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

fn sse_values(bytes: &[u8]) -> Vec<Value> {
    let mut parser = nyro_protocol::framing::Decoder::new(1024 * 1024);
    let events = parser.push(bytes).unwrap();
    assert!(parser.finish().unwrap().is_empty());
    events
        .into_iter()
        .filter(|e| e.data != "[DONE]")
        .map(|e| {
            let value: Value = serde_json::from_str(&e.data).unwrap();
            if let Some(name) = e.event {
                assert_eq!(value["type"], name);
            }
            value
        })
        .collect()
}

// Compare the entire semantic payload, normalizing only protocol envelope IDs/timestamps.
fn snapshot(format: &str, value: &Value) -> Value {
    let (model, text, calls, finish, usage) = match format {
        "responses" => {
            let output = value["output"].as_array().unwrap();
            let mut text = String::new();
            let mut calls = Vec::new();
            for item in output {
                assert!(!item["id"].as_str().unwrap().is_empty());
                assert!(
                    item["status"] == "completed"
                        || (value["status"] == "incomplete" && item["status"] == "incomplete")
                );
                match item["type"].as_str().unwrap() {
                    "message" => {
                        assert_eq!(item["role"], "assistant");
                        for part in item["content"].as_array().unwrap() {
                            assert_eq!(part["type"], "output_text");
                            assert_eq!(part["annotations"], json!([]));
                            text.push_str(part["text"].as_str().unwrap());
                        }
                    }
                    "function_call" => calls.push(json!({"id":item["call_id"],"name":item["name"],"arguments":item["arguments"]})),
                    other => panic!("unexpected output item {other}: {item}"),
                }
            }
            assert_eq!(value["object"], "response");
            assert!(value["error"].is_null());
            let finish = if value["status"] == "incomplete" {
                assert_eq!(value["incomplete_details"]["reason"], "max_output_tokens");
                "length"
            } else {
                assert_eq!(value["status"], "completed");
                assert!(value["incomplete_details"].is_null());
                if calls.is_empty() {
                    "stop"
                } else {
                    "tool_calls"
                }
            };
            (
                value["model"].clone(),
                text,
                calls,
                json!(finish),
                json!([
                    value["usage"]["input_tokens"],
                    value["usage"]["output_tokens"],
                    value["usage"]["total_tokens"]
                ]),
            )
        }
        "openai" => {
            assert_eq!(value["choices"].as_array().unwrap().len(), 1);
            let choice = &value["choices"][0];
            assert_eq!(choice["index"], 0);
            assert_eq!(choice["message"]["role"], "assistant");
            let calls = choice["message"]["tool_calls"].as_array().into_iter().flatten().map(|call| {
                assert_eq!(call["type"], "function");
                json!({"id":call["id"],"name":call["function"]["name"],"arguments":call["function"]["arguments"]})
            }).collect();
            let text = match &choice["message"]["content"] {
                Value::String(text) => text.clone(),
                Value::Array(parts) => parts
                    .iter()
                    .map(|part| {
                        assert_eq!(part["type"], "text");
                        part["text"].as_str().unwrap()
                    })
                    .collect(),
                Value::Null => String::new(),
                other => panic!("unexpected message content: {other}"),
            };
            (
                value["model"].clone(),
                text,
                calls,
                choice["finish_reason"].clone(),
                json!([
                    value["usage"]["prompt_tokens"],
                    value["usage"]["completion_tokens"],
                    value["usage"]["total_tokens"]
                ]),
            )
        }
        "anthropic" => {
            assert_eq!(value["role"], "assistant");
            let mut text = String::new();
            let mut calls = Vec::new();
            for item in value["content"].as_array().unwrap() {
                match item["type"].as_str().unwrap() {
                    "text" => text.push_str(item["text"].as_str().unwrap()),
                    "tool_use" => calls.push(json!({"id":item["id"],"name":item["name"],"arguments":item["input"].to_string()})),
                    other => panic!("unexpected {other}"),
                }
            }
            let finish = match value["stop_reason"].as_str().unwrap() {
                "end_turn" => "stop",
                "tool_use" => "tool_calls",
                "max_tokens" => "length",
                other => panic!("unexpected {other}"),
            };
            (
                value["model"].clone(),
                text,
                calls,
                json!(finish),
                json!([
                    value["usage"]["input_tokens"],
                    value["usage"]["output_tokens"],
                    value["usage"]["input_tokens"].as_u64().unwrap()
                        + value["usage"]["output_tokens"].as_u64().unwrap()
                ]),
            )
        }
        _ => {
            assert_eq!(value["candidates"].as_array().unwrap().len(), 1);
            let mut text = String::new();
            let mut calls = Vec::new();
            for item in value["candidates"][0]["content"]["parts"]
                .as_array()
                .unwrap()
            {
                if let Some(content) = item["text"].as_str() {
                    text.push_str(content);
                } else {
                    let call = &item["functionCall"];
                    assert!(call.is_object(), "{item}");
                    calls.push(json!({"id":call["id"],"name":call["name"],"arguments":call["args"].to_string()}));
                }
            }
            let finish = match value["candidates"][0]["finishReason"].as_str().unwrap() {
                "MAX_TOKENS" => "length",
                "STOP" if calls.is_empty() => "stop",
                "STOP" => "tool_calls",
                other => panic!("unexpected {other}"),
            };
            (
                value["modelVersion"].clone(),
                text,
                calls,
                json!(finish),
                json!([
                    value["usageMetadata"]["promptTokenCount"],
                    value["usageMetadata"]["candidatesTokenCount"],
                    value["usageMetadata"]["totalTokenCount"]
                ]),
            )
        }
    };
    json!({"model":model,"text":text,"calls":calls,"finish":finish,"usage":usage})
}

fn stream_snapshot(format: &str, bytes: &[u8]) -> Value {
    let events = sse_values(bytes);
    if format == "responses" {
        assert!(!String::from_utf8_lossy(bytes).contains("[DONE]"));
        let terminal = events.last().unwrap();
        assert!(matches!(
            terminal["type"].as_str(),
            Some("response.completed" | "response.incomplete")
        ));
        let output = &terminal["response"]["output"];
        for (sequence, event) in events.iter().enumerate() {
            assert_eq!(event["sequence_number"], sequence);
            if let Some(index) = event["output_index"].as_u64() {
                let final_item = &output[index as usize];
                if event["item_id"].is_string() {
                    assert_eq!(event["item_id"], final_item["id"]);
                }
                if event["item"].is_object() {
                    assert_eq!(event["item"]["id"], final_item["id"]);
                }
                if event["type"] == "response.output_item.done" {
                    assert_eq!(event["item"], *final_item);
                }
            }
        }
        for (index, item) in output.as_array().unwrap().iter().enumerate() {
            let tool = item["type"] == "function_call";
            let delta_type = if tool {
                "response.function_call_arguments.delta"
            } else {
                "response.output_text.delta"
            };
            let deltas: String = events
                .iter()
                .filter(|e| e["type"] == delta_type && e["output_index"] == index)
                .map(|e| e["delta"].as_str().unwrap())
                .collect();
            assert_eq!(
                deltas,
                if tool {
                    item["arguments"].as_str().unwrap()
                } else {
                    item["content"][0]["text"].as_str().unwrap()
                }
            );
        }
        return snapshot(format, &terminal["response"]);
    }
    let mut value = response(format);
    value["usage"] = Value::Null;
    value["usageMetadata"] = Value::Null;
    match format {
        "openai" => {
            value["choices"][0]["finish_reason"] = Value::Null;
            assert!(String::from_utf8_lossy(bytes).ends_with("data: [DONE]\n\n"));
            let mut text = String::new();
            let mut call = json!({"id":"","type":"function","function":{"name":"","arguments":""}});
            let mut arguments = String::new();
            for event in &events {
                value["model"] = event["model"].clone();
                if event["usage"].is_object() {
                    value["usage"] = event["usage"].clone();
                }
                if let Some(choice) = event["choices"].as_array().unwrap().first() {
                    if choice["finish_reason"].is_string() {
                        value["choices"][0]["finish_reason"] = choice["finish_reason"].clone();
                    }
                    if let Some(s) = choice["delta"]["content"].as_str() {
                        text.push_str(s);
                    }
                    for delta in choice["delta"]["tool_calls"]
                        .as_array()
                        .into_iter()
                        .flatten()
                    {
                        assert_eq!(delta["index"], 0);
                        if delta["id"].is_string() {
                            call["id"] = delta["id"].clone();
                        }
                        if delta["function"]["name"].is_string() {
                            call["function"]["name"] = delta["function"]["name"].clone();
                        }
                        if let Some(s) = delta["function"]["arguments"].as_str() {
                            arguments.push_str(s);
                        }
                    }
                }
            }
            value["choices"][0]["message"]["content"] = json!(text);
            if !arguments.is_empty() {
                call["function"]["arguments"] = json!(arguments);
                value["choices"][0]["message"]["tool_calls"] = json!([call]);
            }
        }
        "anthropic" => {
            value = events[0]["message"].clone();
            assert_eq!(events.last().unwrap()["type"], "message_stop");
            let mut blocks: Vec<Value> = Vec::new();
            let mut arguments = String::new();
            for event in &events {
                match event["type"].as_str().unwrap() {
                    "content_block_start" => blocks.push(event["content_block"].clone()),
                    "content_block_delta" if event["delta"]["type"] == "text_delta" => {
                        let block = blocks.last_mut().unwrap();
                        let text = block["text"].as_str().unwrap().to_owned()
                            + event["delta"]["text"].as_str().unwrap();
                        block["text"] = json!(text);
                    }
                    "content_block_delta" => {
                        arguments.push_str(event["delta"]["partial_json"].as_str().unwrap())
                    }
                    "content_block_stop" if !arguments.is_empty() => {
                        blocks.last_mut().unwrap()["input"] =
                            serde_json::from_str(&arguments).unwrap();
                        arguments.clear();
                    }
                    "message_delta" => {
                        value["stop_reason"] = event["delta"]["stop_reason"].clone();
                        value["usage"]["output_tokens"] = event["usage"]["output_tokens"].clone();
                        if event["usage"]["input_tokens"].is_number() {
                            value["usage"]["input_tokens"] = event["usage"]["input_tokens"].clone();
                        }
                    }
                    _ => {}
                }
            }
            value["content"] = json!(blocks);
        }
        _ => {
            value["candidates"][0]["finishReason"] = Value::Null;
            let mut parts: Vec<Value> = Vec::new();
            for event in events {
                parts.extend(
                    event["candidates"][0]["content"]["parts"]
                        .as_array()
                        .into_iter()
                        .flatten()
                        .cloned(),
                );
                value["modelVersion"] = event["modelVersion"].clone();
                if event["usageMetadata"].is_object() {
                    value["usageMetadata"] = event["usageMetadata"].clone();
                }
                if event["candidates"][0]["finishReason"].is_string() {
                    value["candidates"][0]["finishReason"] =
                        event["candidates"][0]["finishReason"].clone();
                }
            }
            value["candidates"][0]["content"]["parts"] = json!(parts);
        }
    }
    snapshot(format, &value)
}

#[tokio::test]
async fn responses_seven_edges_preserve_text_tools_history_usage_and_credentials() {
    for upstream_format in FORMATS {
        for tools in [false, true] {
            let fixture = upstream(upstream_format, if tools { "tools" } else { "normal" }).await;
            let limit = ConcurrencyLimit::new(1).unwrap();
            let runtime = runtime(&fixture, upstream_format, limit.clone(), Options::default());
            for ingress in FORMATS
                .into_iter()
                .filter(|f| *f == "responses" || upstream_format == "responses")
            {
                for streaming in [false, true] {
                    let mut input = if tools {
                        tool_request(ingress, streaming).await
                    } else {
                        request(ingress, streaming)
                    };
                    if ingress == "openai" && streaming {
                        let (parts, body) = input.into_parts();
                        let mut body: Value =
                            serde_json::from_slice(&to_bytes(body, 1024 * 1024).await.unwrap())
                                .unwrap();
                        body["stream_options"] = json!({"include_usage":true});
                        input = Request::from_parts(parts, Body::from(body.to_string()));
                    }
                    let result = runtime.handle(input, CancellationToken::new()).await;
                    let status = result.status();
                    let bytes = to_bytes(result.into_body(), 1024 * 1024).await.unwrap_or_else(|e| panic!("{ingress}->{upstream_format}, tools={tools}, stream={streaming}: {e}"));
                    assert_eq!(
                        status,
                        StatusCode::OK,
                        "{ingress}->{upstream_format}, tools={tools}, stream={streaming}: {}",
                        String::from_utf8_lossy(&bytes)
                    );
                    let actual = if streaming {
                        stream_snapshot(ingress, &bytes)
                    } else {
                        snapshot(ingress, &serde_json::from_slice(&bytes).unwrap())
                    };
                    assert_eq!(
                        actual,
                        json!({"model":"public","text":if tools {""} else {"Hello"},"calls":if tools {json!([{"id":"call-next","name":"lookup","arguments":"{\"query\":\"next\"}"}])} else {json!([])},"finish":if tools {"tool_calls"} else {"stop"},"usage":[3,2,5]}),
                        "{ingress}->{upstream_format}, tools={tools}, stream={streaming}"
                    );
                    assert!(!String::from_utf8_lossy(&bytes).contains("internal"));
                    assert_eq!(limit.available(), 1);
                }
            }
            let calls = fixture.calls.lock().unwrap();
            assert_eq!(
                calls.len(),
                if upstream_format == "responses" { 8 } else { 2 }
            );
            for call in calls.iter() {
                let headers = &call["headers"];
                let body = &call["body"];
                assert!(!headers.to_string().contains("client-secret"));
                let expected_key = match upstream_format {
                    "anthropic" => "x-api-key",
                    "gemini" => "x-goog-api-key",
                    _ => "authorization",
                };
                for key in ["authorization", "x-api-key", "x-goog-api-key"] {
                    if key == expected_key {
                        assert_eq!(
                            headers[key],
                            if key == "authorization" {
                                "Bearer upstream-secret"
                            } else {
                                "upstream-secret"
                            }
                        );
                    } else {
                        assert!(headers[key].is_null());
                    }
                }
                if upstream_format == "gemini" {
                    assert!(
                        call["path"]
                            .as_str()
                            .unwrap()
                            .starts_with("/v1beta/models/internal:")
                    );
                } else {
                    assert_eq!(body["model"], "internal");
                    assert_eq!(
                        call["path"],
                        match upstream_format {
                            "responses" => "/v1/responses",
                            "anthropic" => "/v1/messages",
                            _ => "/v1/chat/completions",
                        }
                    );
                }
                let max_tokens = match upstream_format {
                    "responses" => &body["max_output_tokens"],
                    "gemini" => &body["generationConfig"]["maxOutputTokens"],
                    _ => &body["max_tokens"],
                };
                assert_eq!(max_tokens, &json!(32));
                assert!(!call["path"].as_str().unwrap().contains("key="));
                if upstream_format == "anthropic" {
                    assert_eq!(headers["anthropic-version"], "2023-06-01");
                }
                if upstream_format == "responses" {
                    assert_eq!(body["store"], false);
                    if body["stream"] == true {
                        assert_eq!(body["stream_options"]["include_obfuscation"], false);
                    }
                }
                if upstream_format == "openai" && body["stream"] == true {
                    assert_eq!(body["stream_options"]["include_usage"], true);
                }
                if upstream_format == "openai" {
                    assert_eq!(body["store"], false);
                }
                if tools {
                    assert_tool_history(upstream_format, body);
                }
            }
        }
    }
}

fn assert_tool_history(format: &str, body: &Value) {
    let schema =
        json!({"type":"object","properties":{"query":{"type":"string"}},"required":["query"]});
    match format {
        "responses" => {
            assert_eq!(body["tools"][0]["name"], "lookup");
            assert_eq!(body["tools"][0]["parameters"], schema);
            assert_eq!(body["tools"][0]["strict"], false);
            let input = body["input"].as_array().unwrap();
            let call = input.iter().find(|i| i["type"] == "function_call").unwrap();
            assert_eq!(call["call_id"], "call-before");
            assert_eq!(call["name"], "lookup");
            assert_eq!(call["arguments"], "{\"query\":\"before\"}");
            let result = input
                .iter()
                .find(|i| i["type"] == "function_call_output")
                .unwrap();
            assert_eq!(result["call_id"], "call-before");
            assert_eq!(result["output"], "{\"result\":\"found\"}");
        }
        "openai" => {
            assert_eq!(body["tools"][0]["function"]["parameters"], schema);
            assert_eq!(
                body["messages"][1]["tool_calls"][0],
                json!({"id":"call-before","type":"function","function":{"name":"lookup","arguments":"{\"query\":\"before\"}"}})
            );
            assert_eq!(body["messages"][2]["tool_call_id"], "call-before");
            assert_eq!(body["messages"][2]["content"], "{\"result\":\"found\"}");
        }
        "anthropic" => {
            assert_eq!(body["tools"][0]["input_schema"], schema);
            assert_eq!(
                body["messages"][1]["content"][0],
                json!({"type":"tool_use","id":"call-before","name":"lookup","input":{"query":"before"}})
            );
            assert_eq!(
                body["messages"][2]["content"][0]["tool_use_id"],
                "call-before"
            );
            assert!(
                body["messages"][2]["content"][0]["content"]
                    .to_string()
                    .contains("found")
            );
        }
        _ => {
            assert_eq!(
                body["tools"][0]["functionDeclarations"][0]["parametersJsonSchema"],
                schema
            );
            assert_eq!(
                body["contents"][1]["parts"][0]["functionCall"],
                json!({"id":"call-before","name":"lookup","args":{"query":"before"}})
            );
            assert_eq!(
                body["contents"][2]["parts"][0]["functionResponse"],
                json!({"id":"call-before","name":"lookup","response":{"result":"found"}})
            );
        }
    }
}

#[tokio::test]
async fn responses_protected_intake_and_unsupported_controls_never_dispatch() {
    let fixture = upstream("openai", "normal").await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(&fixture, "openai", limit.clone(), Options::default());
    for token in [None, Some("Bearer invalid")] {
        let mut input = request("responses", false);
        input.headers_mut().remove("authorization");
        if let Some(token) = token {
            input
                .headers_mut()
                .insert("authorization", token.parse().unwrap());
        }
        let result = runtime.handle(input, CancellationToken::new()).await;
        assert_eq!(result.status(), StatusCode::UNAUTHORIZED);
        drop(result);
    }
    for extra in [
        json!({"previous_response_id":"resp_prior"}),
        json!({"conversation":"conv_prior"}),
        json!({"store":true}),
        json!({"background":true}),
        json!({"reasoning":{"effort":"high"}}),
        json!({"tools":[{"type":"web_search"}]}),
        json!({"input":[{"type":"item_reference","id":"msg_prior"}]}),
        json!({"input":[{"role":"user","content":[{"type":"input_image","file_id":"file-1"}]}]}),
        json!({"unknown_control":true}),
    ] {
        let mut payload = json!({"model":"public","input":"Hi","max_output_tokens":32});
        payload
            .as_object_mut()
            .unwrap()
            .extend(extra.as_object().unwrap().clone());
        let input = Request::builder()
            .method("POST")
            .uri("/v1/responses")
            .header("authorization", "Bearer client-secret")
            .body(Body::from(payload.to_string()))
            .unwrap();
        let result = runtime.handle(input, CancellationToken::new()).await;
        assert_eq!(result.status(), StatusCode::BAD_REQUEST, "{extra}");
        let error: Value =
            serde_json::from_slice(&to_bytes(result.into_body(), 1024 * 1024).await.unwrap())
                .unwrap();
        assert!(error["error"]["message"].is_string(), "{error}");
        assert_eq!(limit.available(), 1);
    }
    assert!(fixture.calls.lock().unwrap().is_empty());
}

#[tokio::test]
async fn responses_stream_failures_deadline_and_drop_release_admission_without_success() {
    for mode in [
        "truncated",
        "error",
        "failed",
        "snapshot_conflict",
        "wrong_item",
        "wrong_sequence",
        "wrong_event",
        "hang",
        "redirect",
    ] {
        let fixture = upstream("responses", mode).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let runtime = runtime(
            &fixture,
            "responses",
            limit.clone(),
            Options {
                request_timeout: Duration::from_millis(500),
                ..Options::default()
            },
        );
        for ingress in ["openai", "responses"] {
            let result = runtime
                .handle(request(ingress, true), CancellationToken::new())
                .await;
            if mode == "redirect" {
                assert_eq!(result.status(), StatusCode::BAD_GATEWAY);
                drop(result);
            } else {
                assert_eq!(result.status(), StatusCode::OK);
                let mut stream = result.into_body().into_data_stream();
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
                    "{mode}/{ingress}: {}",
                    String::from_utf8_lossy(&output)
                );
                assert!(
                    !String::from_utf8_lossy(&output).contains("[DONE]"),
                    "{mode}"
                );
                assert!(
                    !String::from_utf8_lossy(&output).contains("event: response.completed"),
                    "{mode}"
                );
                drop(stream);
            }
            assert_eq!(limit.available(), 1, "{mode}/{ingress}");
        }
        assert_eq!(fixture.calls.lock().unwrap().len(), 2);
    }
    let fixture = upstream("responses", "hang").await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let runtime = runtime(&fixture, "responses", limit.clone(), Options::default());
    let result = runtime
        .handle(request("responses", true), CancellationToken::new())
        .await;
    assert_eq!(result.status(), StatusCode::OK);
    assert_eq!(limit.available(), 0);
    drop(result);
    assert_eq!(limit.available(), 1);
    let cancellation = CancellationToken::new();
    let result = runtime
        .handle(request("responses", true), cancellation.clone())
        .await;
    cancellation.cancel();
    assert!(to_bytes(result.into_body(), 1024 * 1024).await.is_err());
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn responses_and_chat_length_finish_maps_incomplete_in_json_and_streams() {
    for (upstream_format, ingress) in [
        ("openai", "responses"),
        ("responses", "openai"),
        ("responses", "responses"),
    ] {
        let fixture = upstream(upstream_format, "length").await;
        let runtime = runtime(
            &fixture,
            upstream_format,
            ConcurrencyLimit::new(1).unwrap(),
            Options::default(),
        );
        for streaming in [false, true] {
            let result = runtime
                .handle(request(ingress, streaming), CancellationToken::new())
                .await;
            assert_eq!(result.status(), StatusCode::OK);
            let body = to_bytes(result.into_body(), 1024 * 1024).await.unwrap();
            if streaming && ingress == "openai" {
                assert!(String::from_utf8_lossy(&body).contains("[DONE]"));
                let values = sse_values(&body);
                assert!(
                    values
                        .iter()
                        .any(|v| v["choices"][0]["finish_reason"] == "length")
                );
            } else {
                let actual = if streaming {
                    stream_snapshot(ingress, &body)
                } else {
                    snapshot(ingress, &serde_json::from_slice(&body).unwrap())
                };
                assert_eq!(
                    actual,
                    json!({"model":"public","text":"Hello","calls":[],"finish":"length","usage":[3,2,5]})
                );
            }
        }
    }
}

fn safety_response(format: &str, mode: &str) -> Value {
    let mut value = response(format);
    if format == "responses" {
        if mode == "refusal" {
            value["output"][0]["content"] = json!([{"type":"refusal","refusal":"Cannot help"}]);
        } else {
            value["status"] = json!("incomplete");
            value["incomplete_details"] = json!({"reason":"content_filter"});
        }
    } else if mode == "refusal" {
        value["choices"][0]["message"]["content"] = Value::Null;
        value["choices"][0]["message"]["refusal"] = json!("Cannot help");
    } else {
        value["choices"][0]["finish_reason"] = json!("content_filter");
    }
    value
}

fn safety_frames(format: &str, mode: &str) -> String {
    assert_eq!(format, "openai");
    let mut first = json!({"id":"answer","object":"chat.completion.chunk","created":1,"model":"internal","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]});
    first["choices"][0]["delta"][if mode == "refusal" {
        "refusal"
    } else {
        "content"
    }] = json!(if mode == "refusal" {
        "Cannot help"
    } else {
        "Hello"
    });
    let last = json!({"id":"answer","object":"chat.completion.chunk","created":1,"model":"internal","choices":[{"index":0,"delta":{},"finish_reason":if mode == "refusal" {"stop"} else {"content_filter"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}});
    format!("data: {first}\n\ndata: {last}\n\ndata: [DONE]\n\n")
}

#[tokio::test]
async fn responses_and_chat_preserve_refusals_and_content_filter_terminals() {
    for (upstream_format, ingress) in [("openai", "responses"), ("responses", "openai")] {
        for mode in ["refusal", "filtered"] {
            let fixture = upstream(upstream_format, mode).await;
            let runtime = runtime(
                &fixture,
                upstream_format,
                ConcurrencyLimit::new(1).unwrap(),
                Options::default(),
            );
            for streaming in [false, true] {
                let result = runtime
                    .handle(request(ingress, streaming), CancellationToken::new())
                    .await;
                assert_eq!(
                    result.status(),
                    StatusCode::OK,
                    "{upstream_format}/{mode}/{streaming}"
                );
                let bytes = to_bytes(result.into_body(), 1024 * 1024).await.unwrap();
                let value: Value = if streaming {
                    let events = sse_values(&bytes);
                    if ingress == "responses" {
                        if mode == "refusal" {
                            let refusal: String = events
                                .iter()
                                .filter(|v| v["type"] == "response.refusal.delta")
                                .map(|v| v["delta"].as_str().unwrap())
                                .collect();
                            assert_eq!(refusal, "Cannot help");
                        }
                        let terminal = events.last().unwrap();
                        assert_eq!(
                            terminal["type"],
                            if mode == "refusal" {
                                "response.completed"
                            } else {
                                "response.incomplete"
                            }
                        );
                        terminal["response"].clone()
                    } else {
                        assert!(String::from_utf8_lossy(&bytes).contains("[DONE]"));
                        if mode == "refusal" {
                            let refusal: String = events
                                .iter()
                                .filter_map(|v| v["choices"][0]["delta"]["refusal"].as_str())
                                .collect();
                            assert_eq!(refusal, "Cannot help");
                        }
                        assert!(events.iter().any(|v| v["choices"][0]["finish_reason"]
                            == if mode == "refusal" {
                                "stop"
                            } else {
                                "content_filter"
                            }));
                        continue;
                    }
                } else {
                    serde_json::from_slice(&bytes).unwrap()
                };
                if ingress == "responses" {
                    assert_eq!(value["model"], "public");
                    if mode == "refusal" {
                        assert_eq!(
                            value["output"][0]["content"],
                            json!([{"type":"refusal","refusal":"Cannot help"}])
                        );
                    } else {
                        assert_eq!(value["status"], "incomplete");
                        assert_eq!(
                            value["incomplete_details"],
                            json!({"reason":"content_filter"})
                        );
                        assert_eq!(value["output"][0]["content"][0]["text"], "Hello");
                    }
                } else if mode == "refusal" {
                    let message = &value["choices"][0]["message"];
                    assert_eq!(message["refusal"], "Cannot help");
                } else {
                    assert_eq!(value["choices"][0]["finish_reason"], "content_filter");
                }
            }
        }
    }
}

async fn result_request(
    format: &str,
    streaming: bool,
    result: Value,
    error: bool,
) -> Request<Body> {
    let (parts, body) = tool_request(format, streaming).await.into_parts();
    let mut value: Value = serde_json::from_slice(&to_bytes(body, 65536).await.unwrap()).unwrap();
    match format {
        "openai" => value["messages"][2]["content"] = result,
        "responses" => value["input"][2]["output"] = result,
        "anthropic" => {
            value["messages"][2]["content"][0]["content"] = result;
            if error {
                value["messages"][2]["content"][0]["is_error"] = json!(true);
            }
        }
        _ => value["contents"][2]["parts"][0]["functionResponse"]["response"] = result,
    }
    Request::from_parts(parts, Body::from(value.to_string()))
}

#[tokio::test]
async fn tool_result_arrays_preserve_blocks_or_reject_incompatible_destinations() {
    for target in FORMATS {
        let fixture = upstream(target, "normal").await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let gateway = runtime(&fixture, target, limit.clone(), Options::default());
        for source in ["openai", "anthropic", "responses"] {
            for empty in [false, true] {
                let texts = if empty {
                    vec![]
                } else {
                    vec!["first", "second"]
                };
                let parts:Vec<_> = texts.iter().map(|text|json!({"type":if source=="responses" {"input_text"}else{"text"},"text":text})).collect();
                for streaming in [false, true] {
                    let before = fixture.calls.lock().unwrap().len();
                    let response = gateway
                        .handle(
                            result_request(source, streaming, json!(parts), false).await,
                            CancellationToken::new(),
                        )
                        .await;
                    if target == "gemini" && !empty {
                        assert_eq!(
                            response.status(),
                            StatusCode::BAD_REQUEST,
                            "{source} -> {target}"
                        );
                        to_bytes(response.into_body(), 65536).await.unwrap();
                        assert_eq!(fixture.calls.lock().unwrap().len(), before);
                        assert_eq!(limit.available(), 1);
                        continue;
                    }
                    assert_eq!(
                        response.status(),
                        StatusCode::OK,
                        "{source} -> {target}, empty={empty}"
                    );
                    let body = to_bytes(response.into_body(), 65536).await.unwrap();
                    let result = if streaming {
                        stream_snapshot(source, &body)
                    } else {
                        snapshot(source, &serde_json::from_slice(&body).unwrap())
                    };
                    assert_eq!(
                        result["text"], "Hello",
                        "{source} -> {target}, empty={empty}, streaming={streaming}"
                    );
                    assert_eq!(limit.available(), 1);
                    let calls = fixture.calls.lock().unwrap();
                    let body = &calls.last().unwrap()["body"];
                    let blocks:Vec<_> = texts.iter().map(|text|json!({"type":if target=="responses" {"input_text"}else{"text"},"text":text})).collect();
                    match target {
                        "openai" => assert_eq!(body["messages"][2]["content"], json!(blocks)),
                        "responses" => assert_eq!(body["input"][2]["output"], json!(blocks)),
                        "anthropic" => {
                            assert_eq!(body["messages"][2]["content"][0]["content"], json!(blocks))
                        }
                        _ => assert_eq!(
                            body["contents"][2]["parts"][0]["functionResponse"]["response"],
                            json!({"result":""})
                        ),
                    }
                }
            }
        }
    }
}

#[tokio::test]
async fn explicit_tool_errors_are_preserved_only_by_capable_backends() {
    for target in FORMATS {
        let fixture = upstream(target, "normal").await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let gateway = runtime(&fixture, target, limit.clone(), Options::default());
        for streaming in [false, true] {
            let response = gateway
                .handle(
                    result_request(
                        "anthropic",
                        streaming,
                        json!([{"type":"text","text":"Tool failed"}]),
                        true,
                    )
                    .await,
                    CancellationToken::new(),
                )
                .await;
            if target == "anthropic" {
                assert_eq!(response.status(), StatusCode::OK);
                let body = to_bytes(response.into_body(), 65536).await.unwrap();
                assert!(String::from_utf8_lossy(&body).contains("Hello"));
                let calls = fixture.calls.lock().unwrap();
                let result = &calls.last().unwrap()["body"]["messages"][2]["content"][0];
                assert_eq!(result["is_error"], true);
                assert_eq!(
                    result["content"],
                    json!([{"type":"text","text":"Tool failed"}])
                );
            } else {
                assert_eq!(response.status(), StatusCode::BAD_REQUEST, "{target}");
                to_bytes(response.into_body(), 65536).await.unwrap();
                assert!(fixture.calls.lock().unwrap().is_empty());
            }
            assert_eq!(limit.available(), 1);
        }
    }
}

#[tokio::test]
async fn gemini_result_objects_keep_business_data_without_inventing_error_status() {
    let object =
        json!({"error":{"code":0,"description":"a business field"},"nested":[true,3,null]});
    for target in FORMATS {
        let fixture = upstream(target, "normal").await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let gateway = runtime(&fixture, target, limit.clone(), Options::default());
        let response = gateway
            .handle(
                result_request("gemini", false, object.clone(), false).await,
                CancellationToken::new(),
            )
            .await;
        assert_eq!(response.status(), StatusCode::OK, "{target}");
        to_bytes(response.into_body(), 65536).await.unwrap();
        let calls = fixture.calls.lock().unwrap();
        let body = &calls[0]["body"];
        let text = match target {
            "openai" => body["messages"][2]["content"].as_str().unwrap(),
            "responses" => body["input"][2]["output"].as_str().unwrap(),
            "anthropic" => {
                assert!(body["messages"][2]["content"][0].get("is_error").is_none());
                body["messages"][2]["content"][0]["content"][0]["text"]
                    .as_str()
                    .unwrap()
            }
            _ => {
                assert_eq!(
                    body["contents"][2]["parts"][0]["functionResponse"]["response"],
                    object
                );
                continue;
            }
        };
        assert_eq!(serde_json::from_str::<Value>(text).unwrap(), object);
    }
}

fn schema_pointer(format: &str) -> &'static str {
    match format {
        "openai" => "/tools/0/function/parameters",
        "responses" => "/tools/0/parameters",
        "anthropic" => "/tools/0/input_schema",
        _ => "/tools/0/functionDeclarations/0/parametersJsonSchema",
    }
}

async fn schema_request(
    format: &str,
    streaming: bool,
    schema: &Value,
    strict: bool,
    native: bool,
) -> Request<Body> {
    let (parts, body) = tool_request(format, streaming).await.into_parts();
    let mut value: Value = serde_json::from_slice(&to_bytes(body, 65536).await.unwrap()).unwrap();
    *value.pointer_mut(schema_pointer(format)).unwrap() = schema.clone();
    match format {
        "openai" => value["tools"][0]["function"]["strict"] = json!(strict),
        "responses" => value["tools"][0]["strict"] = json!(strict),
        "gemini" if native => {
            let f = &mut value["tools"][0]["functionDeclarations"][0];
            f.as_object_mut().unwrap().remove("parametersJsonSchema");
            f["parameters"] = schema.clone();
        }
        _ => {}
    }
    Request::from_parts(parts, Body::from(value.to_string()))
}

#[tokio::test]
async fn function_schemas_preserve_constraints_and_strict_backend_eligibility() {
    let schema = json!({"type":"object","$defs":{"query":{"type":"string","pattern":"^[a-z]+$"}},
        "properties":{"query":{"$ref":"#/$defs/query"}},"required":["query"],"additionalProperties":false,
        "default":{"type":"literal","nullable":true}});
    for target in FORMATS {
        let fixture = upstream(target, "normal").await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let gateway = runtime(&fixture, target, limit.clone(), Options::default());
        for source in FORMATS {
            for strict in [false, true] {
                if strict && !matches!(source, "openai" | "responses") {
                    continue;
                }
                for streaming in [false, true] {
                    let before = fixture.calls.lock().unwrap().len();
                    let response = gateway
                        .handle(
                            schema_request(source, streaming, &schema, strict, false).await,
                            CancellationToken::new(),
                        )
                        .await;
                    let allowed = !strict || matches!(target, "openai" | "responses");
                    assert_eq!(
                        response.status(),
                        if allowed {
                            StatusCode::OK
                        } else {
                            StatusCode::BAD_REQUEST
                        },
                        "{source} -> {target}, strict={strict}"
                    );
                    let body = to_bytes(response.into_body(), 65536).await.unwrap();
                    assert_eq!(limit.available(), 1);
                    let calls = fixture.calls.lock().unwrap();
                    assert_eq!(calls.len(), before + usize::from(allowed));
                    if !allowed {
                        continue;
                    }
                    let result = if streaming {
                        stream_snapshot(source, &body)
                    } else {
                        snapshot(source, &serde_json::from_slice(&body).unwrap())
                    };
                    assert_eq!(result["text"], "Hello");
                    let sent = &calls.last().unwrap()["body"];
                    assert_eq!(sent.pointer(schema_pointer(target)).unwrap(), &schema);
                    match target {
                        "responses" => assert_eq!(sent["tools"][0]["strict"], strict),
                        "openai" if strict => {
                            assert_eq!(sent["tools"][0]["function"]["strict"], true)
                        }
                        "anthropic" => assert!(sent["tools"][0].get("strict").is_none()),
                        "gemini" => assert!(
                            sent["tools"][0]["functionDeclarations"][0]
                                .get("strict")
                                .is_none()
                        ),
                        _ => {}
                    }
                }
            }
        }
    }
}

#[tokio::test]
async fn gemini_schema_normalization_and_rejections_precede_network_dispatch() {
    let schema = json!({"type":"OBJECT","properties":{"query":{"type":"STRING","minLength":"2","nullable":true},"items":{"type":"ARRAY","minItems":"1","items":{"type":"INTEGER","minimum":0}}},"required":["query"]});
    let expected = json!({"type":"object","properties":{"query":{"type":["string","null"],"minLength":2},"items":{"type":"array","minItems":1,"items":{"type":"integer","minimum":0}}},"required":["query"]});
    for target in FORMATS {
        let fixture = upstream(target, "normal").await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let gateway = runtime(&fixture, target, limit.clone(), Options::default());
        for streaming in [false, true] {
            let response = gateway
                .handle(
                    schema_request("gemini", streaming, &schema, false, true).await,
                    CancellationToken::new(),
                )
                .await;
            assert_eq!(response.status(), StatusCode::OK, "{target}");
            let body = to_bytes(response.into_body(), 65536).await.unwrap();
            let result = if streaming {
                stream_snapshot("gemini", &body)
            } else {
                snapshot("gemini", &serde_json::from_slice(&body).unwrap())
            };
            assert_eq!(result["text"], "Hello");
            assert_eq!(
                fixture.calls.lock().unwrap().last().unwrap()["body"]
                    .pointer(schema_pointer(target))
                    .unwrap(),
                &expected
            );
            assert_eq!(limit.available(), 1);
        }
        let before = fixture.calls.lock().unwrap().len();
        for bad in [
            json!({"type":"OBJECT","propertyOrdering":["query"]}),
            json!({"type":"OBJECT","properties":{"x":{"type":"STRING","nullable":true,"enum":["x"]}}}),
            json!({"type":"OBJECT","properties":{"x":{"type":"ARRAY","minItems":"invalid"}}}),
        ] {
            let response = gateway
                .handle(
                    schema_request("gemini", false, &bad, false, true).await,
                    CancellationToken::new(),
                )
                .await;
            assert_eq!(response.status(), StatusCode::BAD_REQUEST);
            to_bytes(response.into_body(), 65536).await.unwrap();
            assert_eq!(fixture.calls.lock().unwrap().len(), before);
            assert_eq!(limit.available(), 1);
        }
    }
}

const IMAGE_PNG: &str =
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC";
fn image_content(format: &str, remote: bool, detail: Option<&str>) -> Value {
    let url = if remote {
        "https://images.example.test/image.png?signature=opaque".into()
    } else {
        format!("data:image/png;base64,{IMAGE_PNG}")
    };
    let (before, mut image, after) = match format {
        "openai" => (
            json!({"type":"text","text":"before"}),
            json!({"type":"image_url","image_url":{"url":url}}),
            json!({"type":"text","text":"after"}),
        ),
        "responses" => (
            json!({"type":"input_text","text":"before"}),
            json!({"type":"input_image","image_url":url}),
            json!({"type":"input_text","text":"after"}),
        ),
        "anthropic" => (
            json!({"type":"text","text":"before"}),
            json!({"type":"image","source":if remote {json!({"type":"url","url":url})} else {json!({"type":"base64","media_type":"image/png","data":IMAGE_PNG})}}),
            json!({"type":"text","text":"after"}),
        ),
        _ => (
            json!({"text":"before"}),
            json!({"inlineData":{"mimeType":"image/png","data":IMAGE_PNG}}),
            json!({"text":"after"}),
        ),
    };
    if let Some(detail) = detail {
        match format {
            "openai" => image["image_url"]["detail"] = json!(detail),
            "responses" => image["detail"] = json!(detail),
            _ => {}
        }
    }
    json!([before, image, after])
}
fn input_image_pointer(format: &str) -> &'static str {
    match format {
        "responses" => "/input/0/content",
        "gemini" => "/contents/0/parts",
        _ => "/messages/0/content",
    }
}
async fn image_request(
    format: &str,
    streaming: bool,
    remote: bool,
    detail: Option<&str>,
) -> Request<Body> {
    let (parts, body) = request(format, streaming).into_parts();
    let mut value: Value = serde_json::from_slice(&to_bytes(body, 65536).await.unwrap()).unwrap();
    if format == "responses" {
        value["input"] = json!([{"role":"user","content":[]}]);
    }
    *value.pointer_mut(input_image_pointer(format)).unwrap() =
        image_content(format, remote, detail);
    Request::from_parts(parts, Body::from(value.to_string()))
}
#[tokio::test]
async fn image_inputs_preserve_content_order_and_bytes_without_fetching_urls() {
    for target in FORMATS {
        let fixture = upstream(target, "normal").await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let gateway = runtime(&fixture, target, limit.clone(), Options::default());
        for source in FORMATS {
            for remote in [false, true] {
                if remote && source == "gemini" {
                    continue;
                }
                for streaming in [false, true] {
                    let before = fixture.calls.lock().unwrap().len();
                    let response = gateway
                        .handle(
                            image_request(source, streaming, remote, None).await,
                            CancellationToken::new(),
                        )
                        .await;
                    let allowed = !(remote && target == "gemini");
                    assert_eq!(
                        response.status(),
                        if allowed {
                            StatusCode::OK
                        } else {
                            StatusCode::BAD_REQUEST
                        },
                        "{source} -> {target}, remote={remote}"
                    );
                    let body = to_bytes(response.into_body(), 65536).await.unwrap();
                    assert_eq!(limit.available(), 1);
                    let calls = fixture.calls.lock().unwrap();
                    assert_eq!(calls.len(), before + usize::from(allowed));
                    if !allowed {
                        continue;
                    }
                    assert_eq!(
                        calls.last().unwrap()["body"]
                            .pointer(input_image_pointer(target))
                            .unwrap(),
                        &image_content(target, remote, None)
                    );
                    let result = if streaming {
                        stream_snapshot(source, &body)
                    } else {
                        snapshot(source, &serde_json::from_slice(&body).unwrap())
                    };
                    assert_eq!(result["text"], "Hello");
                }
            }
        }
    }
}
#[tokio::test]
async fn image_detail_is_preserved_or_rejected_before_dispatch() {
    for target in FORMATS {
        let fixture = upstream(target, "normal").await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let gateway = runtime(&fixture, target, limit.clone(), Options::default());
        for source in ["openai", "responses"] {
            for detail in ["auto", "low", "high", "original"] {
                let before = fixture.calls.lock().unwrap().len();
                let response = gateway
                    .handle(
                        image_request(source, false, false, Some(detail)).await,
                        CancellationToken::new(),
                    )
                    .await;
                let allowed = detail == "auto" || matches!(target, "openai" | "responses");
                assert_eq!(
                    response.status(),
                    if allowed {
                        StatusCode::OK
                    } else {
                        StatusCode::BAD_REQUEST
                    },
                    "{source} -> {target}, detail={detail}"
                );
                to_bytes(response.into_body(), 65536).await.unwrap();
                assert_eq!(limit.available(), 1);
                let calls = fixture.calls.lock().unwrap();
                assert_eq!(calls.len(), before + usize::from(allowed));
                if allowed {
                    assert_eq!(
                        calls.last().unwrap()["body"]
                            .pointer(input_image_pointer(target))
                            .unwrap(),
                        &image_content(target, false, Some(detail))
                    );
                }
            }
        }
    }
}

#[tokio::test]
async fn cache_controls_survive_openai_api_conversion_and_filter_other_targets() {
    for target in FORMATS {
        let fixture = upstream(target, "normal").await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let gateway = runtime(&fixture, target, limit.clone(), Options::default());
        for source in ["openai", "responses"] {
            for streaming in [false, true] {
                for (field, control, valid) in [
                    ("prompt_cache_retention", json!("in_memory"), true),
                    ("prompt_cache_retention", json!("24h"), true),
                    ("prompt_cache_retention", Value::Null, true),
                    ("prompt_cache_retention", json!("1h"), false),
                    ("prompt_cache_retention", json!(24), false),
                    (
                        "prompt_cache_options",
                        json!({"mode":"implicit","ttl":"30m"}),
                        true,
                    ),
                    (
                        "prompt_cache_options",
                        json!({"mode":"explicit","ttl":"30m"}),
                        true,
                    ),
                    ("prompt_cache_options", json!({}), true),
                    ("prompt_cache_options", Value::Null, true),
                    ("prompt_cache_options", json!({"ttl":"24h"}), false),
                    ("prompt_cache_options", json!({"mode":"auto"}), false),
                ] {
                    let (parts, body) = request(source, streaming).into_parts();
                    let mut value: Value =
                        serde_json::from_slice(&to_bytes(body, 65536).await.unwrap()).unwrap();
                    value[field] = control.clone();
                    if streaming && source == "openai" {
                        value["stream_options"] = json!({"include_usage":true});
                    }
                    let before = fixture.calls.lock().unwrap().len();
                    let response = gateway
                        .handle(
                            Request::from_parts(parts, Body::from(value.to_string())),
                            CancellationToken::new(),
                        )
                        .await;
                    let allowed =
                        control.is_null() || (valid && matches!(target, "openai" | "responses"));
                    assert_eq!(
                        response.status(),
                        if allowed {
                            StatusCode::OK
                        } else {
                            StatusCode::BAD_REQUEST
                        },
                        "{source} -> {target}, stream={streaming}, {field}={control}"
                    );
                    let bytes = to_bytes(response.into_body(), 65536).await.unwrap();
                    assert_eq!(limit.available(), 1);
                    let calls = fixture.calls.lock().unwrap();
                    assert_eq!(calls.len(), before + usize::from(allowed));
                    if allowed {
                        let result = if streaming {
                            stream_snapshot(source, &bytes)
                        } else {
                            snapshot(source, &serde_json::from_slice(&bytes).unwrap())
                        };
                        assert_eq!(result["text"], "Hello");
                        let call = calls.last().unwrap();
                        assert_eq!(call["body"][field], control);
                        if field == "prompt_cache_options" && !control.is_null() {
                            assert_eq!(result["usage"], json!([3, 2, 5]));
                            let values = if streaming {
                                sse_values(&bytes)
                            } else {
                                vec![serde_json::from_slice(&bytes).unwrap()]
                            };
                            let usage = values
                                .iter()
                                .rev()
                                .find_map(|v| {
                                    let u = if streaming && source == "responses" {
                                        &v["response"]["usage"]
                                    } else {
                                        &v["usage"]
                                    };
                                    u.is_object().then_some(u)
                                })
                                .unwrap();
                            let details = if source == "responses" {
                                "input_tokens_details"
                            } else {
                                "prompt_tokens_details"
                            };
                            assert_eq!(usage[details]["cached_tokens"], 1);
                            assert_eq!(usage[details]["cache_write_tokens"], 2);
                        }
                        assert!(!call["headers"].to_string().contains("client-secret"));
                        if control.is_null() {
                            assert!(call["body"].get(field).is_none());
                        }
                    }
                }
            }
        }
    }
}

#[tokio::test]
async fn cache_write_usage_settles_once_even_when_chat_stream_usage_is_hidden() {
    for target in ["openai", "responses"] {
        let fixture = upstream(target, "normal").await;
        for streaming in [false, true] {
            let mut provider = json!({"kind":"openai","base_url":format!("{}/v1",fixture.base)});
            if target == "responses" {
                provider["api"] = json!("responses");
            }
            let config = serde_json::from_value(json!({"providers":{"p":provider},"models":{"public":{"provider":"p","upstream_model":"internal","workloads":["chat"],"subjects":["alice"],"quota":{"total_tokens":10,"reserve_tokens":1}}}})).unwrap();
            let limit = ConcurrencyLimit::new(1).unwrap();
            let gateway = Runtime::new(
                config,
                Arc::new(
                    ApiKeys::new(vec![ApiKey {
                        id: "alice".into(),
                        secret: "client-secret".into(),
                    }])
                    .unwrap(),
                ),
                limit.clone(),
                Options::default(),
            )
            .unwrap();
            let before = fixture.calls.lock().unwrap().len();
            for i in 0..3 {
                let (parts, body) = request("openai", streaming).into_parts();
                let mut body: Value =
                    serde_json::from_slice(&to_bytes(body, 65536).await.unwrap()).unwrap();
                body["prompt_cache_options"] = json!({"mode":"implicit","ttl":"30m"});
                let response = gateway
                    .handle(
                        Request::from_parts(parts, Body::from(body.to_string())),
                        CancellationToken::new(),
                    )
                    .await;
                assert_eq!(
                    response.status(),
                    if i < 2 {
                        StatusCode::OK
                    } else {
                        StatusCode::TOO_MANY_REQUESTS
                    }
                );
                let bytes = to_bytes(response.into_body(), 65536).await.unwrap();
                if i < 2 && streaming {
                    assert!(String::from_utf8_lossy(&bytes).contains("[DONE]"));
                    assert!(sse_values(&bytes).iter().all(|v| v.get("usage").is_none()));
                }
                assert_eq!(limit.available(), 1);
            }
            assert_eq!(fixture.calls.lock().unwrap().len(), before + 2);
        }
    }
}
