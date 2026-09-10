//! Stateless native Responses. Preserve opaque items, but own routing, accounting
//! and the response lifecycle. No item reconstruction or hosted session state.
use super::{Failure, Frame};
use crate::Usage;
use nyro_protocol::framing::Event;
use serde_json::{Value, json};

fn nonempty(value: &Value) -> bool {
    value.as_str().is_some_and(|s| !s.trim().is_empty())
}
fn optional<'a>(value: &'a Value, field: &str) -> Option<&'a Value> {
    value.get(field).filter(|v| !v.is_null())
}
fn input_item(item: &Value) -> bool {
    if !item.is_object() {
        return false;
    }
    match item["type"].as_str().unwrap_or("message") {
        "message" => {
            matches!(
                item["role"].as_str(),
                Some("user" | "assistant" | "system" | "developer")
            ) && (item["content"].is_string()
                || item["content"].as_array().is_some_and(|parts| {
                    !parts.is_empty() && parts.iter().all(|p| p.is_object() && nonempty(&p["type"]))
                }))
        }
        "reasoning" => {
            nonempty(&item["id"])
                && optional(item, "summary").is_none_or(Value::is_array)
                && optional(item, "encrypted_content").is_none_or(Value::is_string)
        }
        "function_call" => {
            nonempty(&item["call_id"]) && nonempty(&item["name"]) && item["arguments"].is_string()
        }
        "function_call_output" => {
            nonempty(&item["call_id"])
                && (item["output"].is_string()
                    || item["output"]
                        .as_array()
                        .is_some_and(|p| p.iter().all(Value::is_object)))
        }
        _ => false,
    }
}
pub(super) fn validate_request(value: &Value, streaming: bool) -> Result<(), Failure> {
    let valid = nonempty(&value["model"])
        && ["store", "background"]
            .iter()
            .all(|k| optional(value, k).is_none_or(|v| v == false))
        && ["previous_response_id", "conversation"]
            .iter()
            .all(|k| optional(value, k).is_none())
        && (value["input"].is_string()
            || value["input"].as_array().is_some_and(|items| {
                (!items.is_empty() || value["instructions"].is_string())
                    && items.iter().all(input_item)
            }))
        && optional(value, "instructions").is_none_or(Value::is_string)
        && optional(value, "max_output_tokens").is_none_or(|n| n.as_u64().is_some_and(|n| n > 0))
        && optional(value, "include").is_none_or(|v| {
            v.as_array()
                .is_some_and(|items| items.iter().all(Value::is_string))
        })
        && optional(value, "tools").is_none_or(|v| {
            v.as_array().is_some_and(|items| {
                items
                    .iter()
                    .all(|t| t["type"] == "function" && nonempty(&t["name"]))
            })
        })
        && optional(value, "tool_choice").is_none_or(|v| {
            matches!(v.as_str(), Some("auto" | "none" | "required"))
                || (v["type"] == "function" && nonempty(&v["name"]))
        })
        && optional(value, "stream_options").is_none_or(|v| {
            streaming
                && v.is_object()
                && optional(v, "include_obfuscation").is_none_or(Value::is_boolean)
        });
    if valid {
        Ok(())
    } else {
        Err(Failure::invalid(
            "Invalid or unsupported native Responses request envelope",
        ))
    }
}

