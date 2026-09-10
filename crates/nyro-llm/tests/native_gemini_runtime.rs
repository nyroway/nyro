//! Gemini native fidelity, path routing, EOF validation, and usage over real HTTP.
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
                    .strip_suffix("TRANSPORT_ERROR")
                    .unwrap_or(&text)
                    .as_bytes()
                    .chunks(7)
                    .map(|chunk| Ok::<_, std::io::Error>(chunk.to_vec()))
                    .collect();
                if text.ends_with("TRANSPORT_ERROR") {
                    Body::from_stream(futures::stream::iter(chunks).chain(futures::stream::once(
                        async {
                            // Flush the finish frame before the transport fails.
                            tokio::time::sleep(std::time::Duration::from_millis(20)).await;
                            Err(std::io::Error::other("provider-secret transport failure"))
                        },
                    )))
                } else {
                    Body::from_stream(futures::stream::iter(chunks))
                }
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
    let base = format!("http://{}/v1beta", listener.local_addr().unwrap());
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
fn request(body: Value, stream: bool) -> Request<Body> {
    Request::builder()
        .method("POST")
        .uri(format!(
            "/v1beta/models/public:{}",
            if stream {
                "streamGenerateContent?alt=sse"
            } else {
                "generateContent"
            }
        ))
        .header("content-type", "application/json")
        .header("x-goog-api-key", "client-secret")
        .header("x-private-caller", "caller-metadata")
        .body(Body::from(body.to_string()))
        .unwrap()
}
fn input(extended: bool) -> Value {
    let mut body = json!({"contents":[{"role":"user","parts":[{"text":"Hello"}]}]});
    if extended {
        body["contents"].as_array_mut().unwrap().extend([
            json!({"role":"model","parts":[{"text":"想一想","thought":true,"thoughtSignature":"opaque-signature"},
                {"functionCall":{"name":"weather","args":{"city":"北京"}},"thoughtSignature":"tool-signature"}]}),
            json!({"role":"user","parts":[{"functionResponse":{"name":"weather","response":{"sunny":true}}},
                {"inlineData":{"mimeType":"image/png","data":"aGVsbG8="}},
                {"fileData":{"mimeType":"application/pdf","fileUri":"gs://private/document"}}]})]);
        body["systemInstruction"] = json!({"parts":[{"text":"Be precise"}]});
        body["cachedContent"] = json!("cachedContents/cache-1");
        body["tools"] =
            json!([{"functionDeclarations":[{"name":"weather","parameters":{"type":"OBJECT"}}]}]);
        body["toolConfig"] = json!({"functionCallingConfig":{"mode":"AUTO"}});
        body["safetySettings"] =
            json!([{"category":"HARM_CATEGORY_DANGEROUS_CONTENT","threshold":"BLOCK_ONLY_HIGH"}]);
        body["generationConfig"] = json!({"candidateCount":1,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":1024},"responseMimeType":"application/json","responseSchema":{"type":"OBJECT"},"temperature":0.3});
        body["vendor_option"] = json!({"nested":[true,7,null]});
    }
    body
}
fn usage(output: u64, thoughts: u64) -> Value {
    json!({"promptTokenCount":8,"cachedContentTokenCount":5,"candidatesTokenCount":output,
        "thoughtsTokenCount":thoughts,"totalTokenCount":8+output+thoughts,
        "promptTokensDetails":[{"modality":"TEXT","tokenCount":8}]})
}
fn answer() -> Value {
    json!({"candidates":[{"index":0,"content":{"role":"model","parts":[
        {"text":"想一想","thought":true,"thoughtSignature":"opaque-signature"},
        {"functionCall":{"name":"weather","args":{"city":"北京"}},"thoughtSignature":"tool-signature"}]},
        "finishReason":"STOP","safetyRatings":[{"category":"HARM_CATEGORY_DANGEROUS_CONTENT","probability":"NEGLIGIBLE"}]}],
        "usageMetadata":usage(2,3),"modelVersion":"private-model-version-2026","responseId":"response-1","vendor_field":{"ok":true}})
}
fn frames() -> Vec<Value> {
    vec![
        json!({"candidates":[{"content":{"role":"model","parts":[{"text":"想一想","thought":true,"thoughtSignature":"opaque-signature"}]}}],"usageMetadata":usage(0,1),"modelVersion":"private-model-version-2026"}),
        json!({"candidates":[{"index":0,"content":{"parts":[{"functionCall":{"name":"weather","args":{"city":"北京"}},"thoughtSignature":"tool-signature"}]},"finishReason":"STOP"}],"usageMetadata":usage(1,2)}),
        json!({"usageMetadata":usage(2,3),"responseId":"response-1","vendor_field":{"ok":true}}),
    ]
}
fn sse(frames: &[Value]) -> String {
    frames.iter().map(|v| format!("data: {v}\n\n")).collect()
}
async fn invoke(runtime: &Runtime, body: Value, stream: bool) -> Response<Body> {
    runtime
        .handle(request(body, stream), CancellationToken::new())
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
    let mut config = config(&[(&upstream, "gemini", true)]);
    config["models"]["public"]["quota"] = json!({"total_tokens":13,"reserve_tokens":1});
    let gateway = runtime(config, &limit, Options::default());
    let mut unauthorized = request(input(true), false);
    unauthorized.headers_mut().remove("x-goog-api-key");
    assert_eq!(
        gateway
            .handle(unauthorized, CancellationToken::new())
            .await
            .status(),
        StatusCode::UNAUTHORIZED
    );
    assert!(upstream.calls.lock().unwrap().is_empty());
    let response = invoke(&gateway, input(true), false).await;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(
        serde_json::from_str::<Value>(&consume(response).await).unwrap(),
        answer()
    );
    {
        let calls = upstream.calls.lock().unwrap();
        assert_eq!(calls.len(), 1);
        assert_eq!(calls[0]["body"], input(true));
        assert_eq!(
            calls[0]["path"],
            "/v1beta/models/private-model:generateContent"
        );
        assert_eq!(calls[0]["headers"]["x-goog-api-key"], "provider-secret");
        for header in ["x-private-caller", "authorization", "x-api-key"] {
            assert!(calls[0]["headers"][header].is_null(), "{header}");
        }
        assert!(!calls[0]["headers"].to_string().contains("client-secret"));
    }
    assert_eq!(limit.available(), 1);
    assert_eq!(
        invoke(&gateway, input(false), false).await.status(),
        StatusCode::TOO_MANY_REQUESTS
    );
}

#[tokio::test]
async fn native_sse_preserves_frames_and_settles_trailing_thought_usage_only_at_eof() {
    let upstream = upstream(200, sse(&frames()), true).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let mut config = config(&[(&upstream, "gemini", true)]);
    // Cache is a subset of 8 prompt tokens; each request costs 8 + 2 + 3 = 13.
    config["models"]["public"]["quota"] = json!({"total_tokens":26,"reserve_tokens":1});
    let gateway = runtime(config, &limit, Options::default());
    for _ in 0..2 {
        let response = invoke(&gateway, input(true), true).await;
        assert_eq!(response.status(), StatusCode::OK);
        assert_eq!(limit.available(), 0);
        let output = consume(response).await;
        let actual: Vec<Value> = output
            .lines()
            .filter_map(|line| line.strip_prefix("data: "))
            .map(|line| serde_json::from_str(line).unwrap())
            .collect();
        assert_eq!(actual, frames());
        assert!(!output.contains("[DONE]"));
        assert_eq!(limit.available(), 1);
    }
    assert_eq!(
        invoke(&gateway, input(false), false).await.status(),
        StatusCode::TOO_MANY_REQUESTS
    );
    let calls = upstream.calls.lock().unwrap();
    assert_eq!(calls.len(), 2);
    assert_eq!(
        calls[0]["path"],
        "/v1beta/models/private-model:streamGenerateContent?alt=sse"
    );
    assert_eq!(calls[0]["body"], input(true));
}

#[tokio::test]
async fn native_extensions_do_not_cross_strict_or_other_protocol_backends() {
    for (kind, native) in [("gemini", false), ("openai", true), ("anthropic", true)] {
        let upstream = upstream(200, answer().to_string(), false).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let gateway = runtime(
            config(&[(&upstream, kind, native)]),
            &limit,
            Options::default(),
        );
        assert_eq!(
            invoke(&gateway, input(true), false).await.status(),
            StatusCode::BAD_REQUEST,
            "{kind}"
        );
        assert!(upstream.calls.lock().unwrap().is_empty());
        assert_eq!(limit.available(), 1);
    }
    let first = upstream(503, "provider-secret unavailable".into(), false).await;
    let other = upstream(200, answer().to_string(), false).await;
    let backup = upstream(200, answer().to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway = runtime(
        config(&[
            (&first, "gemini", true),
            (&other, "openai", true),
            (&backup, "gemini", true),
        ]),
        &limit,
        Options::default(),
    );
    let response = invoke(&gateway, input(true), false).await;
    assert_eq!(response.status(), StatusCode::OK);
    consume(response).await;
    assert_eq!(first.calls.lock().unwrap().len(), 1);
    assert!(other.calls.lock().unwrap().is_empty());
    assert_eq!(backup.calls.lock().unwrap().len(), 1);
}

#[tokio::test]
async fn path_controls_and_minimum_contents_are_validated_before_dispatch() {
    let upstream = upstream(200, answer().to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway = runtime(
        config(&[(&upstream, "gemini", true)]),
        &limit,
        Options::default(),
    );
    for (pointer, invalid) in [
        ("/contents", json!([])),
        ("/contents", json!({})),
        ("/contents/0", json!(null)),
        ("/contents/0/role", json!(7)),
        ("/contents/0/role", json!("")),
        ("/contents/0/parts", json!([])),
        ("/contents/0/parts", json!({})),
        ("/contents/0/parts/0", json!(null)),
        ("/contents/0/parts/0", json!({})),
        ("/generationConfig", json!([])),
        ("/generationConfig/candidateCount", json!(2)),
    ] {
        let mut body = input(true);
        *body.pointer_mut(pointer).unwrap() = invalid;
        assert_eq!(
            invoke(&gateway, body, false).await.status(),
            StatusCode::BAD_REQUEST,
            "{pointer}"
        );
    }
    for key in ["model", "stream"] {
        for value in [Value::Null, json!(false), json!("private-model")] {
            let mut body = input(true);
            body[key] = value;
            assert_eq!(
                invoke(&gateway, body, false).await.status(),
                StatusCode::BAD_REQUEST,
                "{key}"
            );
        }
    }
    for path in [
        "/v1beta/models/public:generateContent?key=client-secret",
        "/v1beta/models/public:streamGenerateContent?%6bey=client-secret",
        "/v1beta/models/public:streamGenerateContent?alt=json",
        "/v1beta/models/public:streamGenerateContent?alt=sse&alt=sse",
        "/v1beta/models/public:generateContent?unknown=1",
        "/v1beta/models/public:unknownAction",
    ] {
        let mut req = request(input(true), false);
        *req.uri_mut() = path.parse().unwrap();
        assert!(
            gateway
                .handle(req, CancellationToken::new())
                .await
                .status()
                .is_client_error(),
            "{path}"
        );
    }
    assert!(upstream.calls.lock().unwrap().is_empty());
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn safety_blocks_and_missing_usage_preserve_success_and_fallback_quota() {
    let blocked = json!({"promptFeedback":{"blockReason":"SAFETY","safetyRatings":[{"category":"HARM_CATEGORY_DANGEROUS_CONTENT","probability":"HIGH","blocked":true}]},"modelVersion":"version-metadata"});
    for payload in [
        blocked,
        {
            let mut body = answer();
            body.as_object_mut().unwrap().remove("usageMetadata");
            body
        },
        json!({"candidates":[{"finishReason":"SAFETY","safetyRatings":[{"blocked":true}]}],"usageMetadata":{"promptTokenCount":0,"totalTokenCount":0}}),
    ] {
        for streaming in [false, true] {
            let upstream = upstream(
                200,
                if streaming {
                    sse(std::slice::from_ref(&payload))
                } else {
                    payload.to_string()
                },
                streaming,
            )
            .await;
            let limit = ConcurrencyLimit::new(1).unwrap();
            let mut config = config(&[(&upstream, "gemini", true)]);
            config["models"]["public"]["quota"] = json!({"total_tokens":1,"reserve_tokens":1});
            let gateway = runtime(config, &limit, Options::default());
            let response = invoke(&gateway, input(true), streaming).await;
            assert_eq!(response.status(), StatusCode::OK);
            let output = consume(response).await;
            let actual: Value = serde_json::from_str(if streaming {
                output
                    .lines()
                    .find_map(|l| l.strip_prefix("data: "))
                    .unwrap()
            } else {
                &output
            })
            .unwrap();
            assert_eq!(actual, payload);
            assert_eq!(limit.available(), 1);
            let next = invoke(&gateway, input(false), streaming).await;
            assert_eq!(
                next.status(),
                if payload.get("usageMetadata").is_some() {
                    StatusCode::OK
                } else {
                    StatusCode::TOO_MANY_REQUESTS
                }
            );
        }
    }
}

#[tokio::test]
async fn malformed_json_and_usage_keep_reservation_without_retry() {
    let mut cases = vec![
        json!({}),
        json!({"candidates":[]}),
        json!({"promptFeedback":{"blockReason":"BLOCK_REASON_UNSPECIFIED"}}),
    ];
    for (pointer, invalid) in [
        ("/candidates", json!([{}, {}])),
        ("/candidates/0/index", json!(1)),
        ("/candidates/0/finishReason", json!(null)),
        ("/candidates/0/finishReason", json!("")),
        (
            "/candidates/0/finishReason",
            json!("FINISH_REASON_UNSPECIFIED"),
        ),
        ("/usageMetadata", json!(null)),
        ("/usageMetadata", json!({})),
        ("/usageMetadata/promptTokenCount", json!(-1)),
        ("/usageMetadata/totalTokenCount", json!(14)),
        ("/usageMetadata/candidatesTokenCount", json!(1.5)),
        ("/usageMetadata/thoughtsTokenCount", json!(u64::MAX)),
        ("/usageMetadata/cachedContentTokenCount", json!(9)),
    ] {
        let mut body = answer();
        *body.pointer_mut(pointer).unwrap() = invalid;
        cases.push(body);
    }
    let mut unsupported = answer();
    unsupported["usageMetadata"]["toolUsePromptTokenCount"] = json!(1);
    cases.push(unsupported);
    for body in cases {
        let first = upstream(200, body.to_string(), false).await;
        let backup = upstream(200, answer().to_string(), false).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let mut config = config(&[(&first, "gemini", true), (&backup, "gemini", true)]);
        config["models"]["public"]["quota"] = json!({"total_tokens":1,"reserve_tokens":1});
        let gateway = runtime(config, &limit, Options::default());
        let response = invoke(&gateway, input(true), false).await;
        assert_eq!(response.status(), StatusCode::BAD_GATEWAY, "{body}");
        let output = consume(response).await;
        assert!(!output.contains("provider-secret") && !output.contains("opaque-signature"));
        assert_eq!(
            invoke(&gateway, input(false), false).await.status(),
            StatusCode::TOO_MANY_REQUESTS
        );
        assert_eq!(first.calls.lock().unwrap().len(), 1);
        assert!(backup.calls.lock().unwrap().is_empty());
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn malformed_streams_and_errors_after_finish_fail_without_retry() {
    let valid = frames();
    let mut cases = vec![
        "data: {broken-json}\n\n".into(),
        sse(&valid[..1]),
        format!("{}data: [DONE]\n\n", sse(&valid)),
        format!("{}TRANSPORT_ERROR", sse(&valid)),
        sse(&[json!({"promptFeedback":{"blockReason":"BLOCK_REASON_UNSPECIFIED"}})]),
    ];
    for replacement in [
        json!({"error":{"message":"provider-secret"}}),
        json!({"candidates":[{"index":0,"content":{"parts":[{"text":"after finish"}]}}]}),
        json!({"usageMetadata":usage(0,0)}),
        json!({"usageMetadata":{"promptTokenCount":8,"totalTokenCount":1}}),
        json!({"usageMetadata":{"promptTokenCount":8,"totalTokenCount":8,"toolUsePromptTokenCount":1}}),
    ] {
        let mut broken = valid.clone();
        broken[2] = replacement;
        cases.push(sse(&broken));
    }
    for replacement in [
        json!({"candidates":[{"index":1,"finishReason":"STOP"}]}),
        json!({"candidates":[{"finishReason":"FINISH_REASON_UNSPECIFIED"}]}),
        json!({"candidates":[{"finishReason":""}]}),
    ] {
        let mut broken = valid.clone();
        broken[1] = replacement;
        cases.push(sse(&broken));
    }
    for (index, wire) in cases.into_iter().enumerate() {
        let first = upstream(200, wire, true).await;
        let backup = upstream(200, sse(&valid), true).await;
        let limit = ConcurrencyLimit::new(1).unwrap();
        let gateway = runtime(
            config(&[(&first, "gemini", true), (&backup, "gemini", true)]),
            &limit,
            Options::default(),
        );
        let response = invoke(&gateway, input(true), true).await;
        if response.status() == StatusCode::BAD_GATEWAY {
            assert!(!consume(response).await.contains("provider-secret"));
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
            assert!(!String::from_utf8_lossy(&output).contains("provider-secret"));
            drop(stream);
        }
        assert_eq!(first.calls.lock().unwrap().len(), 1);
        assert!(
            backup.calls.lock().unwrap().is_empty(),
            "case {index} retried"
        );
        assert_eq!(limit.available(), 1);
    }
}

#[tokio::test]
async fn dropping_native_stream_releases_admission_and_keeps_reservation() {
    let upstream = upstream(200, sse(&frames()), true).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let mut config = config(&[(&upstream, "gemini", true)]);
    config["models"]["public"]["quota"] = json!({"total_tokens":1,"reserve_tokens":1});
    let gateway = runtime(config, &limit, Options::default());
    let response = invoke(&gateway, input(true), true).await;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(limit.available(), 0);
    drop(response);
    assert_eq!(limit.available(), 1);
    assert_eq!(
        invoke(&gateway, input(false), false).await.status(),
        StatusCode::TOO_MANY_REQUESTS
    );
    assert_eq!(upstream.calls.lock().unwrap().len(), 1);
}

#[tokio::test]
async fn early_usage_without_terminal_snapshot_cannot_settle_output_as_zero() {
    let frames = vec![
        json!({"usageMetadata":{"promptTokenCount":10,"totalTokenCount":10}}),
        json!({"candidates":[{"content":{"parts":[{"text":"Generated output"}]},"finishReason":"STOP"}]}),
    ];
    let upstream = upstream(200, sse(&frames), true).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let mut config = config(&[(&upstream, "gemini", true)]);
    config["models"]["public"]["quota"] = json!({"total_tokens":30,"reserve_tokens":20});
    let gateway = runtime(config, &limit, Options::default());
    let response = invoke(&gateway, input(true), true).await;
    assert_eq!(response.status(), StatusCode::OK);
    assert!(
        to_bytes(response.into_body(), 65536).await.is_err(),
        "early input-only usage cannot establish final output usage"
    );
    assert_eq!(limit.available(), 1);
    assert_eq!(
        invoke(&gateway, input(false), true).await.status(),
        StatusCode::TOO_MANY_REQUESTS
    );
    assert_eq!(upstream.calls.lock().unwrap().len(), 1);
}

#[tokio::test]
async fn cross_protocol_ingress_uses_original_gemini_codec() {
    let plain = json!({"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}});
    let upstream = upstream(200, plain.to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway = runtime(
        config(&[(&upstream, "gemini", true)]),
        &limit,
        Options::default(),
    );
    for extended in [true, false] {
        let mut body = json!({"model":"public","messages":[{"role":"user","content":"Hello"}]});
        if extended {
            body["vendor_option"] = json!(true);
        }
        let request = Request::builder()
            .method("POST")
            .uri("/v1/chat/completions")
            .header("authorization", "Bearer client-secret")
            .header("content-type", "application/json")
            .body(Body::from(body.to_string()))
            .unwrap();
        let response = gateway.handle(request, CancellationToken::new()).await;
        assert_eq!(
            response.status(),
            if extended {
                StatusCode::BAD_REQUEST
            } else {
                StatusCode::OK
            }
        );
        if extended {
            assert!(upstream.calls.lock().unwrap().is_empty());
        } else {
            let actual: Value = serde_json::from_str(&consume(response).await).unwrap();
            assert_eq!(actual["model"], "public");
            assert_eq!(actual["choices"][0]["message"]["content"], "Hello");
        }
    }
    let calls = upstream.calls.lock().unwrap();
    assert_eq!(calls.len(), 1);
    assert_eq!(calls[0]["body"]["contents"][0]["parts"][0]["text"], "Hello");
    assert!(calls[0]["body"].get("model").is_none());
    assert!(calls[0]["body"].get("stream").is_none());
    assert_eq!(limit.available(), 1);
}

#[tokio::test]
async fn optional_null_request_fields_remain_compatible_with_strict_codec() {
    let upstream = upstream(200, answer().to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway = runtime(
        config(&[(&upstream, "gemini", true)]),
        &limit,
        Options::default(),
    );
    for generation in [
        Value::Null,
        json!({"candidateCount":null,"maxOutputTokens":null}),
    ] {
        let body = json!({"contents":[{"role":null,"parts":[{"text":"Hello","functionCall":null}]}],
            "generationConfig":generation,"systemInstruction":null});
        assert!(nyro_llm::codec::gemini::decode_chat(body.clone(), "public", false).is_ok());
        let response = invoke(&gateway, body.clone(), false).await;
        assert_eq!(response.status(), StatusCode::OK);
        consume(response).await;
        assert_eq!(upstream.calls.lock().unwrap().last().unwrap()["body"], body);
    }
}

#[tokio::test]
async fn optional_null_response_fields_remain_compatible_with_strict_codec() {
    let payload = json!({"candidates":[{"index":null,"content":{"role":null,"parts":[{"text":"Hello","functionCall":null}]},"finishReason":"STOP"}],"modelVersion":null,"responseId":null});
    assert!(nyro_llm::codec::gemini::decode_chat_response(payload.clone()).is_ok());
    let upstream = upstream(200, payload.to_string(), false).await;
    let limit = ConcurrencyLimit::new(1).unwrap();
    let gateway = runtime(
        config(&[(&upstream, "gemini", true)]),
        &limit,
        Options::default(),
    );
    let response = invoke(&gateway, input(false), false).await;
    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(
        serde_json::from_str::<Value>(&consume(response).await).unwrap(),
        payload
    );
}

#[tokio::test]
async fn cached_resource_reference_survives_native_retry_without_cross_protocol_fallback() {
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
        let mut body = input(false);
        body["cachedContent"] = json!("cachedContents/existing-cache");
        let gateway = runtime(
            config(&[
                (&first, "gemini", true),
                (&incompatible, "gemini", false),
                (&incompatible, "openai", true),
                (&backup, "gemini", true),
            ]),
            &limit,
            Options::default(),
        );
        let response = invoke(&gateway, body.clone(), streaming).await;
        assert_eq!(response.status(), StatusCode::OK);
        consume(response).await;
        assert_eq!(limit.available(), 1);
        assert!(incompatible.calls.lock().unwrap().is_empty());
        for up in [&first, &backup] {
            let calls = up.calls.lock().unwrap();
            assert_eq!(calls.len(), 1);
            assert_eq!(calls[0]["body"], body);
        }
        for kind in ["gemini", "openai", "anthropic"] {
            let strict = runtime(
                config(&[(&incompatible, kind, kind != "gemini")]),
                &limit,
                Options::default(),
            );
            assert_eq!(
                invoke(&strict, body.clone(), streaming).await.status(),
                StatusCode::BAD_REQUEST
            );
            assert!(incompatible.calls.lock().unwrap().is_empty());
            assert_eq!(limit.available(), 1);
        }
    }
}
