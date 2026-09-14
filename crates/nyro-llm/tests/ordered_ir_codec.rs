use nyro_llm::{
    codec::{anthropic, gemini, openai},
    ir::{ChatEvent, Content, MessageItem, PartDelta, PositionedDelta, StreamItem, StreamPosition},
};
use serde_json::{Value, json};

#[test]
fn ordered_messages_keep_nested_content_and_reject_unrepresentable_projection() {
    let wire = json!({"model":"m","messages":[{"role":"assistant","content":[{"type":"text","text":"first"},{"type":"text","text":"second"}],"tool_calls":[{"type":"function","id":"a","function":{"name":"lookup","arguments":"{}"}},{"type":"function","id":"b","function":{"name":"lookup","arguments":"{}"}}]}]});
    let mut request = openai::decode_chat(wire.clone()).unwrap();
    assert_eq!(openai::encode_chat(&request).unwrap(), wire);
    assert!(matches!(
        request.messages[0].items[0],
        MessageItem::Content(Content::Parts(_))
    ));
    let serialized = serde_json::to_value(&request.messages[0]).unwrap();
    assert!(serialized.get("content").is_none());
    assert!(serialized.get("tool_calls").is_none());
    assert_eq!(serialized["items"][2]["value"]["id"], "b");

    request.messages[0]
        .items
        .push(MessageItem::Content(Content::Text("after tools".into())));
    for result in [
        openai::encode_chat(&request),
        openai::responses::encode_chat(&request),
        anthropic::encode_chat(&request),
        gemini::encode_chat(&request),
    ] {
        assert!(
            result.is_err(),
            "unsupported content must not be omitted or moved"
        );
    }
}

fn chunk(delta: Value) -> Value {
    json!({"id":"c","object":"chat.completion.chunk","created":0,"model":"m","choices":[{"index":0,"delta":delta,"finish_reason":null}]})
}

#[test]
fn chat_rejects_tool_call_fields_on_non_assistant_even_when_empty() {
    for role in ["user", "system", "developer", "tool"] {
        let mut message = json!({"role":role,"content":"text","tool_calls":[]});
        if role == "tool" {
            message["tool_call_id"] = json!("a");
        }
        assert!(
            openai::decode_chat(json!({"model":"m","messages":[message]})).is_err(),
            "role {role}"
        );
    }
}

#[test]
fn chat_field_slots_preserve_parallel_tool_indices_and_wire_round_trip() {
    let first = chunk(
        json!({"role":"assistant","content":"","tool_calls":[{"index":1,"id":"b","type":"function","function":{"name":"lookup","arguments":"{"}},{"index":0,"id":"a","type":"function","function":{"name":"lookup","arguments":"{}"}}]}),
    );
    let event = openai::decode_chat_event(&first.to_string()).unwrap();
    let ChatEvent::Chunk(decoded) = &event else {
        panic!("expected chunk")
    };
    let events = &decoded.choices[0].delta.events;
    assert_eq!(
        events[0].position,
        StreamPosition {
            item: StreamItem::OpenAiMessage,
            part: 0
        }
    );
    assert_eq!(events[1].position.item, StreamItem::OpenAiTool(1));
    assert_eq!(events[2].position.item, StreamItem::OpenAiTool(0));
    let output = openai::encode_chat_event(&event, "m").unwrap();
    assert_eq!(
        serde_json::from_str::<Value>(output.strip_prefix("data: ").unwrap()).unwrap(),
        first
    );

    let later = chunk(json!({"tool_calls":[{"index":1,"function":{"arguments":"}"}}]}));
    let ChatEvent::Chunk(decoded) = openai::decode_chat_event(&later.to_string()).unwrap() else {
        panic!("expected chunk")
    };
    assert_eq!(
        decoded.choices[0].delta.events[0].position.item,
        StreamItem::OpenAiTool(1)
    );
}

#[test]
fn chat_encoder_rejects_mismatched_slots_and_does_not_reorder_ordered_events() {
    let original = chunk(json!({"tool_calls":[{"index":1,"function":{"arguments":"{}"}}]}));
    let ChatEvent::Chunk(mut decoded) = openai::decode_chat_event(&original.to_string()).unwrap()
    else {
        panic!("expected chunk")
    };
    decoded.choices[0].delta.events[0].position.item = StreamItem::OpenAiTool(0);
    assert!(openai::encode_chat_event(&ChatEvent::Chunk(decoded.clone()), "m").is_err());

    decoded.choices[0].delta.events[0].position.item = StreamItem::Ordered(0);
    decoded.choices[0].delta.events.push(PositionedDelta {
        position: StreamPosition {
            item: StreamItem::Ordered(1),
            part: 0,
        },
        delta: PartDelta::Text("after call".into()),
    });
    assert!(openai::encode_chat_event(&ChatEvent::Chunk(decoded), "m").is_err());
}
