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
fn static_messages_store_text_before_calls_in_ordered_items() {
    let parts =
        json!([{"text":"Checking"},{"functionCall":{"id":"call-1","name":"weather","args":{}}}]);
    let request = decode_chat(
        json!({"contents":[{"role":"model","parts":parts},{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"weather","response":{}}}]}]}),
        "m",
        false,
    )
    .unwrap();
    let response = decode_chat_response(
        json!({"candidates":[{"content":{"role":"model","parts":parts},"finishReason":"STOP"}]}),
    )
    .unwrap();
    for message in [
        serde_json::to_value(&request.messages[0]).unwrap(),
        serde_json::to_value(&response.choices[0].message).unwrap(),
    ] {
        assert_eq!(message["items"][0]["type"], "content");
        assert_eq!(message["items"][1]["type"], "tool_call");
        assert!(message.get("content").is_none());
        assert!(message.get("tool_calls").is_none());
    }
    assert_eq!(
        encode_chat(&request).unwrap()["contents"][0]["parts"],
        parts
    );
    assert_eq!(
        encode_chat_response(&response).unwrap()["candidates"][0]["content"]["parts"],
        parts
    );
}
#[test]
fn stream_parts_keep_receive_order_across_frames() {
    let mut decoder = StreamDecoder::new();
    let mut positions = Vec::new();
    for parts in [
        json!([{"text":"summary","thought":true},{"text":"answer"}]),
        json!([{"functionCall":{"id":"a","name":"f","args":{}}}]),
    ] {
        for event in decoder
            .push(&event(json!({"candidates":[{"content":{"parts":parts}}]})))
            .unwrap()
        {
            let ChatEvent::Chunk(chunk) = event else {
                panic!()
            };
            for choice in chunk.choices {
                assert!(matches!(
                    choice.delta.events.first().unwrap().delta,
                    PartDelta::Start(_)
                ));
                assert!(matches!(
                    choice.delta.events.last().unwrap().delta,
                    PartDelta::End
                ));
                for event in choice.delta.events {
                    if !matches!(event.delta, PartDelta::Start(_) | PartDelta::End) {
                        positions.push(event.position);
                    }
                }
            }
        }
    }
    assert_eq!(
        positions,
        (0..3)
            .map(|item| StreamPosition {
                item: StreamItem::Ordered(item),
                part: 0
            })
            .collect::<Vec<_>>()
    );
}

#[test]
fn many_small_stream_parts_do_not_accumulate_position_state() {
    let mut decoder = StreamDecoder::with_limit(1024);
    let mut encoder = StreamEncoder::with_limit("m".into(), 1024);
    for _ in 0..256 {
        for event in decoder
            .push(&event(
                json!({"candidates":[{"content":{"parts":[{"text":"x"}]}}]}),
            ))
            .unwrap()
        {
            assert!(encoder.push(&event).unwrap().contains("\"text\":\"x\""));
        }
    }
    for event in decoder
        .push(&event(json!({"candidates":[{"finishReason":"STOP"}]})))
        .unwrap()
    {
        encoder.push(&event).unwrap();
    }
    for event in decoder.finish().unwrap() {
        assert!(encoder.push(&event).unwrap().contains("STOP"));
    }
}

#[test]
fn stream_part_expansion_counts_heap_allocated_events() {
    let frame = event(json!({"candidates":[{"content":{"parts":[{"text":""},{"text":""}]}}]}));
    assert!(frame.data.len() < 1024);
    assert!(StreamDecoder::with_limit(1024).push(&frame).is_err());
}

#[test]
fn multi_event_encoder_batch_counts_structures_and_text_payloads() {
    for texts in [vec![String::new(); 8], vec!["x".repeat(400); 2]] {
        let delta = Delta {
            role: None,
            events: texts
                .into_iter()
                .enumerate()
                .map(|(index, text)| PositionedDelta {
                    position: StreamPosition {
                        item: StreamItem::Ordered(index as u32),
                        part: 0,
                    },
                    delta: PartDelta::Text(text),
                })
                .collect(),
        };
        assert!(
            StreamEncoder::with_limit("m".into(), 1024)
                .push(&chunk(delta, None))
                .is_err()
        );
    }
}

