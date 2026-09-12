use nyro_llm::codec::{anthropic, openai};
use serde_json::{Value, json};

fn request() -> Value {
    json!({"model":"test","max_tokens":4096,"messages":[{"role":"user","content":"hello"}]})
}
fn thinking() -> Value {
    json!({"type":"thinking","thinking":"summary","signature":"opaque-signature"})
}
fn answer() -> Value {
    json!({"id":"msg_test","type":"message","role":"assistant","model":"test",
        "content":[thinking(),{"type":"redacted_thinking","data":"opaque-data"},{"type":"text","text":"answer"}],
        "stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":2,"output_tokens":8}})
}
#[test]
fn thinking_config_round_trip_and_cross_protocol_rejection() {
    for config in [
        json!({"type":"enabled","budget_tokens":1024}),
        json!({"type":"adaptive"}),
        json!({"type":"adaptive","display":"omitted"}),
        json!({"type":"enabled","budget_tokens":8192,"display":"summarized"}),
        json!({"type":"disabled"}),
    ] {
        let mut input = request();
        input["thinking"] = config.clone();
        let ir = anthropic::decode_chat(input).unwrap();
        assert_eq!(anthropic::encode_chat(&ir).unwrap()["thinking"], config);
        assert!(openai::encode_chat(&ir).is_err());
        assert!(openai::responses::encode_chat(&ir).is_err());
        assert!(nyro_llm::codec::gemini::encode_chat(&ir).is_err());
    }
    assert!(
        anthropic::encode_chat(&anthropic::decode_chat(request()).unwrap())
            .unwrap()
            .get("thinking")
            .is_none()
    );
}
#[test]
fn thinking_config_rejects_invalid_shapes() {
    for config in [
        json!({"type":"enabled"}),
        json!({"type":"enabled","budget_tokens":1023}),
        json!({"type":"adaptive","budget_tokens":1024}),
        json!({"type":"disabled","display":"omitted"}),
        json!({"type":"adaptive","display":null}),
        json!({"type":"adaptive","display":"updates"}),
    ] {
        let mut input = request();
        input["thinking"] = config.clone();
        assert!(anthropic::decode_chat(input).is_err(), "{config}");
    }
}
#[test]
fn thinking_history_and_json_preserve_opaque_blocks() {
    let response = answer();
    let ir = anthropic::decode_chat_response(response.clone()).unwrap();
    assert_eq!(anthropic::encode_chat_response(&ir).unwrap(), response);
    assert!(openai::encode_chat_response(&ir).is_err());
    assert!(openai::responses::encode_chat_response(&ir).is_err());
    assert!(nyro_llm::codec::gemini::encode_chat_response(&ir).is_err());
    let mut input = request();
    input["messages"] = json!([{"role":"assistant","content":response["content"]},{"role":"user","content":"continue"}]);
    let decoded = anthropic::decode_chat(input.clone()).unwrap();
    assert_eq!(
        anthropic::encode_chat(&decoded).unwrap()["messages"][0],
        input["messages"][0]
    );
    assert!(openai::encode_chat(&decoded).is_err());
}

