use nyro_llm::{
    codec::anthropic::*,
    ir::{ChatEvent, PartDelta, PositionedDelta, StreamItem, StreamPartKind, StreamPosition},
};
use nyro_protocol::framing::Event;
use serde_json::{Value, json};

#[test]
fn interleaved_history_and_json_preserve_text_tools_and_signed_blocks() {
    let content = json!([
        {"type":"text","text":"before"},
        {"type":"tool_use","id":"a","name":"f","input":{}},
        {"type":"text","text":"between"},
        {"type":"thinking","thinking":"reason","signature":"signature-one"},
        {"type":"redacted_thinking","data":"opaque"},
        {"type":"tool_use","id":"b","name":"g","input":{"x":1}},
        {"type":"thinking","thinking":"","signature":"signature-two"},
        {"type":"text","text":"after"}
    ]);
    let request = decode_chat(json!({"model":"m","max_tokens":16,"messages":[
        {"role":"assistant","content":content},
        {"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"ok"},{"type":"tool_result","tool_use_id":"b","content":"ok"}]}
    ]})).unwrap();
    assert_eq!(
        encode_chat(&request).unwrap()["messages"][0]["content"],
        content
    );
    assert!(nyro_llm::codec::openai::encode_chat(&request).is_err());
    let response = decode_chat_response(json!({"id":"r","type":"message","role":"assistant","model":"m","content":content,"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":2}})).unwrap();
    assert_eq!(encode_chat_response(&response).unwrap()["content"], content);
    assert!(nyro_llm::codec::openai::encode_chat_response(&response).is_err());
}

