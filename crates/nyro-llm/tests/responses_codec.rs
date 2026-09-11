use nyro_llm::{
    codec::openai::{self, responses::*},
    ir::*,
};
use nyro_protocol::framing::{Decoder, Event};
use serde_json::{Value, json};

fn response(output: Value, status: &str) -> Value {
    json!({"id":"resp_test","object":"response","created_at":7,"model":"upstream","status":status,"output":output,"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5,"input_tokens_details":{"cached_tokens":1},"output_tokens_details":{"reasoning_tokens":0}}})
}
fn message(text: &str) -> Value {
    json!({"id":"msg_test","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":text,"annotations":[]}]})
}
fn event(n: u64, kind: &str, fields: Value) -> Event {
    let mut v = fields;
    v["type"] = json!(kind);
    v["sequence_number"] = json!(n);
    Event {
        event: Some(kind.into()),
        data: v.to_string(),
    }
}
fn created() -> Event {
    event(
        0,
        "response.created",
        json!({"response":response(json!([]), "in_progress")}),
    )
}
#[test]
fn request_text_history_functions_and_options() {
    let simple = decode_chat(
        json!({"model":"m","instructions":"Be precise","input":"hello","max_output_tokens":21}),
    )
    .unwrap();
    assert_eq!(simple.messages[0].role, Role::System);
    assert_eq!(simple.generation.max_tokens, Some(21));
    assert_eq!(encode_chat(&simple).unwrap()["store"], false);
    let request = decode_chat(json!({"model":"m","stream":true,"stream_options":{"include_obfuscation":false},"input":[{"role":"user","content":[{"type":"input_text","text":"weather?"}]},{"type":"function_call","id":"fc1","call_id":"call1","name":"weather","arguments":"{}"},{"type":"function_call_output","call_id":"call1","output":"sunny"}],"tools":[{"type":"function","name":"weather","parameters":{"type":"object"},"strict":true}],"tool_choice":{"type":"function","name":"weather"},"temperature":0.4,"top_p":0.9,"parallel_tool_calls":false})).unwrap();
    assert_eq!(request.messages[2].tool_call_id.as_deref(), Some("call1"));
    let wire = encode_chat(&request).unwrap();
    assert_eq!(wire["input"][1]["call_id"], "call1");
    assert_eq!(wire["tools"][0]["name"], "weather");
    assert_eq!(decode_chat(wire).unwrap(), request);
}
#[test]
fn function_result_preserves_strings_and_text_part_boundaries() {
    for (output, expected) in [
        (json!(""), Content::Text(String::new())),
        (json!("sunny"), Content::Text("sunny".into())),
        (json!([]), Content::Parts(vec![])),
        (
            json!([{"type":"input_text","text":"first"},{"type":"input_text","text":""},{"type":"input_text","text":"second"}]),
            Content::Parts(vec![
                ContentPart::Text {
                    prompt_cache_breakpoint: None,
                    text: "first".into(),
                },
                ContentPart::Text {
                    prompt_cache_breakpoint: None,
                    text: String::new(),
                },
                ContentPart::Text {
                    prompt_cache_breakpoint: None,
                    text: "second".into(),
                },
            ]),
        ),
    ] {
        let request = decode_chat(json!({"model":"m","input":[
            {"type":"function_call","call_id":"call1","name":"weather","arguments":"{}"},
            {"type":"function_call_output","call_id":"call1","output":output}
        ]}))
        .unwrap();
        assert_eq!(request.messages[1].content.as_ref(), Some(&expected));
        let encoded = encode_chat(&request).unwrap();
        assert_eq!(encoded["input"][1]["output"], output);
        assert_eq!(decode_chat(encoded).unwrap(), request);
    }
}

#[test]
fn function_result_error_status_cannot_be_dropped() {
    let mut request = decode_chat(json!({"model":"m","input":[
        {"type":"function_call_output","call_id":"call1","output":"failed"}
    ]}))
    .unwrap();
    assert!(!request.messages[0].tool_error);
    request.messages[0].tool_error = true;
    assert!(encode_chat(&request).is_err());
}

