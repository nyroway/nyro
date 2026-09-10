use nyro_llm::{codec::gemini::*, ir::*};
use nyro_protocol::framing::Event;
use serde_json::json;
fn event(v: serde_json::Value) -> Event {
    Event {
        data: v.to_string(),
        event: None,
    }
}
#[test]
fn request_roundtrip_and_tool_identity() {
    let v = json!({"systemInstruction":{"parts":[{"text":"Be helpful"}]},"contents":[{"role":"user","parts":[{"text":"Weather?"}]},{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"weather","args":{"city":"Paris"}}}]},{"role":"user","parts":[{"functionResponse":{"name":"weather","response":{"temp":20}}}]}],"tools":[{"functionDeclarations":[{"name":"weather","parametersJsonSchema":{"type":"object"}}]}],"generationConfig":{"temperature":0.5,"maxOutputTokens":100,"topP":0.9,"stopSequences":["END"]}});
    let r = decode_chat(v, "public", false).unwrap();
    assert_eq!(r.messages[3].tool_call_id.as_deref(), Some("call-1"));
    let w = encode_chat(&r).unwrap();
    assert_eq!(
        w["contents"][2]["parts"][0]["functionResponse"]["id"],
        "call-1"
    );
    assert_eq!(w["generationConfig"]["maxOutputTokens"], 100);
    assert!(decode_chat(json!({"contents":[],"cachedContent":"x"}), "m", false).is_err());
}
#[test]
fn response_and_stream_terminal() {
    let v = json!({"responseId":"r","modelVersion":"m","candidates":[{"content":{"role":"model","parts":[{"text":"Hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}});
    let r = decode_chat_response(v.clone()).unwrap();
    assert_eq!(r.usage.unwrap().total_tokens, 3);
    let mut d = StreamDecoder::new();
    let out = d.push(&event(v)).unwrap();
    assert!(!out.iter().any(ChatEvent::is_done));
    assert_eq!(d.finish().unwrap(), vec![ChatEvent::Done]);
    let mut d = StreamDecoder::new();
    d.push(&event(
        json!({"candidates":[{"content":{"parts":[{"text":"Hi"}]}}]}),
    ))
    .unwrap();
    assert!(d.finish().is_err());
    let mut d = StreamDecoder::new();
    assert!(d.push(&event(json!({"error":{"message":"bad"}}))).is_err());
    assert!(d.finish().is_err());
}
#[test]
fn rejects_thought_signature() {
    assert!(decode_chat_response(json!({"candidates":[{"content":{"parts":[{"text":"x","thoughtSignature":"abc"}]},"finishReason":"STOP"}]})).is_err());
}
fn chunk(delta: Delta, finish: Option<&str>) -> ChatEvent {
    ChatEvent::Chunk(Box::new(ChatChunk {
        id: "r".into(),
        object: "chat.completion.chunk".into(),
        created: 0,
        model: "upstream".into(),
        choices: vec![StreamChoice {
            index: 0,
            delta,
            finish_reason: finish.map(str::to_owned),
            logprobs: None,
        }],
        usage: None,
        system_fingerprint: None,
        service_tier: None,
        obfuscation: None,
    }))
}
fn tool_delta(args: &str, first: bool) -> Delta {
    Delta {
        tool_calls: Some(vec![ToolCallDelta {
            index: 0,
            id: first.then(|| "id1".into()),
            r#type: Some(FunctionType::Function),
            function: Some(FunctionDelta {
                name: first.then(|| "weather".into()),
                arguments: Some(args.into()),
            }),
        }]),
        ..Default::default()
    }
}
#[test]
fn streamed_tool_fragments_roundtrip_and_limits() {
    let mut e = StreamEncoder::new("public".into());
    assert_eq!(
        e.push(&chunk(tool_delta("{\"city\":", true), None))
            .unwrap(),
        ""
    );
    assert_eq!(
        e.push(&chunk(tool_delta("\"Paris\"}", false), None))
            .unwrap(),
        ""
    );
    let frame = e
        .push(&chunk(Delta::default(), Some("tool_calls")))
        .unwrap();
    assert!(frame.contains("public"));
    assert!(!frame.contains("upstream"));
    let v: serde_json::Value =
        serde_json::from_str(frame.trim().strip_prefix("data: ").unwrap()).unwrap();
    let mut d = StreamDecoder::new();
    let events = d.push(&event(v)).unwrap();
    let ChatEvent::Chunk(c) = &events[0] else {
        panic!()
    };
    let t = &c.choices[0].delta.tool_calls.as_ref().unwrap()[0];
    assert_eq!(t.id.as_deref(), Some("id1"));
    assert_eq!(
        t.function.as_ref().unwrap().arguments.as_deref(),
        Some("{\"city\":\"Paris\"}")
    );
    let terminal = e.push(&ChatEvent::Done).unwrap();
    let v = serde_json::from_str(terminal.trim().strip_prefix("data: ").unwrap()).unwrap();
    d.push(&event(v)).unwrap();
    assert_eq!(d.finish().unwrap(), vec![ChatEvent::Done]);
    let mut e = StreamEncoder::new("m".into());
    e.push(&chunk(tool_delta("{", true), None)).unwrap();
    assert!(
        e.push(&chunk(Delta::default(), Some("tool_calls")))
            .is_err()
    );
    assert!(e.push(&ChatEvent::Done).is_err());
    let mut e = StreamEncoder::with_limit("m".into(), 10);
    assert!(
        e.push(&chunk(tool_delta("{\"city\":", true), None))
            .is_err()
    );
    let mut d = StreamDecoder::with_limit(2);
    assert!(d.push(&event(json!({"candidates":[]}))).is_err());
}
#[test]
fn terminal_errors_never_become_success() {
    let mut d = StreamDecoder::new();
    d.push(&event(json!({"candidates":[{"finishReason":"STOP"}]})))
        .unwrap();
    assert!(
        d.push(&event(json!({"error":{"message":"failed"}})))
            .is_err()
    );
    assert!(d.finish().is_err());
    let mut e = StreamEncoder::new("m".into());
    assert!(e.push(&ChatEvent::Done).is_err());
    for finish in [
        "MALFORMED_FUNCTION_CALL",
        "OTHER",
        "FINISH_REASON_UNSPECIFIED",
    ] {
        assert!(decode_chat_response(json!({"candidates":[{"finishReason":finish}]})).is_err());
    }
}
#[test]
fn native_schema_options_and_usage_mapping() {
    let r=decode_chat(json!({"contents":[{"parts":[{"text":"hi"}]}],"tools":[{"functionDeclarations":[{"name":"f","parameters":{"type":"OBJECT","properties":{"x":{"type":"STRING","nullable":true}}}}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["f"]}}}),"m",true).unwrap();
    let v = encode_chat(&r).unwrap();
    assert_eq!(
        v["tools"][0]["functionDeclarations"][0]["parametersJsonSchema"]["properties"]["x"]["type"],
        json!(["string", "null"])
    );
    assert_eq!(
        v["toolConfig"]["functionCallingConfig"]["allowedFunctionNames"],
        json!(["f"])
    );
    let v = json!({"candidates":[{"content":{"parts":[{"functionCall":{"id":"abc","name":"f","args":{}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":15,"cachedContentTokenCount":3,"thoughtsTokenCount":3}});
    let r = decode_chat_response(v).unwrap();
    assert_eq!(r.choices[0].finish_reason.as_deref(), Some("tool_calls"));
    let encoded = encode_chat_response(&r).unwrap();
    assert_eq!(encoded["usageMetadata"]["thoughtsTokenCount"], 3);
    assert_eq!(encoded["usageMetadata"]["cachedContentTokenCount"], 3);
    for field in ["topK", "thinkingConfig", "responseMimeType"] {
        let mut v = json!({"contents":[{"parts":[{"text":"x"}]}],"generationConfig":{}});
        v["generationConfig"][field] = json!(1);
        assert!(decode_chat(v, "m", false).is_err());
    }
    let mut r = nyro_llm::codec::openai::decode_chat(
        json!({"model":"m","messages":[{"role":"user","content":"x"}],"parallel_tool_calls":false}),
    )
    .unwrap();
    assert!(encode_chat(&r).is_err());
    r.openai.parallel_tool_calls = None;
    r.generation.max_completion_tokens = Some(10);
    assert!(encode_chat(&r).is_err());
}
#[test]
fn ambiguous_missing_tool_identity_is_rejected() {
    assert!(decode_chat(json!({"contents":[{"role":"model","parts":[{"functionCall":{"id":"a","name":"f","args":{}}},{"functionCall":{"id":"b","name":"f","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"f","response":{}}}]}]}),"m",false).is_err());
    assert!(
        decode_chat(
            json!({"contents":[],"systemInstruction":{"parts":[{"text":"system"}]}}),
            "m",
            false
        )
        .is_err()
    );
}

#[test]
fn mixed_user_function_responses_preserve_part_order() {
    let r = decode_chat(
        json!({"contents":[
            {"role":"model","parts":[
                {"functionCall":{"id":"a","name":"first","args":{}}},
                {"functionCall":{"id":"b","name":"second","args":{}}}
            ]},
            {"role":"user","parts":[
                {"text":"before"},
                {"functionResponse":{"name":"second","response":{"value":2}}},
                {"text":"between"},
                {"functionResponse":{"id":"a","name":"first","response":{"value":1}}},
                {"text":"after"}
            ]}
        ]}),
        "m",
        false,
    )
    .unwrap();
    assert_eq!(
        r.messages
            .iter()
            .map(|m| m.role.clone())
            .collect::<Vec<_>>(),
        vec![
            Role::Assistant,
            Role::User,
            Role::Tool,
            Role::User,
            Role::Tool,
            Role::User
        ]
    );
    assert_eq!(
        r.messages[1].content,
        Some(Content::Parts(vec![ContentPart::Text {
            prompt_cache_breakpoint: None,
            text: "before".into()
        }]))
    );
    assert_eq!(r.messages[2].tool_call_id.as_deref(), Some("b"));
    assert_eq!(
        r.messages[2].content,
        Some(Content::Text(r#"{"value":2}"#.into()))
    );
    assert_eq!(
        r.messages[3].content,
        Some(Content::Parts(vec![ContentPart::Text {
            prompt_cache_breakpoint: None,
            text: "between".into()
        }]))
    );
    assert_eq!(r.messages[4].tool_call_id.as_deref(), Some("a"));
    assert_eq!(
        r.messages[5].content,
        Some(Content::Parts(vec![ContentPart::Text {
            prompt_cache_breakpoint: None,
            text: "after".into()
        }]))
    );
    assert!(
        encode_chat(&r).is_err(),
        "interrupted result batches cannot be encoded"
    );
}

#[test]
fn generated_history_call_ids_avoid_all_explicit_ids() {
    let r = decode_chat(
        json!({"contents":[
            {"role":"model","parts":[
                {"functionCall":{"name":"generated","args":{}}},
                {"functionCall":{"id":"gemini_call_0","name":"explicit","args":{}}}
            ]},
            {"role":"user","parts":[
                {"functionResponse":{"name":"generated","response":{}}},
                {"functionResponse":{"id":"gemini_call_0","name":"explicit","response":{}}}
            ]}
        ]}),
        "m",
        false,
    )
    .unwrap();
    let calls = r.messages[0].tool_calls.as_ref().unwrap();
    let ToolCall::Function { id: generated, .. } = &calls[0];
    let ToolCall::Function { id: explicit, .. } = &calls[1];
    assert_ne!(generated, explicit);
    assert_eq!(explicit, "gemini_call_0");
    assert_eq!(r.messages[1].tool_call_id.as_ref(), Some(generated));
    assert_eq!(r.messages[2].tool_call_id.as_ref(), Some(explicit));
}

fn result_history() -> ChatRequest {
    nyro_llm::codec::openai::decode_chat(json!({"model":"m","messages":[
        {"role":"assistant","tool_calls":[
            {"type":"function","id":"a","function":{"name":"first","arguments":"{}"}},
            {"type":"function","id":"b","function":{"name":"second","arguments":"{}"}}
        ]},
        {"role":"tool","tool_call_id":"b","content":"{\"error\":\"ordinary data\"}"},
        {"role":"tool","tool_call_id":"a","content":"plain result"},
        {"role":"user","content":[{"type":"text","text":"continue"},{"type":"text","text":"please"}]}
    ]})).unwrap()
}

#[test]
fn encodes_complete_result_batch_and_following_text_in_one_content() {
    let v = encode_chat(&result_history()).unwrap();
    assert_eq!(v["contents"].as_array().unwrap().len(), 2);
    assert_eq!(
        v["contents"][1],
        json!({"role":"user","parts":[
            {"functionResponse":{"id":"b","name":"second","response":{"error":"ordinary data"}}},
            {"functionResponse":{"id":"a","name":"first","response":{"result":"plain result"}}},
            {"text":"continue"}, {"text":"please"}
        ]})
    );
    let decoded = decode_chat(v.clone(), "m", false).unwrap();
    assert_eq!(encode_chat(&decoded).unwrap(), v);
}

#[test]
fn semantic_tool_error_cannot_be_downgraded_to_gemini_text() {
    let mut r = result_history();
    assert!(
        encode_chat(&r).is_ok(),
        "an error object key alone is ordinary data"
    );
    r.messages[1].tool_error = true;
    assert!(encode_chat(&r).is_err());
}

#[test]
fn encoder_rejects_incomplete_interrupted_or_invalid_result_batches() {
    let base = result_history();
    let mut missing = base.clone();
    missing.messages.truncate(2);
    assert!(encode_chat(&missing).is_err(), "missing result");
    let mut interrupted = base.clone();
    interrupted.messages.swap(2, 3);
    assert!(
        encode_chat(&interrupted).is_err(),
        "user interrupts pending results"
    );
    let mut orphan = base.clone();
    orphan.messages.remove(0);
    assert!(encode_chat(&orphan).is_err(), "orphan result");
    let mut duplicate = base.clone();
    duplicate.messages[2].tool_call_id = Some("b".into());
    assert!(encode_chat(&duplicate).is_err(), "duplicate result");
    let mut missing_id = base;
    missing_id.messages[1].tool_call_id = None;
    assert!(encode_chat(&missing_id).is_err(), "missing result id");
}

#[test]
fn result_text_blocks_are_not_silently_concatenated() {
    let mut r = result_history();
    r.messages[1].content = Some(Content::Parts(vec![
        ContentPart::Text {
            prompt_cache_breakpoint: None,
            text: "one".into(),
        },
        ContentPart::Text {
            prompt_cache_breakpoint: None,
            text: "two".into(),
        },
    ]));
    assert!(encode_chat(&r).is_err());
    for (parts, expected) in [
        (vec![], json!({"result":""})),
        (
            vec![ContentPart::Text {
                prompt_cache_breakpoint: None,
                text: "one".into(),
            }],
            json!({"result":"one"}),
        ),
    ] {
        r.messages[1].content = Some(Content::Parts(parts));
        assert_eq!(
            encode_chat(&r).unwrap()["contents"][1]["parts"][0]["functionResponse"]["response"],
            expected
        );
    }
}

#[test]
fn decoder_rejects_unmatched_identity_and_assistant_reordering() {
    for response in [
        json!({"id":"a","name":"wrong","response":{}}),
        json!({"id":"unknown","name":"first","response":{}}),
        json!({"name":"unknown","response":{}}),
    ] {
        assert!(
            decode_chat(
                json!({"contents":[
                    {"role":"model","parts":[{"functionCall":{"id":"a","name":"first","args":{}}}]},
                    {"role":"user","parts":[{"functionResponse":response}]}
                ]}),
                "m",
                false
            )
            .is_err()
        );
    }
    assert!(
        decode_chat(
            json!({"contents":[{"role":"user","parts":[
                {"functionResponse":{"name":"first","response":{}}}
            ]}]}),
            "m",
            false
        )
        .is_err()
    );
    assert!(
        decode_chat(
            json!({"contents":[{"role":"model","parts":[
                {"functionCall":{"name":"first","args":{}}}, {"text":"after"}
            ]}]}),
            "m",
            false
        )
        .is_err()
    );
}
#[test]
fn streamed_tool_then_text_is_rejected_without_reordering() {
    let tool = json!({"functionCall":{"name":"f","args":{}}});
    let text = json!({"text":"after"});
    let mut d = StreamDecoder::new();
    assert!(
        d.push(&event(
            json!({"candidates":[{"content":{"parts":[tool.clone(),text.clone()]}}]})
        ))
        .is_err()
    );
    let mut d = StreamDecoder::new();
    d.push(&event(json!({"candidates":[{"content":{"parts":[tool]}}]})))
        .unwrap();
    assert!(
        d.push(&event(json!({"candidates":[{"content":{"parts":[text]}}]})))
            .is_err()
    );
    let mut e = StreamEncoder::new("m".into());
    e.push(&chunk(tool_delta("{}", true), None)).unwrap();
    assert!(
        e.push(&chunk(
            Delta {
                content: Some("after".into()),
                ..Default::default()
            },
            None
        ))
        .is_err()
    );
    assert!(e.push(&ChatEvent::Done).is_err());
}
#[test]
fn thought_usage_counts_are_inclusive_in_ir_and_checked() {
    let native = |usage| json!({"candidates":[{"finishReason":"STOP"}],"usageMetadata":usage});
    let r=decode_chat_response(native(json!({"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":15}))).unwrap();
    assert_eq!(r.usage.as_ref().unwrap().completion_tokens, 5);
    assert_eq!(
        encode_chat_response(&r).unwrap()["usageMetadata"]["candidatesTokenCount"],
        2
    );
    let mut d = StreamDecoder::new();
    let events=d.push(&event(native(json!({"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":15})))).unwrap();
    let ChatEvent::Chunk(c) = &events[0] else {
        panic!()
    };
    assert_eq!(c.usage.as_ref().unwrap().completion_tokens, 5);
    for u in [
        json!({"candidatesTokenCount":u64::MAX,"thoughtsTokenCount":1}),
        json!({"promptTokenCount":u64::MAX,"candidatesTokenCount":1}),
        json!({"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":12}),
        json!({"promptTokenCount":1,"cachedContentTokenCount":2}),
    ] {
        assert!(decode_chat_response(native(u)).is_err());
    }
    let mut r = r;
    r.usage.as_mut().unwrap().completion_tokens = 2;
    assert!(encode_chat_response(&r).is_err());
    r.usage.as_mut().unwrap().completion_tokens = 5;
    r.usage.as_mut().unwrap().total_tokens = 12;
    assert!(encode_chat_response(&r).is_err());
}
#[test]
fn unrepresentable_response_fingerprint_is_rejected() {
    let mut r = decode_chat_response(json!({"candidates":[{"finishReason":"STOP"}]})).unwrap();
    r.system_fingerprint = Some("fp".into());
    assert!(encode_chat_response(&r).is_err());
    let mut c = chunk(Delta::default(), Some("stop"));
    let ChatEvent::Chunk(ref mut data) = c else {
        panic!()
    };
    data.system_fingerprint = Some("fp".into());
    let mut e = StreamEncoder::new("m".into());
    assert!(e.push(&c).is_err());
    assert!(e.push(&ChatEvent::Done).is_err());
}

#[test]
fn tool_id_on_non_tool_message_is_rejected() {
    let r = nyro_llm::codec::openai::decode_chat(
        json!({"model":"m","messages":[{"role":"user","content":"x","tool_call_id":"orphan"}]}),
    )
    .unwrap();
    assert!(encode_chat(&r).is_err());
}
