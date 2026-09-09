//! Native OpenAI Chat response envelopes.
use super::{Failure, Frame};
use crate::Usage;
use nyro_protocol::framing::Event;
use serde_json::{Value, json};

// Validate the accounting/response envelope without filtering vendor fields.
pub(super) fn response(
    mut value: Value,
    model: &str,
    streaming: bool,
) -> Result<(Value, Option<Usage>), Failure> {
    let object = if streaming {
        "chat.completion.chunk"
    } else {
        "chat.completion"
    };
    if value.get("error").is_some()
        || value["object"] != object
        || !value["id"].is_string()
        || !value["model"].is_string()
        || value["created"].as_u64().is_none()
    {
        return Err(Failure::upstream());
    }
    let choices = value["choices"].as_array().ok_or_else(Failure::upstream)?;
    if !streaming && choices.is_empty() {
        return Err(Failure::upstream());
    }
    for choice in choices {
        let message = &choice[if streaming { "delta" } else { "message" }];
        if (!streaming || !message["role"].is_null()) && message["role"] != "assistant" {
            return Err(Failure::upstream());
        }
        if !(message["content"].is_null()
            || message["content"].is_string()
            || message["content"].is_array())
            || !(message["refusal"].is_null() || message["refusal"].is_string())
            || !(message["tool_calls"].is_null() || message["tool_calls"].is_array())
            || !(message["audio"].is_null() || message["audio"].is_object())
        {
            return Err(Failure::upstream());
        }
        if choice["index"]
            .as_u64()
            .is_none_or(|n| u32::try_from(n).is_err())
            || !message.is_object()
            || !(choice["finish_reason"].is_null() || choice["finish_reason"].is_string())
        {
            return Err(Failure::upstream());
        }
    }
    let usage = if value["usage"].is_null() {
        None
    } else {
        let usage = &value["usage"];
        Some(Usage {
            prompt_tokens: usage["prompt_tokens"]
                .as_u64()
                .ok_or_else(Failure::upstream)?,
            completion_tokens: usage["completion_tokens"]
                .as_u64()
                .ok_or_else(Failure::upstream)?,
            total_tokens: usage["total_tokens"]
                .as_u64()
                .ok_or_else(Failure::upstream)?,
            prompt_tokens_details: None,
            completion_tokens_details: None,
        })
    };
    value["model"] = json!(model);
    Ok((value, usage))
}

pub(super) fn frame(event: Event, model: &str, include_usage: bool) -> Result<Frame, Failure> {
    if event
        .event
        .as_deref()
        .is_some_and(|name| !name.is_empty() && name != "message")
    {
        return Err(Failure::upstream());
    }
    if event.data.trim() == "[DONE]" {
        return Ok(Frame {
            output: "data: [DONE]\n\n".into(),
            usage: None,
            done: true,
        });
    }
    let value = serde_json::from_str(&event.data).map_err(|_| Failure::upstream())?;
    let (mut value, usage) = response(value, model, true)?;
    let hidden = !include_usage && value["choices"].as_array().unwrap().is_empty();
    if !include_usage {
        value.as_object_mut().unwrap().remove("usage");
    }
    Ok(Frame {
        output: if hidden {
            String::new()
        } else {
            format!("data: {value}\n\n")
        },
        usage,
        done: false,
    })
}
