//! Opt-in OpenAI Chat envelopes. Vendor payloads stay private to the runtime;
//! crossing a protocol boundary still requires the original strict codec.
use super::Failure;
use crate::{Request, Usage, codec::openai};
use nyro_protocol::framing::Event;
use serde_json::{Value, json};

pub(super) enum Input {
    Typed(Request),
    Native(NativeRequest),
}
impl Input {
    pub(super) fn model(&self) -> &str {
        match self {
            Self::Typed(request) => request.model(),
            Self::Native(request) => &request.model,
        }
    }
    pub(super) fn is_streaming(&self) -> bool {
        match self {
            Self::Typed(request) => request.is_streaming(),
            Self::Native(request) => request.streaming,
        }
    }
    pub(super) fn include_usage(&self) -> bool {
        match self {
            Self::Typed(Request::Chat(chat)) => chat
                .openai
                .stream_options
                .as_ref()
                .is_some_and(|options| options.include_usage == Some(true)),
            Self::Typed(_) => false,
            Self::Native(request) => request.include_usage,
        }
    }
    pub(super) fn typed(&self) -> Option<Request> {
        match self {
            Self::Typed(request) => Some(request.clone()),
            Self::Native(request) => openai::decode_chat(request.value.clone())
                .ok()
                .map(Request::Chat),
        }
    }
}

pub(super) struct NativeRequest {
    value: Value,
    model: String,
    streaming: bool,
    include_usage: bool,
}
impl NativeRequest {
    pub(super) fn parse(value: Value) -> Result<Self, Failure> {
        let invalid = || Failure::invalid("Invalid native Chat request envelope");
        let model = value
            .get("model")
            .and_then(Value::as_str)
            .filter(|model| !model.is_empty())
            .ok_or_else(invalid)?
            .to_owned();
        let messages = value
            .get("messages")
            .and_then(Value::as_array)
            .filter(|messages| !messages.is_empty())
            .ok_or_else(invalid)?;
        if messages.iter().any(|message| {
            message
                .get("role")
                .and_then(Value::as_str)
                .is_none_or(str::is_empty)
        }) {
            return Err(invalid());
        }
        let streaming = optional_bool(&value["stream"]).ok_or_else(invalid)?;
        let options = &value["stream_options"];
        if !options.is_null() && !options.is_object() {
            return Err(invalid());
        }
        let include_usage = optional_bool(&options["include_usage"]).ok_or_else(invalid)?;
        optional_bool(&options["include_obfuscation"]).ok_or_else(invalid)?;
        Ok(Self {
            value,
            model,
            streaming,
            include_usage,
        })
    }
    pub(super) fn encode(&self, model: &str) -> Value {
        let mut value = self.value.clone();
        value["model"] = json!(model);
        if self.streaming {
            if value["stream_options"].is_null() {
                value["stream_options"] = json!({});
            }
            value["stream_options"]["include_usage"] = json!(true);
        }
        value
    }
}
fn optional_bool(value: &Value) -> Option<bool> {
    if value.is_null() {
        Some(false)
    } else {
        value.as_bool()
    }
}

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

pub(super) struct Frame {
    pub output: String,
    pub usage: Option<Usage>,
    pub done: bool,
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
