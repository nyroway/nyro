use nyro_llm::{codec::anthropic::*, ir::ChatEvent};
use nyro_protocol::framing::Event;
use serde_json::{Value, json};
fn event(value: Value) -> Event {
    Event {
        event: value["type"].as_str().map(str::to_owned),
        data: value.to_string(),
    }
}
#[test]
fn tools_round_trip_and_strict_options() {
    let value = json!({"model":"claude","max_tokens":64,"system":"help","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"weather","input":{"city":"Paris"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"sun"}]}],"tools":[{"name":"weather","input_schema":{"type":"object"}}]});
    let request = decode_chat(value).unwrap();
    let wire = encode_chat(&request).unwrap();
    assert_eq!(wire["max_tokens"], 64);
    assert_eq!(wire["messages"][1]["content"][0]["input"]["city"], "Paris");
    let mut bad = wire;
    bad["thinking"] = json!({"type":"enabled"});
    assert!(decode_chat(bad).is_err());
}
#[test]
fn native_stream_order_and_tool_fragments() {
    let mut decoder = StreamDecoder::new();
    let events = [
        json!({"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"c","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":0}}}),
        json!({"type":"ping"}),
        json!({"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"f","input":{}}}),
        json!({"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"x\":"}}),
        json!({"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"1}"}}),
        json!({"type":"content_block_stop","index":0}),
        json!({"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":4}}),
        json!({"type":"message_stop"}),
    ];
    let mut out = vec![];
    for e in events {
        out.extend(decoder.push(&event(e)).unwrap());
    }
    assert!(matches!(out.last(), Some(ChatEvent::Done)));
    assert!(decoder.finish().unwrap().is_empty());
    let mut truncated = StreamDecoder::new();
    assert!(truncated.finish().is_err());
    assert!(
        truncated
            .push(&event(json!({"type":"content_block_stop","index":0})))
            .is_err()
    );
    let mut errors = StreamDecoder::new();
    assert!(
        errors
            .push(&event(
                json!({"type":"error","error":{"type":"overloaded_error","message":"busy"}})
            ))
            .is_err()
    );
}
#[test]
fn response_and_stream_encoding_preserve_usage() {
    let native = json!({"id":"m","type":"message","role":"assistant","model":"upstream","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":7,"output_tokens":2}});
    let response = decode_chat_response(native.clone()).unwrap();
    assert_eq!(encode_chat_response(&response).unwrap(), native);
    let mut encoder = StreamEncoder::new("public".into());
    let chunk = nyro_llm::ir::ChatChunk {
        id: "m".into(),
        object: "chat.completion.chunk".into(),
        created: 0,
        model: "upstream".into(),
        choices: vec![nyro_llm::ir::StreamChoice {
            index: 0,
            delta: nyro_llm::ir::Delta {
                content: Some("hello".into()),
                ..Default::default()
            },
            finish_reason: Some("stop".into()),
            logprobs: None,
        }],
        usage: response.usage,
        system_fingerprint: None,
        service_tier: None,
        obfuscation: None,
    };
    let mut sse = encoder
        .push(&ChatEvent::Chunk(Box::new(chunk.clone())))
        .unwrap();
    sse.push_str(&encoder.push(&ChatEvent::Done).unwrap());
    assert!(!sse.contains("upstream"));
    let mut framing = nyro_protocol::framing::Decoder::new(4096);
    let mut decoder = StreamDecoder::new();
    let mut events = vec![];
    for bytes in sse.as_bytes().chunks(3) {
        for e in framing.push(bytes).unwrap() {
            events.extend(decoder.push(&e).unwrap());
        }
    }
    assert!(events.last().unwrap().is_done());
    let usage = events
        .iter()
        .filter_map(|e| {
            if let ChatEvent::Chunk(c) = e {
                c.usage.as_ref()
            } else {
                None
            }
        })
        .next_back()
        .unwrap();
    assert_eq!(usage.total_tokens, 9);
    assert!(
        StreamEncoder::with_limit("p".into(), 2)
            .push(&ChatEvent::Chunk(Box::new(chunk)))
            .is_err()
    );
}
#[test]
fn rejects_lossy_options_and_missing_max_tokens() {
    let request = nyro_llm::codec::openai::decode_chat(
        json!({"model":"m","messages":[{"role":"user","content":"hi"}]}),
    )
    .unwrap();
    assert!(encode_chat(&request).is_err());
    let mut request = request;
    request.generation.max_tokens = Some(4);
    request.generation.seed = Some(7);
    assert!(encode_chat(&request).is_err());
    assert!(decode_chat(json!({"model":"m","max_tokens":4,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]}]})).is_err());
}
#[test]
fn malformed_and_oversized_tool_streams_fail_without_done() {
    let start = json!({"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"c","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}});
    let tool = json!({"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"f","input":{}}});
    let mut decoder = StreamDecoder::with_limit(256);
    decoder.push(&event(start.clone())).unwrap();
    decoder.push(&event(tool.clone())).unwrap();
    for _ in 0..3 {
        decoder.push(&event(json!({"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"x".repeat(70)}}))).unwrap();
    }
    assert!(decoder.push(&event(json!({"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"x".repeat(70)}}))).is_err());
    let mut decoder = StreamDecoder::new();
    decoder.push(&event(start)).unwrap();
    decoder.push(&event(tool)).unwrap();
    decoder.push(&event(json!({"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{"}}))).unwrap();
    assert!(
        decoder
            .push(&event(json!({"type":"content_block_stop","index":0})))
            .is_err()
    );
    assert!(decoder.finish().is_err());
}
#[test]
fn parallel_tool_encoder_validates_fragmented_arguments() {
    use nyro_llm::{codec::openai, ir::*};
    let response=decode_chat_response(json!({"id":"m","type":"message","role":"assistant","model":"c","content":[],"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":2,"output_tokens":3}})).unwrap();
    let mut encoder = StreamEncoder::new("public".into());
    let mut sse = String::new();
    for (tools, finish) in [
        (
            json!([{"index":0,"id":"a","type":"function","function":{"name":"f","arguments":"{"}},{"index":1,"id":"b","type":"function","function":{"name":"g","arguments":"{"}}]),
            None,
        ),
        (
            json!([{"index":1,"function":{"arguments":"\"y\":2}"}},{"index":0,"function":{"arguments":"\"x\":1}"}}]),
            Some("tool_calls"),
        ),
    ] {
        let chunk:ChatChunk=serde_json::from_value(json!({"id":"m","object":"chat.completion.chunk","created":0,"model":"c","choices":[{"index":0,"delta":{"tool_calls":tools},"finish_reason":finish}],"usage":if finish.is_some(){serde_json::to_value(&response.usage).unwrap()}else{Value::Null}})).unwrap();
        sse.push_str(&encoder.push(&ChatEvent::Chunk(Box::new(chunk))).unwrap());
    }
    sse.push_str(&encoder.push(&ChatEvent::Done).unwrap());
    let mut framing = nyro_protocol::framing::Decoder::new(4096);
    let mut decoder = StreamDecoder::new();
    let mut out = String::new();
    for e in framing.push(sse.as_bytes()).unwrap() {
        for chunk in decoder.push(&e).unwrap() {
            out.push_str(&openai::encode_chat_event(&chunk, "public").unwrap());
        }
    }
    assert!(out.contains("\\\"x\\\":1"));
    assert!(out.contains("\\\"y\\\":2"));
    assert!(out.contains("\"prompt_tokens\":2"));
    assert!(out.contains("[DONE]"));
}
#[test]
fn rejects_text_after_tool_use_instead_of_reordering() {
    let content = json!([{"type":"tool_use","id":"t","name":"f","input":{}},{"type":"text","text":"after tool"}]);
    assert!(decode_chat_response(json!({"id":"m","type":"message","role":"assistant","model":"c","content":content,"stop_reason":"tool_use","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}})).is_err());
    assert!(
        decode_chat(
            json!({"model":"c","max_tokens":4,"messages":[{"role":"assistant","content":content}]})
        )
        .is_err()
    );
    let mut decoder = StreamDecoder::new();
    for value in [
        json!({"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"c","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}),
        json!({"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"f","input":{}}}),
        json!({"type":"content_block_stop","index":0}),
    ] {
        decoder.push(&event(value)).unwrap();
    }
    assert!(decoder.push(&event(json!({"type":"content_block_start","index":1,"content_block":{"type":"text","text":"after tool"}}))).is_err());
}
#[test]
fn rejects_unrepresentable_matched_stop_sequence() {
    for (reason, sequence) in [
        ("stop_sequence", Value::Null),
        ("stop_sequence", json!("END")),
        ("end_turn", json!("END")),
    ] {
        assert!(decode_chat_response(json!({"id":"m","type":"message","role":"assistant","model":"c","content":[],"stop_reason":reason,"stop_sequence":sequence,"usage":{"input_tokens":1,"output_tokens":1}})).is_err());
        let mut decoder = StreamDecoder::new();
        decoder.push(&event(json!({"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"c","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}))).unwrap();
        assert!(decoder.push(&event(json!({"type":"message_delta","delta":{"stop_reason":reason,"stop_sequence":sequence},"usage":{"output_tokens":1}}))).is_err());
    }
}

fn parallel_history() -> Value {
    json!({"model":"m","max_tokens":64,"messages":[
        {"role":"user","content":"Compare cities"},
        {"role":"assistant","content":"Checking both","tool_calls":[
            {"type":"function","id":"a","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}},
            {"type":"function","id":"b","function":{"name":"weather","arguments":"{\"city\":\"Tokyo\"}"}}]},
        {"role":"tool","tool_call_id":"b","content":"Tokyo: sunny"},
        {"role":"tool","tool_call_id":"a","content":"Paris: cloudy"},
        {"role":"user","content":"Compare the results"}]})
}

#[test]
fn parallel_tool_results_share_one_user_turn_without_reordering_text_or_ids() {
    let source = parallel_history();
    let request = nyro_llm::codec::openai::decode_chat(source).unwrap();
    let wire = encode_chat(&request).unwrap();
    assert_eq!(
        wire["messages"],
        json!([
        {"role":"user","content":[{"type":"text","text":"Compare cities"}]},
        {"role":"assistant","content":[{"type":"text","text":"Checking both"},
            {"type":"tool_use","id":"a","name":"weather","input":{"city":"Paris"}},
            {"type":"tool_use","id":"b","name":"weather","input":{"city":"Tokyo"}}]},
        {"role":"user","content":[
            {"type":"tool_result","tool_use_id":"b","content":[{"type":"text","text":"Tokyo: sunny"}]},
            {"type":"tool_result","tool_use_id":"a","content":[{"type":"text","text":"Paris: cloudy"}]},
            {"type":"text","text":"Compare the results"}]}])
    );
    // Decoding and encoding again must preserve the same valid Anthropic turns.
    assert_eq!(
        encode_chat(&decode_chat(wire.clone()).unwrap()).unwrap(),
        wire
    );
}

#[test]
fn anthropic_tool_batches_reject_missing_or_ambiguous_results_and_interleaved_messages() {
    let source = parallel_history();
    let mut cases = vec![];
    let mut bad = source.clone();
    bad["messages"][3]["tool_call_id"] = json!("b");
    cases.push(bad);
    let mut bad = source.clone();
    bad["messages"][3]["tool_call_id"] = json!("unknown");
    cases.push(bad);
    let mut bad = source.clone();
    bad["messages"][1]["tool_calls"][1]["id"] = json!("a");
    cases.push(bad);
    let mut bad = source.clone();
    bad["messages"].as_array_mut().unwrap().remove(3);
    cases.push(bad);
    let mut bad = source.clone();
    bad["messages"].as_array_mut().unwrap().truncate(3);
    cases.push(bad);
    let mut bad = source.clone();
    bad["messages"].as_array_mut().unwrap().remove(1);
    cases.push(bad);
    for role in ["user", "assistant"] {
        let mut bad = source.clone();
        bad["messages"]
            .as_array_mut()
            .unwrap()
            .insert(3, json!({"role":role,"content":"interleaved"}));
        cases.push(bad);
    }
    for bad in cases {
        let request = nyro_llm::codec::openai::decode_chat(bad.clone()).unwrap();
        assert!(encode_chat(&request).is_err(), "{bad}");
    }
}

#[test]
fn completed_tool_batches_keep_separate_turns_and_reset_pending_ids() {
    let mut source = parallel_history();
    source["messages"].as_array_mut().unwrap().extend([
        json!({"role":"assistant","tool_calls":[{"type":"function","id":"c","function":{"name":"lookup","arguments":"{}"}}]}),
        json!({"role":"tool","tool_call_id":"c","content":"Second batch"}),
    ]);
    let request = nyro_llm::codec::openai::decode_chat(source).unwrap();
    let wire = encode_chat(&request).unwrap();
    assert_eq!(wire["messages"].as_array().unwrap().len(), 5);
    assert_eq!(wire["messages"][2]["content"].as_array().unwrap().len(), 3);
    assert_eq!(
        wire["messages"][3]["content"],
        json!([{"type":"tool_use","id":"c","name":"lookup","input":{}}])
    );
    assert_eq!(
        wire["messages"][4],
        json!({"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":[{"type":"text","text":"Second batch"}]}]})
    );
    assert_eq!(
        encode_chat(&decode_chat(wire.clone()).unwrap()).unwrap(),
        wire
    );
}