fn event(value: Value) -> nyro_protocol::framing::Event {
    nyro_protocol::framing::Event {
        event: value["type"].as_str().map(str::to_owned),
        data: value.to_string(),
    }
}
fn start() -> Value {
    let mut message = answer();
    message["content"] = json!([]);
    message["stop_reason"] = Value::Null;
    message["usage"]["output_tokens"] = json!(0);
    json!({"type":"message_start","message":message})
}
fn frames(omitted: bool) -> Vec<Value> {
    let mut frames = vec![
        start(),
        json!({"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}),
    ];
    if !omitted {
        frames.push(json!({"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"思考 summary"}}));
    }
    frames.extend([
        json!({"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"opaque-signature"}}),
        json!({"type":"content_block_stop","index":0}),
        json!({"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"opaque-data"}}),
        json!({"type":"content_block_stop","index":1}),
        json!({"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}),
        json!({"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"answer"}}),
        json!({"type":"content_block_stop","index":2}),
        json!({"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":8}}),
        json!({"type":"message_stop"}),
    ]);
    frames
}
#[test]
fn streaming_preserves_boundaries_signatures_and_usage() {
    for omitted in [false, true] {
        let mut decoder = anthropic::StreamDecoder::new();
        let mut encoder = anthropic::StreamEncoder::new("test".into());
        let mut ir_events = vec![];
        let mut output = String::new();
        for frame in frames(omitted) {
            for ir in decoder.push(&event(frame)).unwrap() {
                output.push_str(&encoder.push(&ir).unwrap());
                ir_events.push(ir);
            }
        }
        decoder.finish().unwrap();
        let mut replay = anthropic::StreamDecoder::new();
        let mut replay_events = vec![];
        for frame in output
            .lines()
            .filter_map(|line| line.strip_prefix("data: "))
        {
            replay_events.extend(
                replay
                    .push(&event(serde_json::from_str(frame).unwrap()))
                    .unwrap(),
            );
        }
        replay.finish().unwrap();
        // Text starts may add an empty text delta; thinking events preserve exact boundaries.
        let thinking_events = |events: &[nyro_llm::ir::ChatEvent]| {
            events
                .iter()
                .filter_map(|e| match e {
                    nyro_llm::ir::ChatEvent::Chunk(c) => c
                        .choices
                        .first()
                        .and_then(|c| c.delta.anthropic_thinking.clone()),
                    _ => None,
                })
                .collect::<Vec<_>>()
        };
        assert_eq!(thinking_events(&ir_events), thinking_events(&replay_events));
        assert_eq!(ir_events.last(), replay_events.last());
        for ir in &ir_events {
            if matches!(ir, nyro_llm::ir::ChatEvent::Chunk(c) if c.choices.iter().any(|c| c.delta.anthropic_thinking.is_some()))
            {
                assert!(openai::encode_chat_event(ir, "test").is_err());
                assert!(
                    nyro_llm::codec::gemini::StreamEncoder::new("test".into())
                        .push(ir)
                        .is_err()
                );
                assert!(
                    openai::responses::StreamEncoder::new("test".into())
                        .push(ir)
                        .is_err()
                );
            }
        }
        assert_eq!(output.matches("event: content_block_start\n").count(), 3);
        assert!(output.contains("opaque-signature"));
        assert!(output.contains("opaque-data"));
        assert_eq!(output.contains("thinking_delta"), !omitted);
        assert!(output.contains("\"output_tokens\":8"));
    }
}
#[test]
fn malformed_thinking_streams_reject_mismatches_missing_signatures_and_overflow() {
    let inputs = frames(true);
    for invalid in [
        json!({"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"wrong"}}),
        json!({"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"wrong-index"}}),
        json!({"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":""}}),
        json!({"type":"content_block_stop","index":0}),
        json!({"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":"","signature":""}}),
    ] {
        let mut decoder = anthropic::StreamDecoder::new();
        for frame in &inputs[..2] {
            decoder.push(&event(frame.clone())).unwrap();
        }
        assert!(decoder.push(&event(invalid)).is_err());
        assert!(decoder.finish().is_err());
    }
    let mut decoder = anthropic::StreamDecoder::with_limit(512);
    for frame in &inputs[..2] {
        decoder.push(&event(frame.clone())).unwrap();
    }
    for _ in 0..2 {
        decoder.push(&event(json!({"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"x".repeat(200)}}))).unwrap();
    }
    assert!(decoder.push(&event(json!({"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"s".repeat(200)}}))).is_err());
    let mut decoder = anthropic::StreamDecoder::new();
    for frame in &inputs[..3] {
        decoder.push(&event(frame.clone())).unwrap();
    }
    assert!(decoder.push(&event(json!({"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"after signature"}}))).is_err());
}
#[test]
fn invalid_history_roles_order_and_output_are_rejected() {
    for content in [
        json!([{"type":"thinking","thinking":"summary"}]),
        json!([{"type":"thinking","thinking":"summary","signature":""}]),
        json!([{"type":"redacted_thinking","data":""}]),
        json!([{"type":"tool_use","id":"t","name":"f","input":{}},thinking()]),
    ] {
        let mut input = request();
        input["messages"] = json!([{"role":"assistant","content":content}]);
        assert!(anthropic::decode_chat(input).is_err());
        let mut response = answer();
        response["content"] = content;
        assert!(anthropic::decode_chat_response(response).is_err());
    }
    for role in ["user", "system", "tool"] {
        let mut input = request();
        input["messages"] = json!([{"role":role,"content":[thinking()]}]);
        assert!(anthropic::decode_chat(input).is_err());
    }
    let mut input = request();
    input["messages"] = json!([{"role":"assistant","content":[thinking(),{"type":"tool_use","id":"t","name":"f","input":{}}]},
        {"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"done"}]}]}]);
    let mut ir = anthropic::decode_chat(input.clone()).unwrap();
    assert_eq!(
        anthropic::encode_chat(&ir).unwrap()["messages"],
        input["messages"]
    );
    ir.messages[0].role = nyro_llm::ir::Role::User;
    assert!(anthropic::encode_chat(&ir).is_err());
}

#[test]
fn encoder_rejects_incomplete_mixed_or_unbounded_thinking_events() {
    use nyro_llm::ir::{AnthropicThinkingDelta as T, ChatChunk, ChatEvent, Delta, StreamChoice};
    let chunk = |delta| {
        ChatEvent::Chunk(Box::new(ChatChunk {
            id: "msg_test".into(),
            object: "chat.completion.chunk".into(),
            created: 0,
            model: "test".into(),
            choices: vec![StreamChoice {
                index: 0,
                delta,
                finish_reason: None,
                logprobs: None,
            }],
            usage: None,
            system_fingerprint: None,
            service_tier: None,
            obfuscation: None,
        }))
    };
    let thinking = |event| {
        chunk(Delta {
            anthropic_thinking: Some(event),
            ..Default::default()
        })
    };
    for invalid in [
        thinking(T::Stop),
        thinking(T::Start),
        chunk(Delta {
            content: Some("text".into()),
            ..Default::default()
        }),
        thinking(T::Signature {
            signature: String::new(),
        }),
        thinking(T::Thinking {
            thinking: "x".repeat(513),
        }),
    ] {
        let mut encoder = anthropic::StreamEncoder::with_limit("test".into(), 512);
        encoder.push(&thinking(T::Start)).unwrap();
        assert!(encoder.push(&invalid).is_err());
    }
    let mut encoder = anthropic::StreamEncoder::new("test".into());
    encoder.push(&thinking(T::Start)).unwrap();
    assert!(encoder.push(&ChatEvent::Done).is_err());
    let mixed = chunk(Delta {
        anthropic_thinking: Some(T::Start),
        content: Some("text".into()),
        ..Default::default()
    });
    assert!(
        anthropic::StreamEncoder::new("test".into())
            .push(&mixed)
            .is_err()
    );
    let mut encoder = anthropic::StreamEncoder::new("test".into());
    encoder.push(&thinking(T::Start)).unwrap();
    encoder
        .push(&thinking(T::Signature {
            signature: "opaque".into(),
        }))
        .unwrap();
    assert!(
        encoder
            .push(&thinking(T::Thinking {
                thinking: "late".into()
            }))
            .is_err()
    );
}

#[test]
fn repeated_thinking_blocks_between_text_keep_order() {
    let mut frames = frames(true);
    let terminal = frames.split_off(frames.len() - 2);
    frames.extend([
        json!({"type":"content_block_start","index":3,"content_block":{"type":"thinking","thinking":""}}),
        json!({"type":"content_block_delta","index":3,"delta":{"type":"signature_delta","signature":"second-signature"}}),
        json!({"type":"content_block_stop","index":3}),
        json!({"type":"content_block_start","index":4,"content_block":{"type":"text","text":"tail"}}),
        json!({"type":"content_block_stop","index":4}),
    ]);
    frames.extend(terminal);
    let mut decoder = anthropic::StreamDecoder::new();
    let mut encoder = anthropic::StreamEncoder::new("test".into());
    let mut output = String::new();
    for frame in frames {
        for e in decoder.push(&event(frame)).unwrap() {
            output.push_str(&encoder.push(&e).unwrap());
        }
    }
    let starts: Vec<_> = output
        .lines()
        .filter_map(|l| l.strip_prefix("data: "))
        .map(|s| serde_json::from_str::<Value>(s).unwrap())
        .filter(|v| v["type"] == "content_block_start")
        .collect();
    assert_eq!(
        starts
            .iter()
            .map(|v| v["index"].as_u64().unwrap())
            .collect::<Vec<_>>(),
        vec![0, 1, 2, 3, 4]
    );
    assert_eq!(
        starts
            .iter()
            .map(|v| v["content_block"]["type"].as_str().unwrap())
            .collect::<Vec<_>>(),
        vec!["thinking", "redacted_thinking", "text", "thinking", "text"]
    );
    let mut replay = anthropic::StreamDecoder::new();
    for line in output.lines().filter_map(|l| l.strip_prefix("data: ")) {
        replay
            .push(&event(serde_json::from_str(line).unwrap()))
            .unwrap();
    }
    replay.finish().unwrap();
}
