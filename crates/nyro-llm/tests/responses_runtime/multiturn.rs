//! Three real Runtime requests per conversation. Client history is built from the
//! returned wire bodies, never from a codec, IR value, or replacement fixture.
use super::*;

const MODES: [[bool; 3]; 4] = [
    [false, false, false],
    [true, true, true],
    [false, true, false],
    [true, false, true],
];

fn history_key(format: &str) -> &'static str {
    match format {
        "responses" => "input",
        "gemini" => "contents",
        _ => "messages",
    }
}

fn assistant_history(format: &str, response: &Value) -> Vec<Value> {
    match format {
        "responses" => response["output"].as_array().unwrap().clone(),
        "openai" => vec![response["choices"][0]["message"].clone()],
        "anthropic" => vec![json!({"role":"assistant","content":response["content"]})],
        "gemini" => vec![response["candidates"][0]["content"].clone()],
        _ => unreachable!(),
    }
}

fn reply(format: &str, round: usize, interleaved: bool, reasoning: bool) -> Value {
    let mut reply = response(format);
    if round == 2 {
        return reply;
    }
    let count = if round == 0 { 2 } else { 1 };
    let mut blocks = vec![];
    if reasoning {
        blocks.push(match format {
            "anthropic" => json!({"type":"thinking","thinking":format!("summary-{round}"),"signature":format!("opaque-signature-{round}")}),
            "responses" => json!({"type":"reasoning","id":format!("rs_{round}"),"summary":[{"type":"summary_text","text":format!("summary-{round}")}],"encrypted_content":format!("opaque-final-{round}")}),
            "gemini" => json!({"text":format!("summary-{round}"),"thought":true}),
            _ => unreachable!(),
        });
    }
    for index in 0..count {
        if interleaved {
            let text = format!("round-{round}-text-{index}");
            blocks.push(match format {
                "anthropic" => json!({"type":"text","text":text}),
                "responses" => json!({"type":"message","id":format!("msg_{round}_{index}"),"role":"assistant","status":"completed","content":[{"type":"output_text","text":text,"annotations":[]}]}),
                "gemini" => json!({"text":text}),
                _ => unreachable!(),
            });
        }
        let id = format!("call-{round}-{index}");
        let args = json!({"query":format!("query-{round}-{index}")});
        blocks.push(match format {
            "openai" => json!({"id":id,"type":"function","function":{"name":"lookup","arguments":args.to_string()}}),
            "anthropic" => json!({"type":"tool_use","id":id,"name":"lookup","input":args}),
            "responses" => json!({"type":"function_call","id":format!("fc_{round}_{index}"),"call_id":id,"name":"lookup","arguments":args.to_string(),"status":"completed"}),
            "gemini" => {
                let mut part = json!({"functionCall":{"id":id,"name":"lookup","args":args}});
                if reasoning {
                    part["thoughtSignature"] = json!(match (round, index) {
                        (0, 0) => "c2lnLTAtMA==",
                        (0, 1) => "c2lnLTAtMQ==",
                        (1, 0) => "c2lnLTEtMA==",
                        _ => unreachable!(),
                    });
                }
                part
            }
            _ => unreachable!(),
        });
    }
    match format {
        "openai" => {
            reply["choices"][0]["message"] =
                json!({"role":"assistant","content":null,"tool_calls":blocks});
            reply["choices"][0]["finish_reason"] = json!("tool_calls");
        }
        "anthropic" => {
            reply["content"] = json!(blocks);
            reply["stop_reason"] = json!("tool_use");
        }
        "responses" => reply["output"] = json!(blocks),
        "gemini" => reply["candidates"][0]["content"]["parts"] = json!(blocks),
        _ => unreachable!(),
    }
    reply
}

async fn session_upstream(format: &'static str, interleaved: bool, reasoning: bool) -> Fixture {
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
                serde_json::from_slice(&to_bytes(request.into_body(), 1_048_576).await.unwrap())
                    .unwrap();
            let streaming = body["stream"] == true || path.contains(":streamGenerateContent");
            let round = {
                let mut calls = observed.lock().unwrap();
                let round = calls.len();
                calls.push(json!({"path":path,"headers":headers,"body":body}));
                round
            };
            assert!(round < 3, "unexpected upstream dispatch {round}");
            let reply = reply(format, round, interleaved, reasoning);
            let body = if streaming {
                let chunks: Vec<_> = response_frames(format, &reply)
                    .into_bytes()
                    .chunks(7)
                    .map(|chunk| Ok::<_, std::io::Error>(chunk.to_vec()))
                    .collect();
                Body::from_stream(futures::stream::iter(chunks))
            } else {
                Body::from(reply.to_string())
            };
            axum::http::Response::builder()
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
    let base = format!("http://{}", listener.local_addr().unwrap());
    let task = tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
    Fixture { base, calls, task }
}