#[test]
fn bounded_ordered_queue_preserves_parallel_call_start_order_and_blocked_text() {
    let at = |item, delta| PositionedDelta {
        position: StreamPosition {
            item: StreamItem::Ordered(item),
            part: 0,
        },
        delta,
    };
    let call = |index, id: &str| {
        PartDelta::ToolCall(ToolCallDelta {
            gemini: None,
            index,
            id: Some(id.into()),
            r#type: Some(FunctionType::Function),
            function: Some(FunctionDelta {
                name: Some("f".into()),
                arguments: Some("{}".into()),
            }),
        })
    };
    let prefix = vec![
        at(0, PartDelta::Start(StreamPartKind::ToolCall)),
        at(0, call(0, "a")),
        at(1, PartDelta::Start(StreamPartKind::Text)),
        at(1, PartDelta::Text("between".into())),
        at(1, PartDelta::End),
        at(2, PartDelta::Start(StreamPartKind::ToolCall)),
        at(2, call(1, "b")),
        at(2, PartDelta::End),
    ];
    let mut encoder = StreamEncoder::new("m".into());
    assert_eq!(
        encoder
            .push(&chunk(
                Delta {
                    role: None,
                    events: prefix.clone()
                },
                None
            ))
            .unwrap(),
        ""
    );
    let output = encoder
        .push(&chunk(
            Delta {
                role: None,
                events: vec![at(0, PartDelta::End)],
            },
            None,
        ))
        .unwrap();
    let value: serde_json::Value =
        serde_json::from_str(output.trim().strip_prefix("data: ").unwrap()).unwrap();
    let parts = &value["candidates"][0]["content"]["parts"];
    assert_eq!(parts[0]["functionCall"]["id"], "a");
    assert_eq!(parts[1]["text"], "between");
    assert_eq!(parts[2]["functionCall"]["id"], "b");
    let mut truncated = StreamEncoder::new("m".into());
    truncated
        .push(&chunk(
            Delta {
                role: None,
                events: prefix,
            },
            None,
        ))
        .unwrap();
    assert!(truncated.push(&ChatEvent::Done).is_err());

    let mut small = StreamEncoder::with_limit("m".into(), 1024);
    small
        .push(&chunk(
            Delta {
                role: None,
                events: vec![
                    at(0, PartDelta::Start(StreamPartKind::ToolCall)),
                    at(0, call(0, "a")),
                    at(1, PartDelta::Start(StreamPartKind::Text)),
                ],
            },
            None,
        ))
        .unwrap();
    let text = chunk(
        Delta {
            role: None,
            events: vec![at(1, PartDelta::Text("x".repeat(400)))],
        },
        None,
    );
    assert!(
        [&text, &text, &text]
            .into_iter()
            .any(|event| small.push(event).is_err())
    );
}

#[test]
fn ordered_part_and_response_container_lifecycles_fail_closed() {
    let at = |item, delta| PositionedDelta {
        position: StreamPosition {
            item: StreamItem::Ordered(item),
            part: 0,
        },
        delta,
    };
    let frame = |events| chunk(Delta { role: None, events }, None);
    let start = at(0, PartDelta::Start(StreamPartKind::Text));
    for invalid in [start.clone(), at(1, PartDelta::End)] {
        let mut encoder = StreamEncoder::new("m".into());
        encoder.push(&frame(vec![start.clone()])).unwrap();
        assert!(encoder.push(&frame(vec![invalid])).is_err());
    }
    let mut encoder = StreamEncoder::new("m".into());
    encoder
        .push(&frame(vec![start, at(0, PartDelta::End)]))
        .unwrap();
    assert!(
        encoder
            .push(&frame(vec![at(0, PartDelta::Text("late".into()))]))
            .is_err()
    );

    let mut encoder = StreamEncoder::new("m".into());
    encoder
        .push(&frame(vec![at(
            0,
            PartDelta::ResponsesItemStart(ResponsesItemStart::FunctionCall {
                id: "item_a".into(),
            }),
        )]))
        .unwrap();
    assert!(
        encoder
            .push(&frame(vec![at(0, PartDelta::Start(StreamPartKind::Text))]))
            .is_err()
    );

    let mut encoder = StreamEncoder::new("m".into());
    encoder
        .push(&frame(vec![at(
            0,
            PartDelta::ResponsesItemStart(ResponsesItemStart::Message { id: "msg_a".into() }),
        )]))
        .unwrap();
    assert!(
        encoder
            .push(&frame(vec![at(
                0,
                PartDelta::ResponsesItemEnd(
                    nyro_protocol::openai::responses::ItemStatus::InProgress
                )
            )]))
            .is_err()
    );
}