#[test]
fn function_result_encode_preserves_ir_text_parts() {
    let request = openai::decode_chat(json!({"model":"m","messages":[
        {"role":"tool","tool_call_id":"call1","content":[
            {"type":"text","text":"first"},{"type":"text","text":"second"}
        ]}
    ]}))
    .unwrap();
    assert_eq!(
        encode_chat(&request).unwrap()["input"][0]["output"],
        json!([{"type":"input_text","text":"first"},{"type":"input_text","text":"second"}])
    );
}

#[test]
fn function_result_rejects_non_input_text_parts_and_invalid_shapes() {
    for output in [
        json!([{"type":"input_image","image_url":"https://example.com/image.png"}]),
        json!([{"type":"input_audio","input_audio":{"data":"YQ==","format":"wav"}}]),
        json!([{"type":"input_file","file_id":"file1"}]),
        json!([{"type":"output_text","text":"lost"}]),
        json!([{"type":"refusal","refusal":"lost"}]),
        json!([{"type":"input_text","text":"ok","extra":"lost"}]),
        json!([{"type":"input_text"}]),
        json!(["text"]),
        json!({"type":"input_text","text":"not an array"}),
        Value::Null,
    ] {
        assert!(
            decode_chat(json!({"model":"m","input":[
                {"type":"function_call_output","call_id":"call1","output":output}
            ]}))
            .is_err(),
            "{output}"
        );
    }
    for part in [
        json!({"type":"image_url","image_url":{"url":"https://example.com/image.png"}}),
        json!({"type":"input_audio","input_audio":{"data":"YQ==","format":"wav"}}),
        json!({"type":"refusal","refusal":"lost"}),
    ] {
        let mut request = openai::decode_chat(json!({"model":"m","messages":[
            {"role":"tool","tool_call_id":"call1","content":"result"}
        ]}))
        .unwrap();
        request.messages[0].content = Some(serde_json::from_value(json!([part])).unwrap());
        assert!(encode_chat(&request).is_err());
    }
}