async fn initial(format: &str, reasoning: bool) -> Value {
    let (_, body) = tool_request(format, false).await.into_parts();
    let mut body: Value = serde_json::from_slice(&to_bytes(body, 65536).await.unwrap()).unwrap();
    body[history_key(format)]
        .as_array_mut()
        .unwrap()
        .truncate(1);
    if reasoning {
        match format {
            "anthropic" => body["thinking"] = json!({"type":"adaptive"}),
            "responses" => {
                body["reasoning"] = json!({"effort":"low","summary":"auto"});
                body["include"] = json!(["reasoning.encrypted_content"]);
            }
            "gemini" => {
                body["generationConfig"]["thinkingConfig"] = json!({"includeThoughts":true})
            }
            _ => unreachable!(),
        }
    }
    body
}

fn http_request(format: &str, streaming: bool, body: &Value) -> Request<Body> {
    let (parts, _) = request(format, streaming).into_parts();
    let mut body = body.clone();
    if format != "gemini" {
        body["stream"] = json!(streaming);
    }
    if format == "openai" && streaming {
        body["stream_options"] = json!({"include_usage":true});
    }
    Request::from_parts(parts, Body::from(body.to_string()))
}

// A test-only wire transcript: keep every ordered part; normalize only envelope
// IDs/status, textual JSON encoding and a single text-result block, not media
// block boundaries or opaque state. Empty unsigned text stream fragments
// contribute no text and are ignored.
fn text_result(value: &Value) -> Value {
    if let Some(parts) = value.as_array()
        && parts.len() == 1
        && parts[0].as_object().is_some_and(|part| part.len() == 2)
        && matches!(parts[0]["type"].as_str(), Some("text" | "input_text"))
    {
        return parts[0]["text"].clone();
    }
    value.clone()
}

fn transcript(format: &str, history: &[Value]) -> Vec<Value> {
    let mut items = vec![];
    for message in history {
        if format == "responses" {
            match message["type"].as_str() {
                Some("function_call") => {
                    items.push(json!([
                        "call",
                        message["call_id"],
                        message["name"],
                        serde_json::from_str::<Value>(message["arguments"].as_str().unwrap())
                            .unwrap()
                    ]));
                    continue;
                }
                Some("function_call_output") => {
                    items.push(json!([
                        "result",
                        message["call_id"],
                        text_result(&message["output"])
                    ]));
                    continue;
                }
                Some("reasoning") => {
                    items.push(json!(["reasoning", message]));
                    continue;
                }
                _ => {}
            }
        }
        if format == "openai" && message["role"] == "tool" {
            items.push(json!([
                "result",
                message["tool_call_id"],
                message["content"]
            ]));
            continue;
        }
        let content = &message[if format == "gemini" {
            "parts"
        } else {
            "content"
        }];
        if let Some(text) = content.as_str()
            && !text.is_empty()
        {
            items.push(json!([message["role"], text]));
        }
        for part in content.as_array().into_iter().flatten() {
            match format {
                "anthropic" if part["type"] == "tool_use" => {
                    assert_eq!(message["role"], "assistant");
                    items.push(json!(["call", part["id"], part["name"], part["input"]]))
                }
                "anthropic" if part["type"] == "tool_result" => {
                    assert_eq!(message["role"], "user");
                    items.push(json!([
                        "result",
                        part["tool_use_id"],
                        text_result(&part["content"])
                    ]));
                }
                "anthropic" if part["type"] == "thinking" => {
                    assert_eq!(message["role"], "assistant");
                    items.push(json!(["reasoning", part]));
                }
                "gemini" if part.get("functionCall").is_some() => {
                    assert_eq!(message["role"], "model");
                    let call = &part["functionCall"];
                    items.push(json!(["call", call["id"], call["name"], call["args"]]));
                    if part.get("thoughtSignature").is_some() {
                        items.push(json!(["signature", part["thoughtSignature"]]));
                    }
                }
                "gemini" if part.get("functionResponse").is_some() => {
                    assert_eq!(message["role"], "user");
                    let result = &part["functionResponse"];
                    assert_eq!(result["name"], "lookup");
                    items.push(json!([
                        "result",
                        result["id"],
                        if result.get("parts").is_some() {
                            json!({"response":result["response"],"parts":result["parts"]})
                        } else {
                            json!(result["response"].to_string())
                        }
                    ]));
                }
                "gemini" if part["thought"] == true => {
                    assert_eq!(message["role"], "model");
                    items.push(json!(["reasoning", part]));
                }
                _ if part == &json!({"text":""}) || part == &json!({"type":"text","text":""}) => {}
                _ => items.push(json!([
                    if message["role"] == "model" {
                        json!("assistant")
                    } else {
                        message["role"].clone()
                    },
                    part["text"]
                ])),
            }
        }
        for call in message["tool_calls"].as_array().into_iter().flatten() {
            assert_eq!(message["role"], "assistant");
            items.push(json!([
                "call",
                call["id"],
                call["function"]["name"],
                serde_json::from_str::<Value>(call["function"]["arguments"].as_str().unwrap())
                    .unwrap()
            ]));
        }
    }
    items
}

