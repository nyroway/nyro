//! Explicit boundary between ordered IR and Chat Completions field slots.
use super::*;

pub(super) fn decode_message(mut value: Value) -> Result<Value, CodecError> {
    let object = value
        .as_object_mut()
        .ok_or_else(|| invalid("expected message"))?;
    let content = object
        .remove("content")
        .map(serde_json::from_value)
        .transpose()?
        .flatten();
    let calls = object
        .remove("tool_calls")
        .map(serde_json::from_value)
        .transpose()?
        .flatten();
    object.insert(
        "items".into(),
        serde_json::to_value(MessageItem::from_parts(content, calls))?,
    );
    Ok(value)
}

pub(super) fn encode_message(mut value: Value) -> Result<Value, CodecError> {
    let object = value
        .as_object_mut()
        .ok_or_else(|| invalid("expected message"))?;
    let items: Vec<MessageItem> = object
        .remove("items")
        .map(serde_json::from_value)
        .transpose()?
        .unwrap_or_default();
    super::super::validate_message_items(&items)?;
    let mut calls = Vec::new();
    for item in items {
        match item {
            MessageItem::Content(content) | MessageItem::ResponsesMessage { content, .. } => {
                let content = match content {
                    Content::Parts(parts) => {
                        let mut body = Vec::new();
                        let mut refusal: Option<String> = None;
                        for part in parts {
                            match part {
                                ContentPart::Refusal { refusal: text } => {
                                    refusal.get_or_insert_default().push_str(&text);
                                }
                                _ if refusal.is_some() => {
                                    return Err(invalid(
                                        "Chat cannot preserve content after refusal",
                                    ));
                                }
                                part => body.push(part),
                            }
                        }
                        if let Some(refusal) = refusal {
                            if object.get("refusal").is_some_and(|value| !value.is_null()) {
                                return Err(invalid("ambiguous refusal representations"));
                            }
                            object.insert("refusal".into(), Value::String(refusal));
                            if body.is_empty() {
                                continue;
                            }
                        }
                        Content::Parts(body)
                    }
                    content => content,
                };
                object.insert("content".into(), serde_json::to_value(content)?);
            }
            MessageItem::ToolCall(call) | MessageItem::ResponsesToolCall { call, .. } => {
                calls.push(call)
            }
        }
    }
    if !calls.is_empty() {
        object.insert("tool_calls".into(), serde_json::to_value(calls)?);
    }
    Ok(value)
}

pub(super) fn decode_delta(value: Value) -> Result<Delta, CodecError> {
    let wire: stream::Delta = serde_json::from_value(value)?;
    let mut events = Vec::new();
    if let Some(text) = wire.content {
        events.push(PositionedDelta {
            position: StreamPosition {
                item: StreamItem::OpenAiMessage,
                part: 0,
            },
            delta: PartDelta::Text(text),
        });
    }
    if let Some(text) = wire.refusal {
        events.push(PositionedDelta {
            position: StreamPosition {
                item: StreamItem::OpenAiMessage,
                part: 1,
            },
            delta: PartDelta::Refusal(text),
        });
    }
    for call in wire.tool_calls.into_iter().flatten() {
        events.push(PositionedDelta {
            position: StreamPosition {
                item: StreamItem::OpenAiTool(call.index),
                part: 0,
            },
            delta: PartDelta::ToolCall(convert(call)?),
        });
    }
    Ok(Delta {
        role: convert(wire.role)?,
        events,
    })
}

pub(super) fn encode_delta(delta: &Delta) -> Result<Value, CodecError> {
    let mut wire = stream::Delta {
        role: convert(&delta.role)?,
        ..Default::default()
    };
    let mut saw_tool = false;
    let mut saw_refusal = false;
    for event in &delta.events {
        super::super::validate_position(event)?;
        match &event.delta {
            PartDelta::Start(_)
            | PartDelta::End
            | PartDelta::ResponsesItemStart(_)
            | PartDelta::ResponsesItemEnd(_) => {}
            PartDelta::Text(text) => {
                if matches!(event.position.item, StreamItem::Ordered(_))
                    && (saw_tool || saw_refusal)
                {
                    return Err(invalid("Chat cannot preserve interleaved delta fields"));
                }
                wire.content.get_or_insert_default().push_str(text);
            }
            PartDelta::Refusal(text) => {
                if matches!(event.position.item, StreamItem::Ordered(_)) && saw_tool {
                    return Err(invalid("Chat cannot preserve interleaved delta fields"));
                }
                wire.refusal.get_or_insert_default().push_str(text);
                saw_refusal = true;
            }
            PartDelta::ToolCall(call) => {
                wire.tool_calls.get_or_insert_default().push(convert(call)?);
                saw_tool = true;
            }
            _ => {
                return Err(invalid(
                    "Chat cannot represent protocol-specific stream content",
                ));
            }
        }
    }
    Ok(serde_json::to_value(wire)?)
}