#[test]
fn request_rejects_unrepresentable_semantics() {
    for (key, value) in [
        ("previous_response_id", json!("r")),
        ("conversation", json!("c")),
        ("store", json!(true)),
        ("background", json!(true)),
        ("reasoning", json!({"effort":"low"})),
        ("include", json!(["reasoning.encrypted_content"])),
        ("truncation", json!("auto")),
        ("unknown", json!(true)),
    ] {
        let mut v = json!({"model":"m","input":"hi"});
        v[key] = value;
        assert!(decode_chat(v).is_err(), "{key}");
    }
    for item in [
        json!({"type":"item_reference","id":"m"}),
        json!({"role":"user","content":[{"type":"input_image","file_id":"file-1"}]}),
        json!({"type":"reasoning","summary":[]}),
    ] {
        assert!(decode_chat(json!({"model":"m","input":[item]})).is_err());
    }
    let mut r = openai::decode_chat(
        json!({"model":"m","messages":[{"role":"user","content":"x"}],"seed":1}),
    )
    .unwrap();
    assert!(encode_chat(&r).is_err());
    r.generation.seed = None;
    r.generation.n = Some(2);
    assert!(encode_chat(&r).is_err());
}
#[test]
fn response_text_refusal_tools_usage_and_incomplete() {
    let mut v = response(
        json!([message("hello"),{"type":"function_call","id":"fc1","call_id":"call1","name":"weather","arguments":"{}","status":"completed"}]),
        "completed",
    );
    let ir = decode_chat_response(v.clone()).unwrap();
    assert_eq!(ir.choices[0].finish_reason.as_deref(), Some("tool_calls"));
    assert_eq!(
        ir.usage
            .as_ref()
            .unwrap()
            .prompt_tokens_details
            .as_ref()
            .unwrap()
            .cached_tokens,
        Some(1)
    );
    let encoded = encode_chat_response(&ir).unwrap();
    assert_eq!(encoded["output"][1]["call_id"], "call1");
    for (native, canonical) in [
        ("max_output_tokens", "length"),
        ("content_filter", "content_filter"),
    ] {
        v["status"] = json!("incomplete");
        v["incomplete_details"] = json!({"reason":native});
        let r = decode_chat_response(v.clone()).unwrap();
        assert_eq!(r.choices[0].finish_reason.as_deref(), Some(canonical));
        assert_eq!(
            encode_chat_response(&r).unwrap()["incomplete_details"]["reason"],
            native
        );
    }
    let refusal = response(
        json!([{"type":"message","id":"m","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"No"}]}]),
        "completed",
    );
    assert_eq!(
        encode_chat_response(&decode_chat_response(refusal).unwrap()).unwrap()["output"][0]["content"]
            [0]["refusal"],
        "No"
    );
    for bad in [
        json!({"type":"reasoning","id":"r","summary":[]}),
        json!({"type":"message","id":"m","role":"assistant","status":"completed","content":[{"type":"output_text","text":"x","annotations":[{"type":"url_citation","url":"https://x"}]}]}),
    ] {
        assert!(decode_chat_response(response(json!([bad]), "completed")).is_err());
    }
}
#[test]
fn snapshot_only_stream_is_explicitly_decoded() {
    let mut d = StreamDecoder::new();
    d.push(&created()).unwrap();
    let events = d
        .push(&event(
            1,
            "response.completed",
            json!({"response":response(json!([message("hello")]),"completed")}),
        ))
        .unwrap();
    assert!(events.iter().any(|e| matches!(e, ChatEvent::Chunk(c) if c.choices.iter().any(|x| x.delta.content.as_deref()==Some("hello")))));
    assert!(events.last().unwrap().is_done());
    d.finish().unwrap();
}
#[test]
fn native_stream_rejects_missing_terminal_and_bad_identity() {
    let mut d = StreamDecoder::new();
    d.push(&created()).unwrap();
    assert!(d.finish().is_err());
    let mut d = StreamDecoder::new();
    d.push(&created()).unwrap();
    assert!(
        d.push(&event(
            0,
            "response.in_progress",
            json!({"response":response(json!([]),"in_progress")})
        ))
        .is_err()
    );
    let mut d = StreamDecoder::new();
    let mut e = created();
    e.event = Some("response.completed".into());
    assert!(d.push(&e).is_err());
    for kind in [
        "response.failed",
        "response.cancelled",
        "error",
        "response.mystery",
    ] {
        let mut d = StreamDecoder::new();
        d.push(&created()).unwrap();
        assert!(
            d.push(&event(
                1,
                kind,
                json!({"response":response(json!([]),"failed")})
            ))
            .is_err()
        );
    }
    let mut d = StreamDecoder::with_limit(100);
    assert!(d.push(&created()).is_err());
}
#[test]
fn encoder_emits_native_lifecycle_full_snapshot_and_no_done_marker() {
    let mut e = StreamEncoder::new("alias".into());
    let mut s = String::new();
    for payload in [
        json!({"content":"hello"}),
        json!({"refusal":"No"}),
        json!({"tool_calls":[{"index":0,"id":"call1","type":"function","function":{"name":"weather","arguments":"{"}},{"index":1,"id":"call2","type":"function","function":{"name":"time","arguments":"{"}}]}),
        json!({"tool_calls":[{"index":1,"function":{"arguments":"}"}},{"index":0,"function":{"arguments":"}"}}]}),
    ] {
        let v = json!({"id":"chat1","object":"chat.completion.chunk","created":7,"model":"upstream","choices":[{"index":0,"delta":payload,"finish_reason":null}]});
        s.push_str(
            &e.push(&openai::decode_chat_event(&v.to_string()).unwrap())
                .unwrap(),
        );
    }
    let v = json!({"id":"chat1","object":"chat.completion.chunk","created":7,"model":"upstream","choices":[{"index":0,"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}});
    s.push_str(
        &e.push(&openai::decode_chat_event(&v.to_string()).unwrap())
            .unwrap(),
    );
    s.push_str(&e.push(&ChatEvent::Done).unwrap());
    assert!(!s.contains("[DONE]"));
    let mut framing = Decoder::new(100_000);
    let events = framing.push(s.as_bytes()).unwrap();
    framing.finish().unwrap();
    let mut d = StreamDecoder::new();
    let mut output = Vec::new();
    for (i, event) in events.iter().enumerate() {
        let v: Value = serde_json::from_str(&event.data).unwrap();
        assert_eq!(v["sequence_number"], i);
        output.extend(d.push(event).unwrap());
    }
    d.finish().unwrap();
    assert!(output.last().unwrap().is_done());
    let last: Value = serde_json::from_str(&events.last().unwrap().data).unwrap();
    assert_eq!(last["type"], "response.incomplete");
    assert_eq!(last["response"]["model"], "alias");
    assert_eq!(last["response"]["output"][2]["arguments"], "{}");
    assert_eq!(last["response"]["usage"]["input_tokens"], 3);
}
#[test]
fn parallel_call_history_stays_one_assistant_turn_and_strict_defaults_are_explicit() {
    let mut v = json!({"model":"m","input":[{"role":"assistant","content":"Looking up"},{"type":"function_call","call_id":"a","name":"a","arguments":"{}"},{"type":"function_call","call_id":"b","name":"b","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":"A"},{"type":"function_call_output","call_id":"b","output":"B"}],"tools":[{"type":"function","name":"a","strict":false,"parameters":{"type":"object"}}]});
    let r = decode_chat(v.clone()).unwrap();
    assert_eq!(r.messages.len(), 3);
    assert_eq!(r.messages[0].tool_calls.as_ref().unwrap().len(), 2);
    assert_eq!(
        serde_json::to_value(&r.openai.tools).unwrap()[0]["function"].get("strict"),
        None
    );
    assert_eq!(encode_chat(&r).unwrap()["tools"][0]["strict"], false);
    v["tools"][0].as_object_mut().unwrap().remove("strict");
    assert!(decode_chat(v).is_err());
    assert!(decode_chat(json!({"model":"m","input":"x","stream":true,"stream_options":{"include_obfuscation":true}})).is_err());
}
#[test]
fn response_echoes_normalize_and_refusal_uses_canonical_refusal_field() {
    let mut v = response(
        json!([{"id":"m","type":"message","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"No"}]}]),
        "completed",
    );
    v["service_tier"] = json!("auto");
    v["text"] = json!({"format":{"type":"text"},"verbosity":"medium"});
    let r = decode_chat_response(v).unwrap();
    assert!(r.service_tier.is_none());
    assert_eq!(r.choices[0].message.refusal.as_deref(), Some("No"));
    assert!(r.choices[0].message.content.is_none());
}
#[test]
fn rejects_contradictory_status_usage_and_output_order() {
    let base = response(json!([message("hello")]), "completed");
    for (path, value) in [
        (vec!["output", "0", "status"], json!("incomplete")),
        (vec!["usage", "total_tokens"], json!(99)),
        (
            vec!["usage", "input_tokens_details", "cached_tokens"],
            json!(9),
        ),
        (
            vec!["usage", "output_tokens_details", "reasoning_tokens"],
            json!(9),
        ),
    ] {
        let mut v = base.clone();
        let mut at = &mut v;
        for key in path {
            at = if let Ok(index) = key.parse::<usize>() {
                &mut at[index]
            } else {
                &mut at[key]
            };
        }
        *at = value;
        assert!(decode_chat_response(v).is_err());
    }
    let mut v = base;
    v["output"][0]["content"] = json!([{"type":"refusal","refusal":"No"},{"type":"output_text","text":"later","annotations":[]}]);
    assert!(decode_chat_response(v).is_err());
}
fn text_stream() -> Vec<Event> {
    let mut added = message("");
    added["status"] = json!("in_progress");
    added["content"] = json!([]);
    vec![
        created(),
        event(
            1,
            "response.output_item.added",
            json!({"output_index":0,"item":added}),
        ),
        event(
            2,
            "response.content_part.added",
            json!({"output_index":0,"content_index":0,"item_id":"msg_test","part":{"type":"output_text","text":"","annotations":[]}}),
        ),
        event(
            3,
            "response.output_text.delta",
            json!({"output_index":0,"content_index":0,"item_id":"msg_test","delta":"hello","logprobs":[]}),
        ),
        event(
            4,
            "response.output_text.done",
            json!({"output_index":0,"content_index":0,"item_id":"msg_test","text":"hello","logprobs":[]}),
        ),
        event(
            5,
            "response.content_part.done",
            json!({"output_index":0,"content_index":0,"item_id":"msg_test","part":{"type":"output_text","text":"hello","annotations":[]}}),
        ),
        event(
            6,
            "response.output_item.done",
            json!({"output_index":0,"item":message("hello")}),
        ),
        event(
            7,
            "response.completed",
            json!({"response":response(json!([message("hello")]),"completed")}),
        ),
    ]
}
#[test]
fn stream_snapshots_identity_lifecycle_and_bounds_are_enforced() {
    let valid = text_stream();
    let mut d = StreamDecoder::new();
    let mut content = String::new();
    for e in &valid {
        for c in d.push(e).unwrap() {
            if let ChatEvent::Chunk(c) = c {
                for choice in c.choices {
                    content.push_str(choice.delta.content.as_deref().unwrap_or(""));
                }
            }
        }
    }
    assert_eq!(content, "hello");
    d.finish().unwrap();
    assert!(d.push(&valid[7]).is_err());
    for (at, key, value) in [
        (3, "item_id", json!("wrong")),
        (3, "content_index", json!(2)),
        (4, "text", json!("conflict")),
        (3, "type", json!("response.refusal.delta")),
    ] {
        let mut frames = valid.clone();
        let mut v: Value = serde_json::from_str(&frames[at].data).unwrap();
        v[key] = value;
        frames[at].data = v.to_string();
        let mut d = StreamDecoder::new();
        assert!(frames.iter().any(|e| d.push(e).is_err()));
        assert!(d.finish().is_err());
    }
    for at in [6, 7] {
        let mut frames = valid.clone();
        let mut v: Value = serde_json::from_str(&frames[at].data).unwrap();
        if at == 6 {
            v["item"]["content"][0]["text"] = json!("wrong");
        } else {
            v["response"]["output"][0]["content"][0]["text"] = json!("wrong");
        }
        frames[at].data = v.to_string();
        let mut d = StreamDecoder::new();
        assert!(frames.iter().any(|e| d.push(e).is_err()));
    }
    let mut d = StreamDecoder::with_limit(700);
    for e in &valid[..3] {
        d.push(e).unwrap();
    }
    let mut failed = false;
    for n in 3..30 {
        if d.push(&event(n,"response.output_text.delta",json!({"output_index":0,"content_index":0,"item_id":"msg_test","delta":"x".repeat(100)}))).is_err() { failed=true;break; }
    }
    assert!(failed, "accumulated small deltas must be bounded");
    let mut e = StreamEncoder::with_limit("m".into(), 700);
    let c = json!({"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"x".repeat(100)},"finish_reason":null}]});
    let c = openai::decode_chat_event(&c.to_string()).unwrap();
    let mut failed = false;
    for _ in 0..30 {
        if e.push(&c).is_err() {
            failed = true;
            break;
        }
    }
    assert!(failed);
}
#[test]
fn message_output_items_cannot_overlap_or_emit_text_after_refusal() {
    for next in [
        json!({"type":"message","id":"second","role":"assistant","status":"in_progress","content":[]}),
        json!({"type":"function_call","id":"fc","call_id":"call","name":"f","arguments":"","status":"in_progress"}),
    ] {
        let mut d = StreamDecoder::new();
        let frames = text_stream();
        for e in &frames[..4] {
            d.push(e).unwrap();
        }
        assert!(
            d.push(&event(
                4,
                "response.output_item.added",
                json!({"output_index":1,"item":next})
            ))
            .is_err()
        );
    }
    let mut frames = text_stream();
    let mut v: Value = serde_json::from_str(&frames[3].data).unwrap();
    v["arguments"] = json!("unexpected");
    frames[3].data = v.to_string();
    let mut d = StreamDecoder::new();
    assert!(
        frames.iter().any(|e| d.push(e).is_err()),
        "cross-event fields cannot be silently ignored"
    );
}
#[test]
fn rejects_unrepresentable_fingerprint_in_json_and_stream() {
    let mut r = decode_chat_response(response(json!([message("hello")]), "completed")).unwrap();
    r.system_fingerprint = Some("fp_native".into());
    assert!(encode_chat_response(&r).is_err());
    let chunk = json!({"id":"c","object":"chat.completion.chunk","created":1,"model":"m","system_fingerprint":"fp_native","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":null}]});
    let mut encoder = StreamEncoder::new("m".into());
    assert!(
        encoder
            .push(&openai::decode_chat_event(&chunk.to_string()).unwrap())
            .is_err()
    );
}
#[test]
fn rejects_cross_event_fields_and_late_progress() {
    let frames = text_stream();
    for (at, key, value) in [
        (0, "delta", json!("lost")),
        (1, "content_index", json!(0)),
        (3, "response", response(json!([]), "failed")),
        (6, "item_id", json!("wrong")),
        (7, "delta", json!("lost")),
    ] {
        let mut modified = frames.clone();
        let mut v: Value = serde_json::from_str(&modified[at].data).unwrap();
        v[key] = value;
        modified[at].data = v.to_string();
        let mut d = StreamDecoder::new();
        assert!(modified.iter().any(|e| d.push(e).is_err()));
        assert!(d.finish().is_err());
    }
    let mut d = StreamDecoder::new();
    for e in &frames[..2] {
        d.push(e).unwrap();
    }
    assert!(
        d.push(&event(
            2,
            "response.in_progress",
            json!({"response":response(json!([]),"in_progress")})
        ))
        .is_err()
    );
}
#[test]
fn tool_delta_identity_argument_snapshots_and_done_order_are_validated() {
    let item = json!({"type":"function_call","id":"fc","call_id":"call","name":"f","arguments":"","status":"in_progress"});
    let prefix = [
        created(),
        event(
            1,
            "response.output_item.added",
            json!({"output_index":0,"item":item}),
        ),
    ];
    for (kind, fields) in [
        (
            "response.function_call_arguments.delta",
            json!({"output_index":0,"item_id":"wrong","delta":"{}"}),
        ),
        (
            "response.function_call_arguments.done",
            json!({"output_index":0,"item_id":"fc","arguments":"conflict"}),
        ),
        (
            "response.output_item.done",
            json!({"output_index":0,"item":{"type":"function_call","id":"fc","call_id":"call","name":"f","arguments":"","status":"completed"}}),
        ),
    ] {
        let mut d = StreamDecoder::new();
        for e in &prefix {
            d.push(e).unwrap();
        }
        assert!(d.push(&event(2, kind, fields)).is_err());
    }
    let mut d = StreamDecoder::new();
    for e in &prefix {
        d.push(e).unwrap();
    }
    d.push(&event(
        2,
        "response.function_call_arguments.done",
        json!({"output_index":0,"item_id":"fc","arguments":""}),
    ))
    .unwrap();
    assert!(
        d.push(&event(
            3,
            "response.function_call_arguments.delta",
            json!({"output_index":0,"item_id":"fc","delta":"late"})
        ))
        .is_err()
    );
}
