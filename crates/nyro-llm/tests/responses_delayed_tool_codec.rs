use nyro_llm::{
    codec::openai::{
        decode_chat_event,
        responses::{StreamDecoder, StreamEncoder},
    },
    ir::{
        ChatEvent, FunctionDelta, FunctionType, PartDelta, PositionedDelta, ResponsesItemStart,
        StreamItem, StreamPartKind, StreamPosition, ToolCallDelta,
    },
};
use serde_json::{Value, json};

fn chunk(item: u32, deltas: Vec<PartDelta>) -> ChatEvent {
    let ChatEvent::Chunk(mut chunk) = decode_chat_event(&json!({"id":"r","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":{}}]}).to_string()).unwrap() else { unreachable!() };
    chunk.choices[0].delta.events = deltas
        .into_iter()
        .map(|delta| PositionedDelta {
            position: StreamPosition {
                item: StreamItem::Ordered(item),
                part: 0,
            },
            delta,
        })
        .collect();
    ChatEvent::Chunk(chunk)
}

#[test]
fn delayed_function_identity_cannot_emit_later_output_item_first() {
    for container in [false, true] {
        let mut encoder = StreamEncoder::new("m".into());
        let mut start = Vec::new();
        if container {
            start.push(PartDelta::ResponsesItemStart(
                ResponsesItemStart::FunctionCall {
                    id: "function-item".into(),
                },
            ));
        }
        start.push(PartDelta::Start(StreamPartKind::ToolCall));
        let mut output = encoder.push(&chunk(0, start)).unwrap();
        output.push_str(
            &encoder
                .push(&chunk(
                    1,
                    vec![
                        PartDelta::Start(StreamPartKind::Text),
                        PartDelta::Text("later message".into()),
                        PartDelta::End,
                    ],
                ))
                .unwrap(),
        );
        output.push_str(
            &encoder
                .push(&chunk(
                    0,
                    vec![
                        PartDelta::ToolCall(ToolCallDelta {
                            index: 0,
                            id: Some("call-id".into()),
                            r#type: Some(FunctionType::Function),
                            gemini: None,
                            function: Some(FunctionDelta {
                                name: Some("f".into()),
                                arguments: Some("{}".into()),
                            }),
                        }),
                        PartDelta::End,
                    ],
                ))
                .unwrap(),
        );
        if container {
            output.push_str(
                &encoder
                    .push(&chunk(
                        0,
                        vec![PartDelta::ResponsesItemEnd(
                            nyro_protocol::openai::responses::ItemStatus::Completed,
                        )],
                    ))
                    .unwrap(),
            );
        }
        let ChatEvent::Chunk(mut terminal) = chunk(0, vec![]) else {
            unreachable!()
        };
        terminal.choices[0].finish_reason = Some("tool_calls".into());
        output.push_str(&encoder.push(&ChatEvent::Chunk(terminal)).unwrap());
        output.push_str(&encoder.push(&ChatEvent::Done).unwrap());
        let indices: Vec<_> = output
            .lines()
            .filter_map(|line| line.strip_prefix("data: "))
            .map(|line| serde_json::from_str::<Value>(line).unwrap())
            .filter(|frame| frame["type"] == "response.output_item.added")
            .map(|frame| frame["output_index"].as_u64().unwrap())
            .collect();
        assert_eq!(indices, [0, 1]);
        let frames = nyro_protocol::framing::Decoder::new(1024 * 1024)
            .push(output.as_bytes())
            .unwrap();
        let mut decoder = StreamDecoder::new();
        for frame in frames {
            decoder.push(&frame).unwrap();
        }
        decoder.finish().unwrap();
    }
}

#[test]
fn delayed_function_bounds_queued_later_frames() {
    let mut encoder = StreamEncoder::with_limit("m".into(), 4096);
    encoder
        .push(&chunk(0, vec![PartDelta::Start(StreamPartKind::ToolCall)]))
        .unwrap();
    encoder
        .push(&chunk(1, vec![PartDelta::Start(StreamPartKind::Text)]))
        .unwrap();
    let mut rejected = false;
    for _ in 0..64 {
        if encoder
            .push(&chunk(1, vec![PartDelta::Text("x".into())]))
            .is_err()
        {
            rejected = true;
            break;
        }
    }
    assert!(rejected);
    assert!(encoder.push(&ChatEvent::Done).is_err());
}

#[test]
fn parallel_delayed_function_identities_flush_in_item_order() {
    let mut encoder = StreamEncoder::new("m".into());
    let mut output = String::new();
    for index in 0..2 {
        output.push_str(
            &encoder
                .push(&chunk(
                    index,
                    vec![PartDelta::Start(StreamPartKind::ToolCall)],
                ))
                .unwrap(),
        );
    }
    output.push_str(
        &encoder
            .push(&chunk(
                2,
                vec![
                    PartDelta::Start(StreamPartKind::Text),
                    PartDelta::Text("later".into()),
                    PartDelta::End,
                ],
            ))
            .unwrap(),
    );
    for index in [1, 0] {
        output.push_str(
            &encoder
                .push(&chunk(
                    index,
                    vec![
                        PartDelta::ToolCall(ToolCallDelta {
                            index,
                            id: Some(format!("call{index}")),
                            r#type: Some(FunctionType::Function),
                            gemini: None,
                            function: Some(FunctionDelta {
                                name: Some("f".into()),
                                arguments: Some("{}".into()),
                            }),
                        }),
                        PartDelta::End,
                    ],
                ))
                .unwrap(),
        );
    }
    let ChatEvent::Chunk(mut terminal) = chunk(0, vec![]) else {
        unreachable!()
    };
    terminal.choices[0].finish_reason = Some("tool_calls".into());
    output.push_str(&encoder.push(&ChatEvent::Chunk(terminal)).unwrap());
    output.push_str(&encoder.push(&ChatEvent::Done).unwrap());
    let frames = nyro_protocol::framing::Decoder::new(1024 * 1024)
        .push(output.as_bytes())
        .unwrap();
    let mut decoder = StreamDecoder::new();
    for frame in frames {
        decoder.push(&frame).unwrap();
    }
    decoder.finish().unwrap();
}