fn result_payload(format: &str, query: &str, images: bool) -> Value {
    if !images {
        return json!(json!({"result":query}).to_string());
    }
    if format == "gemini" {
        return json!({"response":{"result":query,"image":{"$ref":"screen.png"}},"parts":[{"inlineData":{"mimeType":"image/png","data":TOOL_PNG,"displayName":"screen.png"}}]});
    }
    let mut blocks = image_result_blocks(format);
    blocks[0]["text"] = json!(query);
    blocks
}

fn append_results(format: &str, history: &mut Vec<Value>, calls: &[Value], images: bool) {
    let mut results = vec![];
    // Deliberately complete the parallel tools in the opposite order.
    for call in calls.iter().rev() {
        assert_eq!(call[2], "lookup");
        let payload = result_payload(format, call[3]["query"].as_str().unwrap(), images);
        results.push(match format {
            "openai" => json!({"role":"tool","tool_call_id":call[1],"content":payload}),
            "responses" => json!({"type":"function_call_output","call_id":call[1],"output":payload}),
            "anthropic" => json!({"type":"tool_result","tool_use_id":call[1],"content":payload}),
            "gemini" => {
                let mut result = json!({"id":call[1],"name":call[2],"response":if images {payload["response"].clone()} else {serde_json::from_str::<Value>(payload.as_str().unwrap()).unwrap()}});
                if images { result["parts"] = payload["parts"].clone(); }
                json!({"functionResponse":result})
            }
            _ => unreachable!(),
        });
    }
    match format {
        "anthropic" => history.push(json!({"role":"user","content":results})),
        "gemini" => history.push(json!({"role":"user","parts":results})),
        _ => history.extend(results),
    }
}

async fn turn(
    gateway: &Runtime,
    format: &str,
    streaming: bool,
    body: &Value,
    limit: &ConcurrencyLimit,
) -> Value {
    let result = gateway
        .handle(
            http_request(format, streaming, body),
            CancellationToken::new(),
        )
        .await;
    let status = result.status();
    let bytes = to_bytes(result.into_body(), 1_048_576).await.unwrap();
    assert_eq!(
        status,
        StatusCode::OK,
        "{format} stream={streaming}: {}",
        String::from_utf8_lossy(&bytes)
    );
    assert_eq!(limit.available(), 1);
    if streaming {
        stream_response(format, &bytes)
    } else {
        serde_json::from_slice(&bytes).unwrap()
    }
}

fn assert_usage(format: &str, reply: &Value) {
    let usage = match format {
        "openai" => json!([
            reply["usage"]["prompt_tokens"],
            reply["usage"]["completion_tokens"],
            reply["usage"]["total_tokens"]
        ]),
        "responses" => json!([
            reply["usage"]["input_tokens"],
            reply["usage"]["output_tokens"],
            reply["usage"]["total_tokens"]
        ]),
        "anthropic" => json!([
            reply["usage"]["input_tokens"],
            reply["usage"]["output_tokens"],
            5
        ]),
        _ => json!([
            reply["usageMetadata"]["promptTokenCount"],
            reply["usageMetadata"]["candidatesTokenCount"],
            reply["usageMetadata"]["totalTokenCount"]
        ]),
    };
    assert_eq!(usage, json!([3, 2, 5]));
}