fn usage(value: &Value) -> Result<Option<Usage>, Failure> {
    let Some(value) = optional(value, "usage") else {
        return Ok(None);
    };
    let input = value["input_tokens"]
        .as_u64()
        .ok_or_else(Failure::upstream)?;
    let output = value["output_tokens"]
        .as_u64()
        .ok_or_else(Failure::upstream)?;
    let total = value["total_tokens"]
        .as_u64()
        .ok_or_else(Failure::upstream)?;
    if input.checked_add(output) != Some(total) {
        return Err(Failure::upstream());
    }
    for (details, field, bound) in [
        ("input_tokens_details", "cached_tokens", input),
        ("output_tokens_details", "reasoning_tokens", output),
    ] {
        if let Some(details) = optional(value, details)
            && (!details.is_object()
                || details
                    .get(field)
                    .is_some_and(|v| v.as_u64().is_none_or(|n| n > bound)))
        {
            return Err(Failure::upstream());
        }
    }
    Ok(Some(Usage {
        prompt_tokens: input,
        completion_tokens: output,
        total_tokens: total,
        cache_creation: None,
        prompt_tokens_details: None,
        completion_tokens_details: None,
    }))
}
fn envelope(value: &Value, terminal: bool) -> Result<Option<Usage>, Failure> {
    if value["object"] != "response"
        || !nonempty(&value["id"])
        || !nonempty(&value["model"])
        || value["created_at"].as_u64().is_none()
        || !value["error"].is_null()
        || !value["output"]
            .as_array()
            .is_some_and(|items| items.iter().all(|v| v.is_object() && nonempty(&v["type"])))
    {
        return Err(Failure::upstream());
    }
    match value["status"].as_str() {
        Some("completed") if terminal && value["incomplete_details"].is_null() => {}
        Some("incomplete") if terminal && nonempty(&value["incomplete_details"]["reason"]) => {}
        Some("in_progress") if !terminal && value["incomplete_details"].is_null() => {}
        _ => return Err(Failure::upstream()),
    }
    usage(value)
}
pub(super) fn response(mut value: Value, model: &str) -> Result<(Value, Option<Usage>), Failure> {
    let usage = envelope(&value, true)?;
    value["model"] = json!(model);
    Ok((value, usage))
}
#[derive(Default)]
pub(in crate::runtime) struct StreamDecoder {
    sequence: u64,
    identity: Option<(String, String, u64)>,
    terminal: bool,
}
impl StreamDecoder {
    pub(super) fn push(&mut self, event: Event, model: &str) -> Result<Frame, Failure> {
        let mut value: Value =
            serde_json::from_str(&event.data).map_err(|_| Failure::upstream())?;
        let kind = value["type"]
            .as_str()
            .filter(|s| s.starts_with("response."))
            .ok_or_else(Failure::upstream)?
            .to_owned();
        if self.terminal
            || value["sequence_number"].as_u64() != Some(self.sequence)
            || event.event.as_deref().is_some_and(|name| name != kind)
            || !value["error"].is_null()
            || matches!(
                kind.as_str(),
                "response.failed" | "response.cancelled" | "response.queued"
            )
        {
            return Err(Failure::upstream());
        }
        self.sequence = self.sequence.checked_add(1).ok_or_else(Failure::upstream)?;
        let terminal = matches!(kind.as_str(), "response.completed" | "response.incomplete");
        let mut observed = None;
        match kind.as_str() {
            "response.created"
            | "response.in_progress"
            | "response.completed"
            | "response.incomplete" => {
                let response = &mut value["response"];
                let usage = envelope(response, terminal)?;
                if terminal && kind != format!("response.{}", response["status"].as_str().unwrap())
                {
                    return Err(Failure::upstream());
                }
                let identity = (
                    response["id"].as_str().unwrap().to_owned(),
                    response["model"].as_str().unwrap().to_owned(),
                    response["created_at"].as_u64().unwrap(),
                );
                if kind == "response.created" {
                    if self.identity.is_some() || !response["output"].as_array().unwrap().is_empty()
                    {
                        return Err(Failure::upstream());
                    }
                    self.identity = Some(identity);
                } else if self.identity.as_ref() != Some(&identity) {
                    return Err(Failure::upstream());
                }
                response["model"] = json!(model);
                if terminal {
                    observed = usage;
                }
            }
            _ => {
                if self.identity.is_none()
                    || value.get("response").is_some()
                    || value.get("usage").is_some()
                {
                    return Err(Failure::upstream());
                }
                for field in ["output_index", "content_index", "summary_index"] {
                    if value.get(field).is_some_and(|v| v.as_u64().is_none()) {
                        return Err(Failure::upstream());
                    }
                }
            }
        }
        self.terminal = terminal;
        let prefix = event
            .event
            .map(|name| format!("event: {name}\n"))
            .unwrap_or_default();
        Ok(Frame {
            output: format!("{prefix}data: {value}\n\n"),
            usage: observed,
            done: false,
        })
    }
    pub(super) fn finish(&self) -> Result<(), Failure> {
        if self.terminal {
            Ok(())
        } else {
            Err(Failure::upstream())
        }
    }
}