#[test]
fn ordered_parts_cannot_bypass_an_earlier_implicit_tool_call() {
    let mut encoder = StreamEncoder::new("m".into());
    let implicit = nyro_llm::codec::openai::decode_chat_event(&json!({"id":"r","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"first","type":"function","function":{"name":"f","arguments":"{}"}}]}}]}).to_string()).unwrap();
    encoder.push(&implicit).unwrap();
    let events = [
        PartDelta::Start(StreamPartKind::Text),
        PartDelta::Text("later".into()),
        PartDelta::End,
    ]
    .into_iter()
    .map(|delta| PositionedDelta {
        position: StreamPosition {
            item: StreamItem::Ordered(0),
            part: 0,
        },
        delta,
    })
    .collect();
    assert!(
        encoder
            .push(&chunk(Delta { role: None, events }, None))
            .is_err()
    );
}

#[test]
fn response_containers_reserve_order_before_their_first_leaf() {
    let at = |item, delta| PositionedDelta {
        position: StreamPosition {
            item: StreamItem::Ordered(item),
            part: 0,
        },
        delta,
    };
    let start = |item, id: &str| {
        at(
            item,
            PartDelta::ResponsesItemStart(ResponsesItemStart::Message { id: id.into() }),
        )
    };
    let end = |item| {
        at(
            item,
            PartDelta::ResponsesItemEnd(nyro_protocol::openai::responses::ItemStatus::Completed),
        )
    };
    for empty_first in [false, true] {
        let mut encoder = StreamEncoder::new("m".into());
        let prefix = vec![
            start(0, "a"),
            start(1, "b"),
            at(1, PartDelta::Start(StreamPartKind::Text)),
            at(1, PartDelta::Text("B".into())),
            at(1, PartDelta::End),
            end(1),
        ];
        assert!(
            encoder
                .push(&chunk(
                    Delta {
                        role: None,
                        events: prefix
                    },
                    None
                ))
                .unwrap()
                .is_empty()
        );
        let mut suffix = vec![];
        if !empty_first {
            suffix.extend([
                at(0, PartDelta::Start(StreamPartKind::Text)),
                at(0, PartDelta::Text("A".into())),
                at(0, PartDelta::End),
            ]);
        }
        suffix.push(end(0));
        let output = encoder
            .push(&chunk(
                Delta {
                    role: None,
                    events: suffix,
                },
                None,
            ))
            .unwrap();
        let value: serde_json::Value =
            serde_json::from_str(output.trim().strip_prefix("data: ").unwrap()).unwrap();
        let texts: Vec<_> = value["candidates"][0]["content"]["parts"]
            .as_array()
            .unwrap()
            .iter()
            .map(|p| p["text"].as_str().unwrap())
            .collect();
        assert_eq!(
            texts,
            if empty_first {
                vec!["B"]
            } else {
                vec!["A", "B"]
            }
        );
    }
    let mut encoder = StreamEncoder::with_limit("m".into(), 1024);
    encoder
        .push(&chunk(
            Delta {
                role: None,
                events: vec![
                    start(0, "a"),
                    start(1, "b"),
                    at(1, PartDelta::Start(StreamPartKind::Text)),
                ],
            },
            None,
        ))
        .unwrap();
    let blocked = chunk(
        Delta {
            role: None,
            events: vec![at(1, PartDelta::Text("x".repeat(100)))],
        },
        None,
    );
    assert!(encoder.push(&blocked).unwrap().is_empty());
    assert!(
        (0..10).any(|_| encoder.push(&blocked).is_err()),
        "a container without leaves still bounds later buffered text"
    );
}

#[test]
fn encoder_preserves_static_interleaving_and_rejects_position_reassignment() {
    let mut response = decode_chat_response(json!({"candidates":[{"content":{"parts":[{"text":"before"},{"functionCall":{"id":"a","name":"f","args":{}}}]},"finishReason":"STOP"}]})).unwrap();
    response.choices[0].message.items.swap(0, 1);
    let output = encode_chat_response(&response).unwrap();
    assert!(
        output["candidates"][0]["content"]["parts"][0]
            .get("functionCall")
            .is_some()
    );
    assert_eq!(
        output["candidates"][0]["content"]["parts"][1]["text"],
        "before"
    );

    let mut first = tool_delta("{", true);
    first.events[0].position.item = StreamItem::Ordered(0);
    let mut second = tool_delta("}", false);
    second.events[0].position.item = StreamItem::Ordered(1);
    let mut encoder = StreamEncoder::new("m".into());
    encoder.push(&chunk(first, None)).unwrap();
    assert!(encoder.push(&chunk(second, None)).is_err());

    let mut encoder = StreamEncoder::new("m".into());
    encoder
        .push(&chunk(
            Delta {
                role: None,
                events: vec![PositionedDelta {
                    position: StreamPosition {
                        item: StreamItem::Ordered(0),
                        part: 0,
                    },
                    delta: PartDelta::Text("before".into()),
                }],
            },
            None,
        ))
        .unwrap();
    let mut call = tool_delta("{}", true);
    call.events[0].position.item = StreamItem::Ordered(0);
    assert!(encoder.push(&chunk(call, None)).is_err());
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
fn rejects_non_string_thought_signature() {
    assert!(decode_chat_response(json!({"candidates":[{"content":{"parts":[{"text":"x","thoughtSignature":7}]},"finishReason":"STOP"}]})).is_err());
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
        events: vec![PositionedDelta {
            position: StreamPosition {
                item: StreamItem::OpenAiTool(0),
                part: 0,
            },
            delta: PartDelta::ToolCall(ToolCallDelta {
                gemini: None,
                index: 0,
                id: first.then(|| "id1".into()),
                r#type: Some(FunctionType::Function),
                function: Some(FunctionDelta {
                    name: first.then(|| "weather".into()),
                    arguments: Some(args.into()),
                }),
            }),
        }],
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
    let t = c.choices[0]
        .delta
        .events
        .iter()
        .find_map(|event| match &event.delta {
            PartDelta::ToolCall(call) => Some(call),
            _ => None,
        })
        .unwrap();
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
        r.messages[1].content().cloned(),
        Some(Content::Parts(vec![ContentPart::Text {
            anthropic_cache_control: None,
            prompt_cache_breakpoint: None,
            text: "before".into()
        }]))
    );
    assert_eq!(r.messages[2].tool_call_id.as_deref(), Some("b"));
    assert_eq!(
        r.messages[2].content().cloned(),
        Some(Content::Text(r#"{"value":2}"#.into()))
    );
    assert_eq!(
        r.messages[3].content().cloned(),
        Some(Content::Parts(vec![ContentPart::Text {
            anthropic_cache_control: None,
            prompt_cache_breakpoint: None,
            text: "between".into()
        }]))
    );
    assert_eq!(r.messages[4].tool_call_id.as_deref(), Some("a"));
    assert_eq!(
        r.messages[5].content().cloned(),
        Some(Content::Parts(vec![ContentPart::Text {
            anthropic_cache_control: None,
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
    let calls: Vec<_> = r.messages[0].tool_calls().collect();
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
    *r.messages[1].content_mut().unwrap() = Content::Parts(vec![
        ContentPart::Text {
            anthropic_cache_control: None,
            prompt_cache_breakpoint: None,
            text: "one".into(),
        },
        ContentPart::Text {
            anthropic_cache_control: None,
            prompt_cache_breakpoint: None,
            text: "two".into(),
        },
    ]);
    assert!(encode_chat(&r).is_err());
    for (parts, expected) in [
        (vec![], json!({"result":""})),
        (
            vec![ContentPart::Text {
                anthropic_cache_control: None,
                prompt_cache_breakpoint: None,
                text: "one".into(),
            }],
            json!({"result":"one"}),
        ),
    ] {
        *r.messages[1].content_mut().unwrap() = Content::Parts(parts);
        assert_eq!(
            encode_chat(&r).unwrap()["contents"][1]["parts"][0]["functionResponse"]["response"],
            expected
        );
    }
}

#[test]
fn decoder_rejects_unmatched_identity_and_accepts_interleaved_assistant() {
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
        .is_ok()
    );
}
#[test]
fn explicit_stream_parts_allow_tool_then_text_without_changing_legacy_slots() {
    let tool = json!({"functionCall":{"name":"f","args":{}}});
    let text = json!({"text":"after"});
    let mut d = StreamDecoder::new();
    assert!(
        d.push(&event(
            json!({"candidates":[{"content":{"parts":[tool.clone(),text.clone()]}}]})
        ))
        .is_ok()
    );
    let mut d = StreamDecoder::new();
    d.push(&event(json!({"candidates":[{"content":{"parts":[tool]}}]})))
        .unwrap();
    assert!(
        d.push(&event(json!({"candidates":[{"content":{"parts":[text]}}]})))
            .is_ok()
    );
    let mut e = StreamEncoder::new("m".into());
    e.push(&chunk(tool_delta("{}", true), None)).unwrap();
    assert!(
        e.push(&chunk(
            Delta {
                events: vec![PositionedDelta {
                    position: StreamPosition {
                        item: StreamItem::OpenAiMessage,
                        part: 0
                    },
                    delta: PartDelta::Text("after".into()),
                }],
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
