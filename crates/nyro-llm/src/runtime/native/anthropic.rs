//! Anthropic native envelopes and bounded stream lifecycle; content stays opaque.
use super::{Failure, Frame};
use crate::Usage;
use nyro_protocol::framing::Event;
use serde_json::{Value, json};

pub(super) fn validate_request(value: &Value) -> Result<(), Failure> {
    if value["max_tokens"].as_u64().is_none_or(|n| n == 0)
        || value["messages"].as_array().is_none_or(|messages| {
            messages
                .iter()
                .any(|message| !(message["content"].is_string() || message["content"].is_array()))
        })
    {
        return Err(Failure::invalid(
            "Invalid native Anthropic request envelope",
        ));
    }
    Ok(())
}
fn nonempty(value: &Value) -> bool {
    value.as_str().is_some_and(|s| !s.trim().is_empty())
}
fn optional_string(value: &Value) -> bool {
    value.is_null() || value.is_string()
}
fn block(value: &Value) -> Result<&str, Failure> {
    let kind = value["type"]
        .as_str()
        .filter(|s| !s.is_empty())
        .ok_or_else(Failure::upstream)?;
    let valid = match kind {
        "text" => value["text"].is_string(),
        "thinking" => value["thinking"].is_string() && optional_string(&value["signature"]),
        "redacted_thinking" => value["data"].is_string(),
        "tool_use" | "server_tool_use" => {
            nonempty(&value["id"]) && nonempty(&value["name"]) && value["input"].is_object()
        }
        _ => true,
    };
    if !valid {
        return Err(Failure::upstream());
    }
    Ok(kind)
}
fn message(value: &Value, streaming: bool) -> Result<(), Failure> {
    if value["type"] != "message"
        || value["role"] != "assistant"
        || !nonempty(&value["id"])
        || !nonempty(&value["model"])
        || value.get("error").is_some()
        || !optional_string(&value["stop_sequence"])
    {
        return Err(Failure::upstream());
    }
    let content = value["content"].as_array().ok_or_else(Failure::upstream)?;
    if streaming {
        if !content.is_empty()
            || !value["stop_reason"].is_null()
            || !value["stop_sequence"].is_null()
        {
            return Err(Failure::upstream());
        }
    } else {
        if !nonempty(&value["stop_reason"]) {
            return Err(Failure::upstream());
        }
        for value in content {
            block(value)?;
        }
    }
    Ok(())
}

// Input categories are disjoint in Anthropic usage. Nested cache-duration
// breakdowns are subsets, not additional input tokens. Retain only counters.
#[derive(Clone, Copy, Default)]
struct Counters {
    input: u64,
    output: u64,
    created: u64,
    read: u64,
}
impl Counters {
    fn update(&mut self, value: &Value, initial: bool) -> Result<Usage, Failure> {
        if !value.is_object() {
            return Err(Failure::upstream());
        }
        let mut next = *self;
        for (name, counter) in [
            ("input_tokens", &mut next.input),
            ("output_tokens", &mut next.output),
            ("cache_creation_input_tokens", &mut next.created),
            ("cache_read_input_tokens", &mut next.read),
        ] {
            match value.get(name) {
                Some(value) => {
                    let count = value.as_u64().ok_or_else(Failure::upstream)?;
                    if count < *counter {
                        return Err(Failure::upstream());
                    }
                    *counter = count;
                }
                None if initial && matches!(name, "input_tokens" | "output_tokens") => {
                    return Err(Failure::upstream());
                }
                None => {}
            }
        }
        let input = next
            .input
            .checked_add(next.created)
            .and_then(|n| n.checked_add(next.read))
            .ok_or_else(Failure::upstream)?;
        let total = input
            .checked_add(next.output)
            .ok_or_else(Failure::upstream)?;
        *self = next;
        Ok(Usage {
            prompt_tokens: input,
            completion_tokens: next.output,
            total_tokens: total,
            prompt_tokens_details: None,
            completion_tokens_details: None,
        })
    }
}
pub(super) fn response(mut value: Value, model: &str) -> Result<(Value, Usage), Failure> {
    message(&value, false)?;
    let usage = Counters::default().update(&value["usage"], true)?;
    value["model"] = json!(model);
    Ok((value, usage))
}