#[test]
fn static_ir_uses_ordered_items_and_preserves_reordered_groups() {
    let content = json!([
        {"type":"text","text":"checking"},
        {"type":"tool_use","id":"t","name":"f","input":{}}
    ]);
    let request = decode_chat(json!({"model":"m","max_tokens":4,"messages":[
        {"role":"assistant","content":content},
        {"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}
    ]}))
    .unwrap();
    let mut value = serde_json::to_value(&request).unwrap();
    let message = &value["messages"][0];
    assert_eq!(message["items"][0]["type"], "content");
    assert_eq!(message["items"][1]["type"], "tool_call");
    assert!(message.get("content").is_none());
    assert!(message.get("tool_calls").is_none());
    assert_eq!(
        encode_chat(&request).unwrap()["messages"][0]["content"],
        content
    );
    value["messages"][0]["items"]
        .as_array_mut()
        .unwrap()
        .swap(0, 1);
    let interleaved = serde_json::from_value(value).unwrap();
    assert_eq!(
        encode_chat(&interleaved).unwrap()["messages"][0]["content"][0]["type"],
        "tool_use"
    );

    let response = decode_chat_response(json!({"id":"r","type":"message","role":"assistant","model":"m","content":content,"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}})).unwrap();
    let mut value = serde_json::to_value(&response).unwrap();
    assert_eq!(
        value["choices"][0]["message"]["items"][1]["type"],
        "tool_call"
    );
    assert_eq!(encode_chat_response(&response).unwrap()["content"], content);
    value["choices"][0]["message"]["items"]
        .as_array_mut()
        .unwrap()
        .swap(0, 1);
    assert_eq!(
        encode_chat_response(&serde_json::from_value(value).unwrap()).unwrap()["content"][0]["type"],
        "tool_use"
    );
}

#[test]
fn decoder_positions_identify_the_original_anthropic_blocks() {
    let mut decoder = StreamDecoder::new();
    decoder.push(&event(json!({"type":"message_start","message":{"id":"r","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}))).unwrap();
    for index in 0..2 {
        let events = decoder.push(&event(json!({"type":"content_block_start","index":index,"content_block":{"type":"text","text":"part"}}))).unwrap();
        let ChatEvent::Chunk(chunk) = &events[0] else {
            panic!("expected chunk")
        };
        let delta = serde_json::to_value(&chunk.choices[0].delta).unwrap();
        assert_eq!(
            delta["events"][0]["position"],
            json!({"item":{"type":"ordered","index":index},"part":0})
        );
        assert!(delta.get("content").is_none());
        decoder
            .push(&event(json!({"type":"content_block_stop","index":index})))
            .unwrap();
    }
}

#[test]
fn encoder_checks_position_ownership_without_reordering_events() {
    use nyro_llm::{codec::openai, ir::AnthropicThinkingDelta};
    let base = openai::decode_chat_event(&json!({"id":"r","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{}}]}).to_string()).unwrap();
    let chunk = |events| {
        let ChatEvent::Chunk(mut c) = base.clone() else {
            unreachable!()
        };
        c.choices[0].delta.events = events;
        ChatEvent::Chunk(c)
    };
    let positioned = |item, delta| PositionedDelta {
        position: StreamPosition {
            item: StreamItem::Ordered(item),
            part: 0,
        },
        delta,
    };
    let mut encoder = StreamEncoder::new("m".into());
    encoder
        .push(&chunk(vec![positioned(
            0,
            PartDelta::AnthropicThinking(AnthropicThinkingDelta::Start),
        )]))
        .unwrap();
    assert!(
        encoder
            .push(&chunk(vec![positioned(
                1,
                PartDelta::AnthropicThinking(AnthropicThinkingDelta::Signature {
                    signature: "signed".into()
                })
            )]))
            .is_err()
    );

    let tool = nyro_llm::ir::ToolCallDelta {
        gemini: None,
        index: 0,
        id: Some("t".into()),
        r#type: Some(nyro_llm::ir::FunctionType::Function),
        function: Some(nyro_llm::ir::FunctionDelta {
            name: Some("f".into()),
            arguments: Some("{}".into()),
        }),
    };
    let mut encoder = StreamEncoder::new("m".into());
    encoder
        .push(&chunk(vec![positioned(
            0,
            PartDelta::ToolCall(tool.clone()),
        )]))
        .unwrap();
    let mut tail = tool.clone();
    tail.id = None;
    tail.function = None;
    assert!(
        encoder
            .push(&chunk(vec![positioned(1, PartDelta::ToolCall(tail))]))
            .is_err()
    );
    let mut encoder = StreamEncoder::new("m".into());
    assert!(
        encoder
            .push(&chunk(vec![
                positioned(0, PartDelta::ToolCall(tool)),
                positioned(1, PartDelta::Text("after".into()))
            ]))
            .is_err()
    );

    let mut encoder = StreamEncoder::new("m".into());
    let output = encoder
        .push(&chunk(vec![
            positioned(0, PartDelta::Text("one".into())),
            positioned(1, PartDelta::Text("two".into())),
        ]))
        .unwrap();
    assert_eq!(output.matches("event: content_block_start\n").count(), 2);
    assert!(output.find("one").unwrap() < output.find("two").unwrap());
    assert!(
        encoder
            .push(&chunk(vec![positioned(
                0,
                PartDelta::Text("reopened".into())
            )]))
            .is_err()
    );
}
fn parts_chunk(events: Vec<PositionedDelta>) -> ChatEvent {
    let ChatEvent::Chunk(mut chunk) = nyro_llm::codec::openai::decode_chat_event(&json!({"id":"r","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{}}]}).to_string()).unwrap() else { unreachable!() };
    chunk.choices[0].delta.events = events;
    ChatEvent::Chunk(chunk)
}

fn text_part(item: u32, part: u32, text: String) -> PositionedDelta {
    PositionedDelta {
        position: StreamPosition {
            item: StreamItem::Ordered(item),
            part,
        },
        delta: PartDelta::Text(text),
    }
}

fn part(item: u32, delta: PartDelta) -> PositionedDelta {
    PositionedDelta {
        position: StreamPosition {
            item: StreamItem::Ordered(item),
            part: 0,
        },
        delta,
    }
}

fn call(index: u32, id: &str) -> PartDelta {
    PartDelta::ToolCall(nyro_llm::ir::ToolCallDelta {
        index,
        id: Some(id.into()),
        r#type: Some(nyro_llm::ir::FunctionType::Function),
        function: Some(nyro_llm::ir::FunctionDelta {
            name: Some("f".into()),
            arguments: Some("{}".into()),
        }),
        gemini: None,
    })
}

#[test]
fn explicit_parallel_tools_keep_start_order_when_ends_arrive_backwards() {
    let mut encoder = StreamEncoder::new("m".into());
    for index in 0..2 {
        encoder
            .push(&parts_chunk(vec![
                part(index, PartDelta::Start(StreamPartKind::ToolCall)),
                part(
                    index,
                    call(index, if index == 0 { "first" } else { "second" }),
                ),
            ]))
            .unwrap();
    }
    assert!(
        encoder
            .push(&parts_chunk(vec![part(1, PartDelta::End)]))
            .unwrap()
            .is_empty()
    );
    let output = encoder
        .push(&parts_chunk(vec![part(0, PartDelta::End)]))
        .unwrap();
    assert!(output.find("first").unwrap() < output.find("second").unwrap());
    assert_eq!(output.matches("event: content_block_start\n").count(), 2);
    let output = encoder
        .push(&parts_chunk(vec![
            part(2, PartDelta::Start(StreamPartKind::Text)),
            part(2, PartDelta::Text("after".into())),
            part(2, PartDelta::End),
        ]))
        .unwrap();
    assert!(output.contains("after"));
}

#[test]
fn explicit_parts_reject_wrong_lifecycle_and_unfinished_parts() {
    let start = part(0, PartDelta::Start(StreamPartKind::Text));
    for invalid in [
        start.clone(),
        part(1, PartDelta::End),
        part(0, call(0, "t")),
    ] {
        let mut encoder = StreamEncoder::new("m".into());
        encoder.push(&parts_chunk(vec![start.clone()])).unwrap();
        assert!(encoder.push(&parts_chunk(vec![invalid])).is_err());
    }
    let mut encoder = StreamEncoder::new("m".into());
    assert!(
        encoder
            .push(&parts_chunk(vec![part(0, PartDelta::End)]))
            .is_err()
    );
    let mut encoder = StreamEncoder::new("m".into());
    encoder
        .push(&parts_chunk(vec![start, part(0, PartDelta::End)]))
        .unwrap();
    assert!(
        encoder
            .push(&parts_chunk(vec![part(0, PartDelta::Text("late".into()))]))
            .is_err()
    );
    let mut encoder = StreamEncoder::new("m".into());
    encoder
        .push(&parts_chunk(vec![
            part(0, PartDelta::Start(StreamPartKind::ToolCall)),
            part(0, call(0, "t")),
        ]))
        .unwrap();
    encoder
        .push(&parts_chunk(vec![part(
            1,
            PartDelta::Start(StreamPartKind::Text),
        )]))
        .unwrap();
    assert!(encoder.push(&ChatEvent::Done).is_err());
}

#[test]
fn blocked_text_and_thinking_are_released_in_start_order() {
    use nyro_llm::ir::AnthropicThinkingDelta as T;
    let mut encoder = StreamEncoder::new("m".into());
    encoder
        .push(&parts_chunk(vec![
            part(0, PartDelta::Start(StreamPartKind::ToolCall)),
            part(0, call(0, "first")),
        ]))
        .unwrap();
    for event in [
        part(1, PartDelta::Start(StreamPartKind::Text)),
        part(1, PartDelta::Text("between".into())),
        part(1, PartDelta::End),
        part(2, PartDelta::AnthropicThinking(T::Start)),
        part(
            2,
            PartDelta::AnthropicThinking(T::Signature {
                signature: "signed".into(),
            }),
        ),
        part(2, PartDelta::AnthropicThinking(T::Stop)),
        part(3, PartDelta::Start(StreamPartKind::ToolCall)),
        part(3, call(1, "last")),
        part(3, PartDelta::End),
    ] {
        assert!(encoder.push(&parts_chunk(vec![event])).unwrap().is_empty());
    }
    let output = encoder
        .push(&parts_chunk(vec![part(0, PartDelta::End)]))
        .unwrap();
    let types: Vec<String> = output
        .lines()
        .filter_map(|line| line.strip_prefix("data: "))
        .map(|line| serde_json::from_str::<Value>(line).unwrap())
        .filter(|frame| frame["type"] == "content_block_start")
        .map(|frame| frame["content_block"]["type"].as_str().unwrap().to_owned())
        .collect();
    assert_eq!(types, ["tool_use", "text", "thinking", "tool_use"]);
    assert!(output.find("first").unwrap() < output.find("between").unwrap());
    assert!(output.find("signed").unwrap() < output.find("last").unwrap());
}

#[test]
fn only_blocked_content_accumulates_against_the_stream_limit() {
    let mut encoder = StreamEncoder::with_limit("m".into(), 4096);
    encoder
        .push(&parts_chunk(vec![part(
            0,
            PartDelta::Start(StreamPartKind::Text),
        )]))
        .unwrap();
    for _ in 0..20 {
        encoder
            .push(&parts_chunk(vec![part(
                0,
                PartDelta::Text("x".repeat(2000)),
            )]))
            .unwrap();
    }
    let mut encoder = StreamEncoder::with_limit("m".into(), 4096);
    encoder
        .push(&parts_chunk(vec![
            part(0, PartDelta::Start(StreamPartKind::ToolCall)),
            part(0, call(0, "t")),
            part(1, PartDelta::Start(StreamPartKind::Text)),
        ]))
        .unwrap();
    encoder
        .push(&parts_chunk(vec![part(
            1,
            PartDelta::Text("x".repeat(2000)),
        )]))
        .unwrap();
    assert!(
        encoder
            .push(&parts_chunk(vec![part(
                1,
                PartDelta::Text("x".repeat(2000))
            )]))
            .is_err()
    );
}

#[test]
fn response_items_preserve_order_when_an_earlier_message_part_arrives_late() {
    use nyro_llm::ir::ResponsesItemStart as R;
    use nyro_protocol::openai::responses::ItemStatus as S;
    for empty in [false, true] {
        let mut encoder = StreamEncoder::new("m".into());
        encoder
            .push(&parts_chunk(vec![part(
                0,
                PartDelta::ResponsesItemStart(R::Message { id: "a".into() }),
            )]))
            .unwrap();
        let output = encoder
            .push(&parts_chunk(vec![
                part(
                    1,
                    PartDelta::ResponsesItemStart(R::FunctionCall { id: "b".into() }),
                ),
                part(1, PartDelta::Start(StreamPartKind::ToolCall)),
                part(1, call(0, "later-call")),
                part(1, PartDelta::End),
                part(1, PartDelta::ResponsesItemEnd(S::Completed)),
            ]))
            .unwrap();
        assert!(
            output.is_empty(),
            "later item bypassed an earlier open container: {output}"
        );
        if !empty {
            for index in 0..2 {
                let at = |delta| PositionedDelta {
                    position: StreamPosition {
                        item: StreamItem::Ordered(0),
                        part: index,
                    },
                    delta,
                };
                let output = encoder
                    .push(&parts_chunk(vec![
                        at(PartDelta::Start(StreamPartKind::Text)),
                        at(PartDelta::Text(format!("earlier-{index}"))),
                        at(PartDelta::End),
                    ]))
                    .unwrap();
                assert!(output.contains(&format!("earlier-{index}")));
                assert!(!output.contains("later-call"));
            }
        }
        let output = encoder
            .push(&parts_chunk(vec![part(
                0,
                PartDelta::ResponsesItemEnd(S::Completed),
            )]))
            .unwrap();
        assert!(output.contains("later-call"));
    }
}

#[test]
fn response_container_blocking_is_bounded_and_unblocked_parts_do_not_accumulate() {
    use nyro_llm::ir::ResponsesItemStart as R;
    let container = |index| {
        part(
            index,
            PartDelta::ResponsesItemStart(R::Message {
                id: format!("message-{index}"),
            }),
        )
    };
    let mut encoder = StreamEncoder::with_limit("m".into(), 4096);
    encoder
        .push(&parts_chunk(vec![
            container(0),
            container(1),
            part(1, PartDelta::Start(StreamPartKind::Text)),
        ]))
        .unwrap();
    encoder
        .push(&parts_chunk(vec![part(
            1,
            PartDelta::Text("x".repeat(2000)),
        )]))
        .unwrap();
    assert!(
        encoder
            .push(&parts_chunk(vec![part(
                1,
                PartDelta::Text("x".repeat(2000))
            )]))
            .is_err()
    );

    let mut encoder = StreamEncoder::with_limit("m".into(), 1024);
    encoder.push(&parts_chunk(vec![container(0)])).unwrap();
    for index in 0..256 {
        let at = |delta| PositionedDelta {
            position: StreamPosition {
                item: StreamItem::Ordered(0),
                part: index,
            },
            delta,
        };
        let output = encoder
            .push(&parts_chunk(vec![
                at(PartDelta::Start(StreamPartKind::Text)),
                at(PartDelta::Text("short".into())),
                at(PartDelta::End),
            ]))
            .unwrap();
        assert!(output.contains("short"));
    }
}

#[test]
fn responses_container_identity_is_normalized_but_lifecycle_is_checked() {
    use nyro_llm::ir::ResponsesItemStart as R;
    use nyro_protocol::openai::responses::ItemStatus as S;
    let mut encoder = StreamEncoder::new("m".into());
    let output = encoder
        .push(&parts_chunk(vec![
            part(
                0,
                PartDelta::ResponsesItemStart(R::FunctionCall {
                    id: "source-item-id".into(),
                }),
            ),
            part(0, PartDelta::Start(StreamPartKind::ToolCall)),
            part(0, call(0, "call-id")),
            part(0, PartDelta::End),
            part(0, PartDelta::ResponsesItemEnd(S::Completed)),
        ]))
        .unwrap();
    assert!(output.contains("call-id"));
    assert!(!output.contains("source-item-id"));
    assert!(
        encoder
            .push(&parts_chunk(vec![part(
                0,
                PartDelta::ResponsesItemEnd(S::Completed)
            )]))
            .is_err()
    );
    for invalid in [
        PartDelta::Start(StreamPartKind::Text),
        PartDelta::ResponsesItemEnd(S::InProgress),
    ] {
        let mut encoder = StreamEncoder::new("m".into());
        encoder
            .push(&parts_chunk(vec![part(
                0,
                PartDelta::ResponsesItemStart(R::FunctionCall {
                    id: "source".into(),
                }),
            )]))
            .unwrap();
        assert!(encoder.push(&parts_chunk(vec![part(0, invalid)])).is_err());
    }
    let mut encoder = StreamEncoder::new("m".into());
    encoder
        .push(&parts_chunk(vec![
            part(
                0,
                PartDelta::ResponsesItemStart(R::Message { id: "msg".into() }),
            ),
            part(0, PartDelta::Start(StreamPartKind::Text)),
        ]))
        .unwrap();
    assert!(
        encoder
            .push(&parts_chunk(vec![part(
                0,
                PartDelta::ResponsesItemEnd(S::Completed)
            )]))
            .is_err()
    );
}

#[test]
fn tools_cannot_reuse_an_earlier_content_item_or_another_part_of_it() {
    for content in [
        vec![text_part(0, 0, "a".into()), text_part(1, 0, "b".into())],
        vec![text_part(0, 1, "a".into())],
    ] {
        let mut encoder = StreamEncoder::new("m".into());
        encoder.push(&parts_chunk(content)).unwrap();
        let tool = PositionedDelta {
            position: StreamPosition {
                item: StreamItem::Ordered(0),
                part: 0,
            },
            delta: PartDelta::ToolCall(nyro_llm::ir::ToolCallDelta {
                index: 0,
                id: Some("t".into()),
                r#type: Some(nyro_llm::ir::FunctionType::Function),
                function: Some(nyro_llm::ir::FunctionDelta {
                    name: Some("f".into()),
                    arguments: Some("{}".into()),
                }),
                gemini: None,
            }),
        };
        assert!(encoder.push(&parts_chunk(vec![tool])).is_err());
    }
}

#[test]
fn text_batch_is_bounded_without_accumulating_text_across_chunks() {
    let mut encoder = StreamEncoder::with_limit("m".into(), 1024);
    let single = parts_chunk(vec![text_part(0, 0, "x".repeat(1024))]);
    for _ in 0..3 {
        encoder.push(&single).unwrap();
    }
    let mut encoder = StreamEncoder::with_limit("m".into(), 1024);
    assert!(
        encoder
            .push(&parts_chunk(vec![text_part(0, 0, "x".repeat(600)); 3]))
            .is_err()
    );
    let mut encoder = StreamEncoder::with_limit("m".into(), 1024);
    assert!(
        encoder
            .push(&parts_chunk(vec![text_part(0, 0, String::new()); 1024]))
            .is_err()
    );
}

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
                events: vec![PositionedDelta {
                    position: StreamPosition {
                        item: StreamItem::OpenAiMessage,
                        part: 0,
                    },
                    delta: PartDelta::Text("hello".into()),
                }],
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
    assert!(decode_chat(json!({"model":"m","max_tokens":4,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"file-1"}}]}]})).is_err());
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
        let chunk = openai::decode_chat_event(&json!({"id":"m","object":"chat.completion.chunk","created":0,"model":"c","choices":[{"index":0,"delta":{"tool_calls":tools},"finish_reason":finish}],"usage":if finish.is_some(){serde_json::to_value(&response.usage).unwrap()}else{Value::Null}}).to_string()).unwrap();
        sse.push_str(&encoder.push(&chunk).unwrap());
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
fn interleaved_stream_preserves_block_lifecycles_and_emits_tools_at_end() {
    let frames = [
        json!({"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"c","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":0}}}),
        json!({"type":"content_block_start","index":0,"content_block":{"type":"text","text":"before"}}),
        json!({"type":"content_block_stop","index":0}),
        json!({"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"t","name":"f","input":{}}}),
        json!({"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"x\":"}}),
        json!({"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"1}"}}),
        json!({"type":"content_block_stop","index":1}),
        json!({"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}),
        json!({"type":"content_block_stop","index":2}),
        json!({"type":"content_block_start","index":3,"content_block":{"type":"thinking","thinking":"","signature":""}}),
        json!({"type":"content_block_delta","index":3,"delta":{"type":"thinking_delta","thinking":"reason"}}),
        json!({"type":"content_block_delta","index":3,"delta":{"type":"signature_delta","signature":"opaque"}}),
        json!({"type":"content_block_stop","index":3}),
        json!({"type":"content_block_start","index":4,"content_block":{"type":"tool_use","id":"u","name":"g","input":{}}}),
        json!({"type":"content_block_stop","index":4}),
        json!({"type":"content_block_start","index":5,"content_block":{"type":"redacted_thinking","data":"opaque-redacted"}}),
        json!({"type":"content_block_stop","index":5}),
        json!({"type":"content_block_start","index":6,"content_block":{"type":"text","text":"after"}}),
        json!({"type":"content_block_stop","index":6}),
        json!({"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}),
        json!({"type":"message_stop"}),
    ];
    let mut decoder = StreamDecoder::new();
    let mut encoder = StreamEncoder::new("c".into());
    let mut output = String::new();
    let mut starts = 0;
    let mut ends = 0;
    for frame in frames {
        for ir in decoder.push(&event(frame.clone())).unwrap() {
            if let ChatEvent::Chunk(chunk) = &ir {
                for e in &chunk.choices[0].delta.events {
                    let delta = serde_json::to_value(&e.delta).unwrap();
                    starts += usize::from(delta["type"] == "start");
                    ends += usize::from(delta["type"] == "end");
                }
            }
            output.push_str(&encoder.push(&ir).unwrap());
        }
        if frame["type"] == "content_block_stop" && frame["index"] == 1 {
            assert!(
                output.contains("tool_use"),
                "tool must be emitted when its block ends"
            );
        }
    }
    decoder.finish().unwrap();
    assert_eq!((starts, ends), (5, 5));
    let output: Vec<Value> = output
        .lines()
        .filter_map(|line| line.strip_prefix("data: "))
        .map(|line| serde_json::from_str(line).unwrap())
        .collect();
    let blocks: Vec<_> = output
        .iter()
        .filter(|frame| frame["type"] == "content_block_start")
        .collect();
    assert_eq!(
        blocks
            .iter()
            .map(|frame| frame["content_block"]["type"].as_str().unwrap())
            .collect::<Vec<_>>(),
        [
            "text",
            "tool_use",
            "text",
            "thinking",
            "tool_use",
            "redacted_thinking",
            "text"
        ]
    );
    for (index, block) in blocks.iter().enumerate() {
        assert_eq!(block["index"], index);
    }
    assert_eq!(
        output
            .iter()
            .filter(|frame| frame["type"] == "content_block_stop")
            .count(),
        7
    );
    assert!(
        output
            .iter()
            .any(|frame| frame["delta"]["signature"] == "opaque")
    );
    assert!(
        output
            .iter()
            .any(|frame| frame["delta"]["partial_json"] == "{\"x\":1}")
    );
    assert!(
        output
            .iter()
            .any(|frame| frame["usage"]["output_tokens"] == 7)
    );
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

#[test]
fn tool_error_and_text_blocks_survive_strict_anthropic_round_trip() {
    let source = json!({"model":"m","max_tokens":32,"messages":[
        {"role":"assistant","content":[{"type":"tool_use","id":"t","name":"f","input":{}}]},
        {"role":"user","content":[{"type":"tool_result","tool_use_id":"t","is_error":true,"content":[{"type":"text","text":"First"},{"type":"text","text":"Second"}]}]}]});
    let request = decode_chat(source.clone()).unwrap();
    let encoded = encode_chat(&request).unwrap();
    assert_eq!(encoded, source);
    assert!(nyro_llm::codec::openai::encode_chat(&request).is_err());
    assert!(nyro_llm::codec::openai::responses::encode_chat(&request).is_err());
    assert!(nyro_llm::codec::gemini::encode_chat(&request).is_err());
}

#[test]
fn empty_tool_results_are_supported_without_inventing_text() {
    for content in [None, Some(json!([])), Some(json!(""))] {
        let mut result = json!({"type":"tool_result","tool_use_id":"t"});
        if let Some(content) = content {
            result["content"] = content;
        }
        let input = json!({"model":"m","max_tokens":32,"messages":[
            {"role":"assistant","content":[{"type":"tool_use","id":"t","name":"f","input":{}}]},
            {"role":"user","content":[result]}]});
        let request = decode_chat(input).unwrap();
        let encoded = encode_chat(&request).unwrap();
        let content = &encoded["messages"][1]["content"][0]["content"];
        assert!(content == &json!([]) || content == &json!([{"type":"text","text":""}]));
        assert!(nyro_llm::codec::openai::encode_chat(&request).is_ok());
    }
}

#[test]
fn successful_tool_result_blocks_keep_boundaries_and_invalid_images_are_rejected() {
    let mut input = json!({"model":"m","max_tokens":32,"messages":[
        {"role":"assistant","content":[{"type":"tool_use","id":"t","name":"f","input":{}}]},
        {"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":"one"},{"type":"text","text":"two"}]}]}]});
    let request = decode_chat(input.clone()).unwrap();
    assert_eq!(encode_chat(&request).unwrap(), input);
    let openai = nyro_llm::codec::openai::encode_chat(&request).unwrap();
    assert_eq!(
        openai["messages"][1]["content"],
        json!([{"type":"text","text":"one"},{"type":"text","text":"two"}])
    );
    input["messages"][1]["content"][0]["content"] = json!([{"type":"image","source":{"type":"base64","media_type":"image/png","data":"opaque"}}]);
    assert!(decode_chat(input).is_err());
}