async fn conversation(
    source: &'static str,
    target: &'static str,
    modes: [bool; 3],
    images: bool,
    reasoning: bool,
    native: bool,
) {
    let interleaved = (images || reasoning) && source != "openai" && target != "openai";
    let fixture = session_upstream(target, interleaved, reasoning).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway = runtime_with_native(&fixture, target, limit.clone(), Options::default(), native);
    let mut body = initial(source, reasoning).await;
    let mut expected_history = vec![json!(["user", "Hi"])];
    for (round, streaming) in modes.into_iter().enumerate() {
        let output = turn(&gateway, source, streaming, &body, &limit).await;
        assert_usage(source, &output);
        match source {
            "openai" => {
                assert_eq!(output["choices"][0]["message"]["role"], "assistant");
                assert_eq!(
                    output["choices"][0]["finish_reason"],
                    if round == 2 { "stop" } else { "tool_calls" }
                );
            }
            "anthropic" => {
                assert_eq!(output["role"], "assistant");
                assert_eq!(
                    output["stop_reason"],
                    if round == 2 { "end_turn" } else { "tool_use" }
                );
            }
            "gemini" => {
                assert_eq!(output["candidates"][0]["content"]["role"], "model");
                assert_eq!(output["candidates"][0]["finishReason"], "STOP");
            }
            "responses" => assert_eq!(output["status"], "completed"),
            _ => unreachable!(),
        }
        assert_eq!(
            output[if source == "gemini" {
                "modelVersion"
            } else {
                "model"
            }],
            if native && source == "gemini" {
                "internal"
            } else {
                "public"
            }
        );
        let received = assistant_history(source, &output);
        let observed = transcript(source, &received);
        let expected = transcript(
            source,
            &assistant_history(source, &reply(source, round, interleaved, reasoning)),
        );
        assert_eq!(
            observed, expected,
            "{source}->{target} {modes:?} round={round}"
        );
        let calls: Vec<_> = observed
            .iter()
            .filter(|v| v[0] == "call")
            .cloned()
            .collect();
        {
            let sent = fixture.calls.lock().unwrap();
            assert_eq!(sent.len(), round + 1);
            let sent = &sent[round];
            if matches!(target, "anthropic" | "gemini") {
                assert_eq!(
                    sent["body"][history_key(target)].as_array().unwrap().len(),
                    1 + round * 2
                );
            }
            assert_eq!(
                transcript(
                    target,
                    sent["body"][history_key(target)].as_array().unwrap()
                ),
                expected_history,
                "{source}->{target} {modes:?} round={round}"
            );
            assert!(!sent.to_string().contains("client-secret"));
            assert_eq!(
                sent["headers"][if target == "anthropic" {
                    "x-api-key"
                } else if target == "gemini" {
                    "x-goog-api-key"
                } else {
                    "authorization"
                }],
                if matches!(target, "openai" | "responses") {
                    "Bearer upstream-secret"
                } else {
                    "upstream-secret"
                }
            );
            if target == "gemini" {
                assert!(sent["path"].as_str().unwrap().contains("/models/internal:"));
            } else {
                assert_eq!(sent["body"]["model"], "internal");
            }
        }
        if round == 2 {
            assert!(calls.is_empty());
            break;
        }
        let history = body[history_key(source)].as_array_mut().unwrap();
        history.extend(received);
        append_results(source, history, &calls, images);
        // The expected upstream transcript uses literal target wire shapes,
        // independent of Nyro's conversion and the client history assembler.
        expected_history.extend(transcript(
            target,
            &assistant_history(target, &reply(target, round, interleaved, reasoning)),
        ));
        for call in calls.iter().rev() {
            expected_history.push(json!([
                "result",
                call[1],
                result_payload(target, call[3]["query"].as_str().unwrap(), images)
            ]));
        }
    }
}

#[tokio::test]
async fn text_tool_sessions_cross_all_four_protocols() {
    for source in FORMATS {
        for target in FORMATS {
            for modes in MODES {
                conversation(source, target, modes, false, false, false).await;
            }
        }
    }
}

#[tokio::test]
async fn image_tool_sessions_keep_interleaved_history() {
    for (source, target) in [
        ("anthropic", "anthropic"),
        ("anthropic", "responses"),
        ("responses", "anthropic"),
        ("responses", "responses"),
        ("gemini", "gemini"),
    ] {
        for modes in MODES {
            conversation(source, target, modes, true, false, false).await;
        }
    }
}

#[tokio::test]
async fn reasoning_and_image_sessions_replay_opaque_state() {
    for format in ["anthropic", "responses", "gemini"] {
        for modes in MODES {
            conversation(format, format, modes, true, true, false).await;
        }
    }
}

#[tokio::test]
async fn native_sessions_replay_tool_results_and_vendor_state() {
    for format in FORMATS {
        for modes in MODES {
            conversation(
                format,
                format,
                modes,
                format != "openai",
                format != "openai",
                true,
            )
            .await;
        }
    }
}

