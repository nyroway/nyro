//! Native single-candidate Gemini envelopes. EOF, not finishReason alone,
//! completes a stream, so trailing usage and transport errors remain observable.
use super::{Failure, Frame};
use crate::Usage;
use nyro_protocol::framing::Event;
use serde_json::Value;

// ProtoJSON null on an optional field means unset; preserve it on the wire.
fn optional<'a>(value: &'a Value, name: &str) -> Option<&'a Value> {
    value.get(name).filter(|value| !value.is_null())
}
fn nonempty(value: &Value) -> bool {
    value.as_str().is_some_and(|s| !s.trim().is_empty())
}
fn content(value: &Value, request: bool) -> bool {
    if let Some(role) = optional(value, "role")
        && (!nonempty(role) || (!request && role != "model"))
    {
        return false;
    }
    value["parts"].as_array().is_some_and(|parts| {
        (!request || !parts.is_empty())
            && parts.iter().all(|part| {
                part.as_object().is_some_and(|p| !p.is_empty())
                    && optional(part, "text").is_none_or(Value::is_string)
                    && optional(part, "thought").is_none_or(Value::is_boolean)
                    && optional(part, "thoughtSignature").is_none_or(Value::is_string)
                    && ["functionCall", "functionResponse", "inlineData", "fileData"]
                        .iter()
                        .all(|name| optional(part, name).is_none_or(Value::is_object))
            })
    })
}
pub(super) fn validate_request(value: &Value) -> Result<(), Failure> {
    let valid = value.is_object()
        && value.get("model").is_none()
        && value.get("stream").is_none()
        && value["contents"].as_array().is_some_and(|contents| {
            !contents.is_empty() && contents.iter().all(|c| content(c, true))
        })
        && optional(value, "systemInstruction").is_none_or(|c| content(c, true))
        && optional(value, "generationConfig").is_none_or(|config| {
            config.is_object()
                && optional(config, "candidateCount").is_none_or(|n| n.as_u64() == Some(1))
                && optional(config, "maxOutputTokens")
                    .is_none_or(|n| n.as_u64().is_some_and(|n| n > 0))
        });
    if valid {
        Ok(())
    } else {
        Err(Failure::invalid(
            "Invalid or unsupported native Gemini request envelope",
        ))
    }
}

// Cached content is a subset of prompt tokens. Optional candidates/thoughts
// may be omitted for zero, but prompt and reported total must establish a
// complete snapshot. Missing usage as a whole stays unknown.
fn usage(value: &Value) -> Result<Option<Usage>, Failure> {
    let Some(value) = value.get("usageMetadata") else {
        return Ok(None);
    };
    let read = |name: &str, required: bool| -> Result<u64, Failure> {
        match value.get(name) {
            Some(n) => n.as_u64().ok_or_else(Failure::upstream),
            None if !required => Ok(0),
            None => Err(Failure::upstream()),
        }
    };
    let input = read("promptTokenCount", true)?;
    let output = read("candidatesTokenCount", false)?
        .checked_add(read("thoughtsTokenCount", false)?)
        .ok_or_else(Failure::upstream)?;
    let total = read("totalTokenCount", true)?;
    if input.checked_add(output) != Some(total)
        || read("cachedContentTokenCount", false)? > input
        || read("toolUsePromptTokenCount", false)? != 0
    {
        return Err(Failure::upstream());
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
struct Envelope {
    candidate: bool,
    blocked: bool,
    terminal: bool,
    usage: Option<Usage>,
}
fn envelope(value: &Value, streaming: bool) -> Result<Envelope, Failure> {
    if !value.is_object()
        || value.get("error").is_some()
        || ["modelVersion", "responseId"]
            .iter()
            .any(|name| optional(value, name).is_some_and(|v| !v.is_string()))
    {
        return Err(Failure::upstream());
    }
    let feedback = optional(value, "promptFeedback");
    if feedback.is_some_and(|v| {
        !v.is_object() || optional(v, "blockReason").is_some_and(|r| !r.is_string())
    }) {
        return Err(Failure::upstream());
    }
    let blocked = feedback.is_some_and(|f| {
        nonempty(&f["blockReason"]) && f["blockReason"] != "BLOCK_REASON_UNSPECIFIED"
    });
    let candidates = match optional(value, "candidates") {
        Some(v) => v.as_array().ok_or_else(Failure::upstream)?.as_slice(),
        None => &[],
    };
    if candidates.len() > 1 || (blocked && !candidates.is_empty()) {
        return Err(Failure::upstream());
    }
    let mut terminal = blocked;
    if let Some(candidate) = candidates.first() {
        if !candidate.is_object()
            || optional(candidate, "index").is_some_and(|i| i.as_u64() != Some(0))
            || optional(candidate, "content").is_some_and(|c| !content(c, false))
            || optional(candidate, "finishReason").is_some_and(|r| !r.is_string())
        {
            return Err(Failure::upstream());
        }
        terminal = nonempty(&candidate["finishReason"])
            && candidate["finishReason"] != "FINISH_REASON_UNSPECIFIED";
        if !terminal && optional(candidate, "content").is_none() {
            return Err(Failure::upstream());
        }
    }
    let usage = usage(value)?;
    if (!streaming && !terminal) || (candidates.is_empty() && !blocked && usage.is_none()) {
        return Err(Failure::upstream());
    }
    Ok(Envelope {
        candidate: !candidates.is_empty(),
        blocked,
        terminal,
        usage,
    })
}
pub(super) fn response(value: Value) -> Result<(Value, Option<Usage>), Failure> {
    let envelope = envelope(&value, false)?;
    Ok((value, envelope.usage))
}

#[derive(Default)]
pub(in crate::runtime) struct StreamDecoder {
    terminal: bool,
    final_usage: bool,
    candidate: bool,
    usage: Option<Usage>,
}
impl StreamDecoder {
    pub(super) fn push(&mut self, event: Event) -> Result<Frame, Failure> {
        if event
            .event
            .as_deref()
            .is_some_and(|e| !e.is_empty() && e != "message")
        {
            return Err(Failure::upstream());
        }
        let value: Value = serde_json::from_str(&event.data).map_err(|_| Failure::upstream())?;
        let envelope = envelope(&value, true)?;
        if (self.terminal && (envelope.candidate || envelope.blocked))
            || (self.candidate && envelope.blocked)
        {
            return Err(Failure::upstream());
        }
        if let Some(usage) = &envelope.usage {
            if self.usage.as_ref().is_some_and(|previous| {
                usage.prompt_tokens < previous.prompt_tokens
                    || usage.completion_tokens < previous.completion_tokens
                    || usage.total_tokens < previous.total_tokens
            }) {
                return Err(Failure::upstream());
            }
            self.usage = Some(usage.clone());
            self.final_usage |= self.terminal || envelope.terminal;
        }
        self.candidate |= envelope.candidate;
        self.terminal |= envelope.terminal;
        Ok(Frame {
            output: format!("data: {value}\n\n"),
            usage: envelope.usage,
            done: false,
        })
    }
    pub(super) fn finish(&self) -> Result<(), Failure> {
        if self.terminal && (self.usage.is_none() || self.final_usage) {
            Ok(())
        } else {
            Err(Failure::upstream())
        }
    }
}