#[derive(Default)]
pub(in crate::runtime) struct StreamDecoder {
    started: bool,
    finalizing: bool,
    terminal: bool,
    next: u32,
    active: Option<(u32, String)>,
    usage: Counters,
}
impl StreamDecoder {
    pub(super) fn push(&mut self, event: Event, model: &str) -> Result<Frame, Failure> {
        let mut value: Value =
            serde_json::from_str(&event.data).map_err(|_| Failure::upstream())?;
        let kind = event
            .event
            .as_deref()
            .filter(|name| !name.is_empty())
            .ok_or_else(Failure::upstream)?;
        if value["type"] != kind || value.get("error").is_some() {
            return Err(Failure::upstream());
        }
        let mut usage = None;
        let mut done = false;
        match kind {
            "ping" => {}
            "message_start" => {
                if self.started {
                    return Err(Failure::upstream());
                }
                message(&value["message"], true)?;
                usage = Some(self.usage.update(&value["message"]["usage"], true)?);
                value["message"]["model"] = json!(model);
                self.started = true;
            }
            "content_block_start" => {
                if !self.started || self.finalizing || self.active.is_some() {
                    return Err(Failure::upstream());
                }
                let index = index(&value)?;
                if index != self.next {
                    return Err(Failure::upstream());
                }
                let kind = block(&value["content_block"])?;
                self.next = self.next.checked_add(1).ok_or_else(Failure::upstream)?;
                self.active = Some((index, kind.to_owned()));
            }
            "content_block_delta" => {
                let index = index(&value)?;
                let (active, kind) = self.active.as_ref().ok_or_else(Failure::upstream)?;
                if *active != index {
                    return Err(Failure::upstream());
                }
                let delta = &value["delta"];
                let delta_kind = delta["type"]
                    .as_str()
                    .filter(|s| !s.is_empty())
                    .ok_or_else(Failure::upstream)?;
                let valid = match delta_kind {
                    "text_delta" => kind == "text" && delta["text"].is_string(),
                    "thinking_delta" => kind == "thinking" && delta["thinking"].is_string(),
                    "signature_delta" => kind == "thinking" && delta["signature"].is_string(),
                    "input_json_delta" => {
                        matches!(kind.as_str(), "tool_use" | "server_tool_use")
                            && delta["partial_json"].is_string()
                    }
                    "citations_delta" => kind == "text" && delta["citation"].is_object(),
                    _ => true,
                };
                if !valid {
                    return Err(Failure::upstream());
                }
            }
            "content_block_stop" => {
                if self.active.as_ref().map(|(i, _)| *i) != Some(index(&value)?) {
                    return Err(Failure::upstream());
                }
                self.active = None;
            }
            "message_delta" => {
                let delta = &value["delta"];
                if !self.started
                    || self.active.is_some()
                    || !delta.is_object()
                    || !optional_string(&delta["stop_reason"])
                    || !optional_string(&delta["stop_sequence"])
                    || value["usage"]["output_tokens"].as_u64().is_none()
                {
                    return Err(Failure::upstream());
                }
                usage = Some(self.usage.update(&value["usage"], false)?);
                self.finalizing = true;
                if let Some(reason) = delta.get("stop_reason") {
                    self.terminal = nonempty(reason);
                }
            }
            "message_stop" => {
                if !self.terminal || self.active.is_some() {
                    return Err(Failure::upstream());
                }
                done = true;
            }
            _ => return Err(Failure::upstream()),
        }
        Ok(Frame {
            output: format!("event: {kind}\ndata: {value}\n\n"),
            usage,
            done,
        })
    }
}
fn index(value: &Value) -> Result<u32, Failure> {
    value["index"]
        .as_u64()
        .and_then(|n| u32::try_from(n).ok())
        .ok_or_else(Failure::upstream)
}
