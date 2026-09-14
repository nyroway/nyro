use nyro_llm::codec::{anthropic, gemini, openai};
use serde_json::{Value, json};
fn request() -> Value {
    json!({"contents":[{"role":"user","parts":[{"text":"Hello"}]}],"generationConfig":{"maxOutputTokens":4096}})
}
fn parts() -> Value {
    json!([{"text":"summary","thought":true},{"text":"answer"},{"text":"","thoughtSignature":"c2lnbmF0dXJl"}])
}
fn response(parts: Value) -> Value {
    json!({"responseId":"r","modelVersion":"m","candidates":[{"index":0,"content":{"role":"model","parts":parts},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"thoughtsTokenCount":4,"totalTokenCount":9}})
}
#[test]
fn config_preserves_omission_budget_and_level_and_rejects_other_protocols() {
    for config in [
        json!({}),
        json!({"includeThoughts":false}),
        json!({"thinkingBudget":-1}),
        json!({"thinkingBudget":0}),
        json!({"thinkingBudget":8192,"includeThoughts":true}),
        json!({"thinkingLevel":"HIGH"}),
        json!({"thinkingLevel":"MINIMAL"}),
    ] {
        let mut v = request();
        v["generationConfig"]["thinkingConfig"] = config.clone();
        let ir = gemini::decode_chat(v, "public", false).unwrap();
        assert_eq!(
            gemini::encode_chat(&ir).unwrap()["generationConfig"]["thinkingConfig"],
            config
        );
        assert!(openai::encode_chat(&ir).is_err());
        assert!(openai::responses::encode_chat(&ir).is_err());
        assert!(anthropic::encode_chat(&ir).is_err());
    }
}
#[test]
fn signed_text_parts_and_function_calls_round_trip_without_merging() {
    for p in [
        parts(),
        json!([{"functionCall":{"name":"f","args":{}},"thoughtSignature":"c2ln"}]),
        json!([{"thoughtSignature":"c2ln"}]),
    ] {
        let ir = gemini::decode_chat_response(response(p.clone())).unwrap();
        assert_eq!(
            gemini::encode_chat_response(&ir).unwrap()["candidates"][0]["content"]["parts"],
            p
        );
        assert!(openai::encode_chat_response(&ir).is_err());
        assert!(openai::responses::encode_chat_response(&ir).is_err());
        assert!(anthropic::encode_chat_response(&ir).is_err());
    }
    let mut v = request();
    v["contents"] =
        json!([{"role":"model","parts":parts()},{"role":"user","parts":[{"text":"continue"}]}]);
    let ir = gemini::decode_chat(v.clone(), "public", false).unwrap();
    assert_eq!(gemini::encode_chat(&ir).unwrap()["contents"], v["contents"]);
    assert!(openai::encode_chat(&ir).is_err());
    assert!(anthropic::encode_chat(&ir).is_err());
}

#[test]
fn interleaved_history_and_json_preserve_text_calls_and_signed_parts() {
    for call in [
        json!({"functionCall":{"id":"call-1","name":"f","args":{}}}),
        json!({"functionCall":{"name":"f","args":{}},"thoughtSignature":"c2ln"}),
    ] {
        let parts = json!([
            {"text":"before"}, call,
            {"text":"after","thoughtSignature":"c2lnMg=="},
            {"text":"final"}
        ]);
        let ir = gemini::decode_chat_response(response(parts.clone())).unwrap();
        assert_eq!(
            gemini::encode_chat_response(&ir).unwrap()["candidates"][0]["content"]["parts"],
            parts
        );
        let mut history = request();
        history["contents"] = json!([
            {"role":"model","parts":parts},
            {"role":"user","parts":[{"functionResponse":{"name":"f","response":{"ok":true}}}]}
        ]);
        let ir = gemini::decode_chat(history.clone(), "m", false).unwrap();
        assert_eq!(
            gemini::encode_chat(&ir).unwrap()["contents"][0]["parts"],
            parts
        );
        assert!(openai::encode_chat(&ir).is_err());
    }
}

#[test]
fn interleaved_stream_emits_signed_calls_before_following_text() {
    let parts = json!([
        {"text":"before"},
        {"functionCall":{"name":"f","args":{"x":1}},"thoughtSignature":"c2ln"},
        {"text":"after","thoughtSignature":"c2lnMg=="}
    ]);
    let mut decoder = gemini::StreamDecoder::new();
    let mut encoder = gemini::StreamEncoder::new("m".into());
    let events = decoder.push(&event(response(parts.clone()))).unwrap();
    let mut emitted = vec![];
    for event in &events {
        let output = encoder.push(event).unwrap();
        for data in output
            .lines()
            .filter_map(|line| line.strip_prefix("data: "))
        {
            let value: Value = serde_json::from_str(data).unwrap();
            emitted.extend(
                value["candidates"][0]["content"]["parts"]
                    .as_array()
                    .cloned()
                    .unwrap_or_default(),
            );
        }
    }
    assert_eq!(Value::Array(emitted), parts);
    for event in decoder.finish().unwrap() {
        assert!(encoder.push(&event).unwrap().contains("finishReason"));
    }
}

fn event(v: Value) -> nyro_protocol::framing::Event {
    nyro_protocol::framing::Event {
        data: v.to_string(),
        event: None,
    }
}
fn stream_frames(with_call: bool) -> Vec<Value> {
    vec![
        json!({"responseId":"r","modelVersion":"m","candidates":[{"content":{"role":"model","parts":[{"text":"summary","thought":true},{"text":"answer"}]}}]}),
        json!({"responseId":"r","modelVersion":"m","candidates":[{"content":{"parts":if with_call {json!([{"functionCall":{"name":"f","args":{"x":1}},"thoughtSignature":"c2ln"}])} else {json!([{"text":"","thoughtSignature":"c2ln"}])}},"finishReason":"STOP"}]}),
        json!({"responseId":"r","modelVersion":"m","usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"thoughtsTokenCount":4,"totalTokenCount":9}}),
    ]
}
#[test]
fn sse_preserves_signed_parts_and_trailing_usage_until_eof() {
    for with_call in [false, true] {
        let mut decoder = gemini::StreamDecoder::new();
        let mut encoder = gemini::StreamEncoder::new("m".into());
        let mut output = String::new();
        let frames = stream_frames(with_call);
        for frame in &frames {
            for e in decoder.push(&event(frame.clone())).unwrap() {
                if let nyro_llm::ir::ChatEvent::Chunk(c) = &e
                    && c.choices.iter().any(|c| {
                        c.delta.events.iter().any(|e| {
                            matches!(&e.delta, nyro_llm::ir::PartDelta::GeminiText(_))
                                || matches!(&e.delta,
                            nyro_llm::ir::PartDelta::ToolCall(c) if c.gemini.is_some())
                        })
                    })
                {
                    assert!(openai::encode_chat_event(&e, "m").is_err());
                    assert!(
                        openai::responses::StreamEncoder::new("m".into())
                            .push(&e)
                            .is_err()
                    );
                    assert!(anthropic::StreamEncoder::new("m".into()).push(&e).is_err());
                }
                output.push_str(&encoder.push(&e).unwrap());
            }
        }
        assert!(!output.contains("finishReason"));
        for e in decoder.finish().unwrap() {
            output.push_str(&encoder.push(&e).unwrap());
        }
        let values: Vec<Value> = output
            .lines()
            .filter_map(|l| l.strip_prefix("data: "))
            .map(|s| serde_json::from_str(s).unwrap())
            .collect();
        let emitted: Vec<Value> = values
            .iter()
            .flat_map(|v| v["candidates"].as_array().into_iter().flatten())
            .flat_map(|c| {
                c["content"]["parts"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .cloned()
            })
            .collect();
        let expected: Vec<Value> = frames
            .iter()
            .flat_map(|v| v["candidates"].as_array().into_iter().flatten())
            .flat_map(|c| {
                c["content"]["parts"]
                    .as_array()
                    .into_iter()
                    .flatten()
                    .cloned()
            })
            .collect();
        assert_eq!(emitted, expected);
        let final_usage = values
            .iter()
            .filter_map(|v| v.get("usageMetadata"))
            .next_back()
            .unwrap();
        assert_eq!(final_usage, &frames[2]["usageMetadata"]);
        let mut replay = gemini::StreamDecoder::new();
        for v in values {
            replay.push(&event(v)).unwrap();
        }
        replay.finish().unwrap();
    }
}
#[test]
fn signed_parallel_call_history_keeps_signature_and_optional_id() {
    for id in [None, Some("explicit")] {
        let mut call = json!({"functionCall":{"name":"f","args":{}},"thoughtSignature":"c2ln"});
        let mut result = json!({"functionResponse":{"name":"f","response":{"ok":true}}});
        if let Some(id) = id {
            call["functionCall"]["id"] = json!(id);
            result["functionResponse"]["id"] = json!(id);
        }
        let mut input = request();
        input["contents"] = json!([
            {"role":"model","parts":[call,{"functionCall":{"id":"second","name":"g","args":{}}}]},
            {"role":"user","parts":[result,{"functionResponse":{"id":"second","name":"g","response":{"ok":true}}}]}
        ]);
        let ir = gemini::decode_chat(input.clone(), "m", false).unwrap();
        assert_eq!(
            gemini::encode_chat(&ir).unwrap()["contents"],
            input["contents"]
        );
        assert!(anthropic::encode_chat(&ir).is_err());
        assert!(openai::encode_chat(&ir).is_err());
        assert!(openai::responses::encode_chat(&ir).is_err());
    }
}
#[test]
fn thinking_configuration_rejects_conflicts_and_preserves_proto_null_defaults() {
    for config in [
        json!({"thinkingBudget":-2}),
        json!({"thinkingBudget":1.5}),
        json!({"thinkingBudget":2147483648_u64}),
        json!({"thinkingLevel":"DEEP"}),
        json!({"thinkingLevel":"LOW","thinkingBudget":0}),
        json!({"includeThoughts":"true"}),
    ] {
        let mut input = request();
        input["generationConfig"]["thinkingConfig"] = config.clone();
        assert!(gemini::decode_chat(input, "m", false).is_err(), "{config}");
    }
    for level in [
        "minimal",
        "low",
        "medium",
        "high",
        "THINKING_LEVEL_UNSPECIFIED",
    ] {
        let mut input = request();
        input["generationConfig"]["thinkingConfig"] =
            json!({"thinkingLevel":level,"thinkingBudget":null,"includeThoughts":null});
        let ir = gemini::decode_chat(input, "m", false).unwrap();
        assert_eq!(
            gemini::encode_chat(&ir).unwrap()["generationConfig"]["thinkingConfig"],
            json!({"thinkingLevel":level.to_uppercase()})
        );
    }
    let plain = gemini::decode_chat(request(), "m", false).unwrap();
    assert!(
        gemini::encode_chat(&plain).unwrap()["generationConfig"]
            .get("thinkingConfig")
            .is_none()
    );
}
#[test]
fn invalid_roles_order_payloads_and_direct_ir_are_rejected() {
    for part in [
        json!({"text":"x","thought":"true"}),
        json!({"text":"x","thoughtSignature":7}),
        json!({"thought":true}),
        json!({"text":"x","functionCall":{"name":"f","args":{}},"thoughtSignature":"sig"}),
        json!({"inlineData":{"mimeType":"image/png","data":"aA=="},"thoughtSignature":"sig"}),
    ] {
        assert!(gemini::decode_chat_response(response(json!([part]))).is_err());
    }
    let mut input = request();
    input["contents"][0]["parts"] = parts();
    assert!(gemini::decode_chat(input, "m", false).is_err());
    let mut input = request();
    input["systemInstruction"] = json!({"parts":[{"text":"x","thought":false}]});
    assert!(gemini::decode_chat(input, "m", false).is_err());
    let invalid = json!([{"functionCall":{"name":"f","args":{}},"thoughtSignature":"sig"},{"text":"later","thought":true}]);
    assert!(gemini::decode_chat_response(response(invalid.clone())).is_ok());
    let mut decoder = gemini::StreamDecoder::new();
    assert!(decoder.push(&event(response(invalid))).is_ok());
    assert!(decoder.finish().is_ok());
    let mut input = request();
    input["contents"] = json!([{"role":"model","parts":parts()}]);
    let mut ir = gemini::decode_chat(input, "m", false).unwrap();
    ir.messages[0].role = nyro_llm::ir::Role::User;
    assert!(gemini::encode_chat(&ir).is_err());
}
#[test]
fn stream_limits_and_truncation_apply_to_thoughts_and_signed_calls() {
    for part in [
        json!({"text":"x","thoughtSignature":"s".repeat(1024)}),
        json!({"functionCall":{"name":"f","args":{}},"thoughtSignature":"s".repeat(1024)}),
    ] {
        let input = response(json!([part]));
        let mut small = gemini::StreamDecoder::with_limit(512);
        assert!(small.push(&event(input.clone())).is_err());
        assert!(small.finish().is_err());
        let mut decoder = gemini::StreamDecoder::new();
        let mut encoder = gemini::StreamEncoder::with_limit("m".into(), 512);
        let events = decoder.push(&event(input)).unwrap();
        assert!(events.iter().any(|e| encoder.push(e).is_err()));
    }
    let mut d = gemini::StreamDecoder::new();
    d.push(&event(stream_frames(false)[0].clone())).unwrap();
    assert!(d.finish().is_err());
    let mut d = gemini::StreamDecoder::new();
    d.push(&event(stream_frames(false)[1].clone())).unwrap();
    assert!(d.push(&event(stream_frames(false)[0].clone())).is_err());
}

#[test]
fn stream_part_expansion_is_bounded_before_repeating_large_identity() {
    let input = json!({"responseId":"r".repeat(2048),"modelVersion":"m",
        "candidates":[{"content":{"parts":vec![json!({"text":"","thought":true});256]},"finishReason":"STOP"}]});
    assert!(input.to_string().len() < 65536);
    let mut decoder = gemini::StreamDecoder::with_limit(65536);
    assert!(decoder.push(&event(input)).is_err());
    assert!(decoder.finish().is_err());
}