async fn rejected(
    gateway: &Runtime,
    source: &str,
    streaming: bool,
    body: &Value,
    fixture: &Fixture,
    limit: &ConcurrencyLimit,
) {
    let before = fixture.calls.lock().unwrap().len();
    let output = gateway
        .handle(
            http_request(source, streaming, body),
            CancellationToken::new(),
        )
        .await;
    let status = output.status();
    let bytes = to_bytes(output.into_body(), 65536).await.unwrap();
    assert_eq!(
        status,
        StatusCode::BAD_REQUEST,
        "{source} stream={streaming}: {}",
        String::from_utf8_lossy(&bytes)
    );
    assert_eq!(fixture.calls.lock().unwrap().len(), before);
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn invalid_second_turn_batches_reject_without_poisoning_the_session() {
    for source in FORMATS {
        // These destinations require complete, adjacent result batches. OpenAI
        // histories have different validation rules; do not invent a global rule.
        for target in ["anthropic", "gemini"] {
            for streaming in [false, true] {
                let fixture = session_upstream(target, false, false).await;
                let limit = ConcurrencyLimit::new(1).unwrap();
                let gateway = runtime(&fixture, target, limit.clone(), Options::default());
                let mut body = initial(source, false).await;
                let output = turn(&gateway, source, streaming, &body, &limit).await;
                let received = assistant_history(source, &output);
                let calls: Vec<_> = transcript(source, &received)
                    .into_iter()
                    .filter(|v| v[0] == "call")
                    .collect();
                assert_eq!(calls.len(), 2);
                body[history_key(source)]
                    .as_array_mut()
                    .unwrap()
                    .extend(received);
                for fault in ["missing", "duplicate", "unknown"] {
                    let mut broken_calls = calls.clone();
                    match fault {
                        "missing" => {
                            broken_calls.pop();
                        }
                        "duplicate" => broken_calls[1] = broken_calls[0].clone(),
                        "unknown" => broken_calls[0][1] = json!("unknown-call"),
                        _ => unreachable!(),
                    }
                    let mut broken = body.clone();
                    append_results(
                        source,
                        broken[history_key(source)].as_array_mut().unwrap(),
                        &broken_calls,
                        false,
                    );
                    rejected(&gateway, source, !streaming, &broken, &fixture, &limit).await;
                }
                append_results(
                    source,
                    body[history_key(source)].as_array_mut().unwrap(),
                    &calls,
                    false,
                );
                let continued = turn(&gateway, source, !streaming, &body, &limit).await;
                assert_eq!(
                    transcript(source, &assistant_history(source, &continued)),
                    transcript(
                        source,
                        &assistant_history(source, &reply(source, 1, false, false))
                    )
                );
                assert_eq!(fixture.calls.lock().unwrap().len(), 2);
            }
        }
    }
}

#[tokio::test]
async fn second_turn_media_and_opaque_state_filter_incompatible_destinations() {
    for source in ["anthropic", "responses", "gemini"] {
        for reasoning in [false, true] {
            for streaming in [false, true] {
                let original = session_upstream(source, false, reasoning).await;
                let limit = ConcurrencyLimit::new(1).unwrap();
                let gateway = runtime(&original, source, limit.clone(), Options::default());
                let mut body = initial(source, reasoning).await;
                let output = turn(&gateway, source, streaming, &body, &limit).await;
                let received = assistant_history(source, &output);
                let calls: Vec<_> = transcript(source, &received)
                    .into_iter()
                    .filter(|v| v[0] == "call")
                    .collect();
                let history = body[history_key(source)].as_array_mut().unwrap();
                history.extend(received);
                append_results(source, history, &calls, !reasoning);
                // Isolate rejection of returned opaque history from rejection of
                // protocol-specific generation options.
                body.as_object_mut().unwrap().remove("thinking");
                body.as_object_mut().unwrap().remove("reasoning");
                body.as_object_mut().unwrap().remove("include");
                if let Some(config) = body.get_mut("generationConfig") {
                    config.as_object_mut().unwrap().remove("thinkingConfig");
                }
                for target in FORMATS {
                    let compatible = if reasoning || source == "gemini" {
                        source == target
                    } else {
                        matches!(target, "anthropic" | "responses")
                    };
                    if compatible {
                        continue;
                    }
                    let other = session_upstream(target, false, false).await;
                    let other_gateway = runtime(&other, target, limit.clone(), Options::default());
                    rejected(&other_gateway, source, !streaming, &body, &other, &limit).await;
                }
                // The same history still works against its original protocol.
                turn(&gateway, source, !streaming, &body, &limit).await;
                assert_eq!(original.calls.lock().unwrap().len(), 2);
            }
        }
    }
}
