use anyhow::Result;
use serde_json::Value;
use uuid::Uuid;

use crate::protocol::ir::request::ToolCall;
use crate::protocol::ir::usage::{ServerToolUsage, Usage};
use crate::protocol::ir::{AiResponse, AiStreamDelta};
use crate::protocol::*;

// ── Non-streaming response parser ──

pub struct AnthropicResponseParser;

impl ResponseDecoder for AnthropicResponseParser {
    fn parse_response(&self, resp: Value) -> Result<AiResponse> {
        let id = resp
            .get("id")
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();
        let model = resp
            .get("model")
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();

        let mut content_text = String::new();
        let mut tool_calls = Vec::new();

        let mut thinking_parts: Vec<String> = Vec::new();
        let mut signature_parts: Vec<String> = Vec::new();
        if let Some(blocks) = resp.get("content").and_then(|c| c.as_array()) {
            for block in blocks {
                match block.get("type").and_then(|t| t.as_str()) {
                    Some("text") => {
                        if let Some(text) = block.get("text").and_then(|t| t.as_str()) {
                            content_text.push_str(text);
                        }
                    }
                    Some("thinking") => {
                        // Present in responses from Ollama and native Anthropic thinking models.
                        if let Some(text) = block
                            .get("thinking")
                            .and_then(|t| t.as_str())
                            .filter(|text| !text.is_empty())
                        {
                            thinking_parts.push(text.to_string());
                        }
                        if let Some(signature) = block
                            .get("signature")
                            .and_then(|t| t.as_str())
                            .filter(|signature| !signature.is_empty())
                        {
                            signature_parts.push(signature.to_string());
                        }
                    }
                    Some("tool_use") => {
                        if let (Some(tc_id), Some(name)) = (
                            block.get("id").and_then(|v| v.as_str()),
                            block.get("name").and_then(|v| v.as_str()),
                        ) {
                            let input = block
                                .get("input")
                                .cloned()
                                .unwrap_or(Value::Object(Default::default()));
                            tool_calls.push(ToolCall {
                                id: tc_id.to_string(),
                                name: name.to_string(),
                                arguments: input.to_string(),
                            });
                        }
                    }
                    _ => {}
                }
            }
        }
        let reasoning_content = if thinking_parts.is_empty() {
            None
        } else {
            Some(thinking_parts.join("\n"))
        };
        let reasoning_signature = if signature_parts.is_empty() {
            None
        } else {
            Some(signature_parts.join("\n"))
        };

        let stop_reason = resp
            .get("stop_reason")
            .and_then(|v| v.as_str())
            .map(|r| match r {
                "end_turn" => "stop".to_string(),
                "tool_use" => "tool_calls".to_string(),
                other => other.to_string(),
            });

        let usage = extract_anthropic_usage(&resp);

        let mut ai_resp = AiResponse::new(id, model);
        ai_resp.content = content_text;
        ai_resp.reasoning_content = reasoning_content;
        ai_resp.reasoning_signature = reasoning_signature;
        ai_resp.tool_calls = tool_calls;
        ai_resp.stop_reason = stop_reason;
        ai_resp.usage = usage;
        Ok(ai_resp)
    }
}

// ── Non-streaming response formatter ──

pub struct AnthropicResponseFormatter;

impl ResponseEncoder for AnthropicResponseFormatter {
    fn format_response(&self, resp: &AiResponse) -> Value {
        let mut content = Vec::new();

        if let Some(reasoning) = resp
            .reasoning_content
            .as_ref()
            .map(|v| v.trim())
            .filter(|v| !v.is_empty())
        {
            let mut block = serde_json::json!({
                "type": "thinking",
                "thinking": reasoning,
            });
            if let Some(signature) = resp
                .reasoning_signature
                .as_ref()
                .map(|v| v.trim())
                .filter(|v| !v.is_empty())
            {
                block
                    .as_object_mut()
                    .expect("thinking block is an object")
                    .insert("signature".into(), serde_json::json!(signature));
            }
            content.push(block);
        }

        if !resp.content.is_empty() {
            content.push(serde_json::json!({"type": "text", "text": resp.content}));
        }

        for tc in &resp.tool_calls {
            let input: Value =
                serde_json::from_str(&tc.arguments).unwrap_or(Value::Object(Default::default()));
            content.push(serde_json::json!({
                "type": "tool_use",
                "id": tc.id,
                "name": tc.name,
                "input": input,
            }));
        }

        let stop_reason = resp.stop_reason.as_deref().map(|r| match r {
            "stop" => "end_turn",
            "tool_calls" => "tool_use",
            other => other,
        });

        let mut usage = serde_json::json!({
            "input_tokens": resp.usage.prompt_tokens,
            "output_tokens": resp.usage.completion_tokens,
        });
        extend_usage_json(&mut usage, &resp.usage);

        serde_json::json!({
            "id": resp.id,
            "type": "message",
            "role": "assistant",
            "content": content,
            "model": resp.model,
            "stop_reason": stop_reason,
            "usage": usage
        })
    }
}

// ── Stream parser (upstream Anthropic SSE → deltas) ──

pub struct AnthropicStreamParser {
    buffer: String,
}

impl Default for AnthropicStreamParser {
    fn default() -> Self {
        Self::new()
    }
}

impl AnthropicStreamParser {
    pub fn new() -> Self {
        Self {
            buffer: String::new(),
        }
    }
}

impl StreamResponseDecoder for AnthropicStreamParser {
    fn parse_chunk(&mut self, raw: &str) -> Result<Vec<AiStreamDelta>> {
        self.buffer.push_str(raw);
        let mut deltas = Vec::new();

        while let Some(pos) = self.buffer.find("\n\n") {
            let block = self.buffer[..pos].to_string();
            self.buffer = self.buffer[pos + 2..].to_string();

            let mut event_type = None;
            let mut data_str = None;

            for line in block.lines() {
                if let Some(ev) = line.strip_prefix("event: ") {
                    event_type = Some(ev.trim().to_string());
                } else if let Some(d) = line.strip_prefix("data: ") {
                    data_str = Some(d.trim().to_string());
                }
            }

            if let Some(data) = data_str
                && let Ok(json) = serde_json::from_str::<Value>(&data)
            {
                parse_anthropic_event(event_type.as_deref(), &json, &mut deltas);
            }
        }

        Ok(deltas)
    }

    fn finish(&mut self) -> Result<Vec<AiStreamDelta>> {
        if self.buffer.trim().is_empty() {
            return Ok(vec![]);
        }
        let remaining = std::mem::take(&mut self.buffer);
        self.parse_chunk(&format!("{remaining}\n\n"))
    }
}

fn parse_anthropic_event(event_type: Option<&str>, data: &Value, deltas: &mut Vec<AiStreamDelta>) {
    match event_type {
        Some("message_start") => {
            if let Some(msg) = data.get("message") {
                let id = msg
                    .get("id")
                    .and_then(|v| v.as_str())
                    .unwrap_or("")
                    .to_string();
                let model = msg
                    .get("model")
                    .and_then(|v| v.as_str())
                    .unwrap_or("")
                    .to_string();
                // Usage BEFORE MessageStart so the formatter has the correct
                // input_tokens available when it emits the message_start SSE event.
                let u = extract_anthropic_usage(msg);
                if u.prompt_tokens > 0
                    || u.cache_read_tokens.is_some()
                    || u.cache_creation_tokens.is_some()
                    || u.server_tool_use.is_some()
                {
                    deltas.push(AiStreamDelta::Usage(u));
                }
                deltas.push(AiStreamDelta::MessageStart { id, model });
            }
        }
        Some("content_block_start") => {
            let idx = data.get("index").and_then(|v| v.as_u64()).unwrap_or(0) as usize;
            if let Some(block) = data.get("content_block") {
                match block.get("type").and_then(|t| t.as_str()) {
                    Some("tool_use") => {
                        let id = block
                            .get("id")
                            .and_then(|v| v.as_str())
                            .unwrap_or("")
                            .to_string();
                        let name = block
                            .get("name")
                            .and_then(|v| v.as_str())
                            .unwrap_or("")
                            .to_string();
                        deltas.push(AiStreamDelta::ToolCallStart {
                            index: idx,
                            id,
                            name,
                        });
                    }
                    // Anthropic server-side tool blocks (web_search, code_execution,
                    // mcp_tool_use, etc.) and any future block types not yet known:
                    // forward verbatim so downstream clients receive the full event.
                    _ => {
                        deltas.push(AiStreamDelta::Unknown {
                            raw: format!("event: content_block_start\ndata: {data}"),
                        });
                    }
                }
            }
        }
        Some("content_block_delta") => {
            if let Some(delta) = data.get("delta") {
                match delta.get("type").and_then(|t| t.as_str()) {
                    Some("text_delta") => {
                        if let Some(text) = delta.get("text").and_then(|t| t.as_str()) {
                            deltas.push(AiStreamDelta::TextDelta(text.to_string()));
                        }
                    }
                    Some("thinking_delta") => {
                        // Produced by Ollama and native Anthropic thinking models.
                        if let Some(text) = delta
                            .get("thinking")
                            .and_then(|t| t.as_str())
                            .filter(|text| !text.is_empty())
                        {
                            deltas.push(AiStreamDelta::ThinkingDelta(text.to_string()));
                        }
                    }
                    Some("signature_delta") => {
                        if let Some(signature) = delta
                            .get("signature")
                            .and_then(|t| t.as_str())
                            .filter(|signature| !signature.is_empty())
                        {
                            deltas.push(AiStreamDelta::ThinkingSignature(signature.to_string()));
                        }
                    }
                    Some("input_json_delta") => {
                        if let Some(json) = delta.get("partial_json").and_then(|t| t.as_str()) {
                            let idx =
                                data.get("index").and_then(|v| v.as_u64()).unwrap_or(0) as usize;
                            deltas.push(AiStreamDelta::ToolCallDelta {
                                index: idx,
                                arguments: json.to_string(),
                            });
                        }
                    }
                    // Unknown delta types (e.g. web_search_tool_result_delta,
                    // citations_delta, code_execution_tool_result_delta):
                    // forward verbatim.
                    _ => {
                        deltas.push(AiStreamDelta::Unknown {
                            raw: format!("event: content_block_delta\ndata: {data}"),
                        });
                    }
                }
            }
        }
        Some("message_delta") => {
            // Usage BEFORE Done: the formatter emits message_delta SSE on Done,
            // so self.usage must already reflect the final counts.
            // Also read input_tokens here — ZhipuAI and others publish the real
            // value in message_delta.usage rather than message_start.usage.
            let u = extract_anthropic_usage(data);
            if u.prompt_tokens > 0
                || u.completion_tokens > 0
                || u.cache_read_tokens.is_some()
                || u.cache_creation_tokens.is_some()
                || u.server_tool_use.is_some()
            {
                deltas.push(AiStreamDelta::Usage(u));
            }
            if let Some(delta) = data.get("delta")
                && let Some(reason) = delta.get("stop_reason").and_then(|v| v.as_str())
            {
                let normalized = match reason {
                    "end_turn" => "stop",
                    "tool_use" => "tool_calls",
                    other => other,
                };
                deltas.push(AiStreamDelta::Done {
                    stop_reason: normalized.to_string(),
                });
            }
        }
        Some("ping") | Some("content_block_stop") | Some("message_stop") => {}
        // Unknown top-level event types: forward verbatim so no data is dropped.
        Some(ev) => {
            deltas.push(AiStreamDelta::Unknown {
                raw: format!("event: {ev}\ndata: {data}"),
            });
        }
        None => {}
    }
}

// ── Stream formatter (deltas → Anthropic SSE) ──

pub struct AnthropicStreamFormatter {
    usage: Usage,
    id: String,
    model: String,
    block_index: usize,
    in_thinking_block: bool,
    in_text_block: bool,
    in_tool_block: bool,
    message_started: bool,
}

impl Default for AnthropicStreamFormatter {
    fn default() -> Self {
        Self::new()
    }
}

impl AnthropicStreamFormatter {
    pub fn new() -> Self {
        Self {
            usage: Usage::default(),
            id: format!("msg_{}", Uuid::new_v4().simple()),
            model: String::new(),
            block_index: 0,
            in_thinking_block: false,
            in_text_block: false,
            in_tool_block: false,
            message_started: false,
        }
    }

    fn ensure_message_start(&mut self, events: &mut Vec<SseEvent>) {
        if self.message_started {
            return;
        }
        self.message_started = true;
        let mut usage = serde_json::json!({
            "input_tokens": self.usage.prompt_tokens,
            "output_tokens": 0
        });
        extend_usage_json(&mut usage, &self.usage);
        let msg_start = serde_json::json!({
            "type": "message_start",
            "message": {
                "id": self.id,
                "type": "message",
                "role": "assistant",
                "content": [],
                "model": self.model,
                "stop_reason": null,
                "usage": usage
            }
        });
        events.push(SseEvent::new(Some("message_start"), msg_start.to_string()));
        events.push(SseEvent::new(Some("ping"), r#"{"type":"ping"}"#));
    }
}

impl StreamResponseEncoder for AnthropicStreamFormatter {
    fn format_deltas(&mut self, deltas: &[AiStreamDelta]) -> Vec<SseEvent> {
        let mut events = Vec::new();

        for delta in deltas {
            match delta {
                AiStreamDelta::MessageStart { id, model } => {
                    self.id = id.clone();
                    self.model = model.clone();
                    self.ensure_message_start(&mut events);
                }
                AiStreamDelta::ThinkingDelta(text) => {
                    self.ensure_message_start(&mut events);
                    self.close_text_block_if_open(&mut events);
                    if !self.in_thinking_block {
                        self.in_thinking_block = true;
                        let block_start = serde_json::json!({
                            "type": "content_block_start",
                            "index": self.block_index,
                            "content_block": {"type": "thinking", "thinking": ""}
                        });
                        events.push(SseEvent::new(
                            Some("content_block_start"),
                            block_start.to_string(),
                        ));
                    }
                    let delta_ev = serde_json::json!({
                        "type": "content_block_delta",
                        "index": self.block_index,
                        "delta": {"type": "thinking_delta", "thinking": text}
                    });
                    events.push(SseEvent::new(
                        Some("content_block_delta"),
                        delta_ev.to_string(),
                    ));
                }
                AiStreamDelta::ThinkingSignature(signature) => {
                    self.ensure_message_start(&mut events);
                    self.close_text_block_if_open(&mut events);
                    if !self.in_thinking_block {
                        self.in_thinking_block = true;
                        let block_start = serde_json::json!({
                            "type": "content_block_start",
                            "index": self.block_index,
                            "content_block": {"type": "thinking", "thinking": ""}
                        });
                        events.push(SseEvent::new(
                            Some("content_block_start"),
                            block_start.to_string(),
                        ));
                    }
                    let delta_ev = serde_json::json!({
                        "type": "content_block_delta",
                        "index": self.block_index,
                        "delta": {"type": "signature_delta", "signature": signature}
                    });
                    events.push(SseEvent::new(
                        Some("content_block_delta"),
                        delta_ev.to_string(),
                    ));
                }
                AiStreamDelta::TextDelta(text) => {
                    if !self.in_text_block && text.trim().is_empty() {
                        continue;
                    }
                    self.ensure_message_start(&mut events);
                    self.close_thinking_block_if_open(&mut events);
                    self.close_tool_block_if_open(&mut events);
                    if !self.in_text_block {
                        self.in_text_block = true;
                        let block_start = serde_json::json!({
                            "type": "content_block_start",
                            "index": self.block_index,
                            "content_block": {"type": "text", "text": ""}
                        });
                        events.push(SseEvent::new(
                            Some("content_block_start"),
                            block_start.to_string(),
                        ));
                    }
                    let delta_ev = serde_json::json!({
                        "type": "content_block_delta",
                        "index": self.block_index,
                        "delta": {"type": "text_delta", "text": text}
                    });
                    events.push(SseEvent::new(
                        Some("content_block_delta"),
                        delta_ev.to_string(),
                    ));
                }
                AiStreamDelta::ToolCallStart { index: _, id, name } => {
                    self.ensure_message_start(&mut events);
                    self.close_thinking_block_if_open(&mut events);
                    self.close_text_block_if_open(&mut events);
                    self.close_tool_block_if_open(&mut events);
                    let tool_use_id = if id.trim().is_empty() {
                        format!("toolu_{}", Uuid::new_v4().simple())
                    } else {
                        id.clone()
                    };
                    let block_start = serde_json::json!({
                        "type": "content_block_start",
                        "index": self.block_index,
                        "content_block": {"type": "tool_use", "id": tool_use_id, "name": name, "input": {}}
                    });
                    events.push(SseEvent::new(
                        Some("content_block_start"),
                        block_start.to_string(),
                    ));
                    self.in_tool_block = true;
                }
                AiStreamDelta::ToolCallDelta {
                    index: _,
                    arguments,
                } => {
                    let delta_ev = serde_json::json!({
                        "type": "content_block_delta",
                        "index": self.block_index,
                        "delta": {"type": "input_json_delta", "partial_json": arguments}
                    });
                    events.push(SseEvent::new(
                        Some("content_block_delta"),
                        delta_ev.to_string(),
                    ));
                }
                AiStreamDelta::Usage(u) => {
                    if u.prompt_tokens > 0 {
                        self.usage.prompt_tokens = u.prompt_tokens;
                    }
                    if u.completion_tokens > 0 {
                        self.usage.completion_tokens = u.completion_tokens;
                    }
                    if u.cache_read_tokens.is_some() {
                        self.usage.cache_read_tokens = u.cache_read_tokens;
                    }
                    if u.cache_creation_tokens.is_some() {
                        self.usage.cache_creation_tokens = u.cache_creation_tokens;
                    }
                    if u.server_tool_use.is_some() {
                        self.usage.server_tool_use = u.server_tool_use.clone();
                    }
                }
                AiStreamDelta::Done { stop_reason } => {
                    self.ensure_message_start(&mut events);
                    self.close_thinking_block_if_open(&mut events);
                    self.close_text_block_if_open(&mut events);
                    self.close_tool_block_if_open(&mut events);
                    let anthropic_reason = match stop_reason.as_str() {
                        "stop" => "end_turn",
                        "tool_calls" => "tool_use",
                        other => other,
                    };
                    let mut usage = serde_json::json!({
                        "output_tokens": self.usage.completion_tokens
                    });
                    extend_usage_json(&mut usage, &self.usage);
                    let msg_delta = serde_json::json!({
                        "type": "message_delta",
                        "delta": {"stop_reason": anthropic_reason},
                        "usage": usage
                    });
                    events.push(SseEvent::new(Some("message_delta"), msg_delta.to_string()));
                    events.push(SseEvent::new(
                        Some("message_stop"),
                        r#"{"type":"message_stop"}"#,
                    ));
                }
                // Pass-through: raw is in SSE format "event: TYPE\ndata: DATA".
                AiStreamDelta::Unknown { raw } => {
                    let mut ev_type = None;
                    let mut ev_data = String::new();
                    for line in raw.lines() {
                        if let Some(t) = line.strip_prefix("event: ") {
                            ev_type = Some(t.to_string());
                        } else if let Some(d) = line.strip_prefix("data: ") {
                            ev_data = d.to_string();
                        }
                    }
                    if let Some(et) = ev_type {
                        events.push(SseEvent::new(Some(et.as_str()), ev_data));
                    }
                }
                _ => {}
            }
        }

        events
    }

    fn format_done(&mut self) -> Vec<SseEvent> {
        vec![]
    }

    fn usage(&self) -> Usage {
        self.usage.clone()
    }
}

impl AnthropicStreamFormatter {
    fn close_text_block_if_open(&mut self, events: &mut Vec<SseEvent>) {
        if !self.in_text_block {
            return;
        }
        let stop = serde_json::json!({
            "type": "content_block_stop",
            "index": self.block_index,
        });
        events.push(SseEvent::new(Some("content_block_stop"), stop.to_string()));
        self.block_index += 1;
        self.in_text_block = false;
    }

    fn close_thinking_block_if_open(&mut self, events: &mut Vec<SseEvent>) {
        if !self.in_thinking_block {
            return;
        }
        let stop = serde_json::json!({
            "type": "content_block_stop",
            "index": self.block_index,
        });
        events.push(SseEvent::new(Some("content_block_stop"), stop.to_string()));
        self.block_index += 1;
        self.in_thinking_block = false;
    }

    fn close_tool_block_if_open(&mut self, events: &mut Vec<SseEvent>) {
        if !self.in_tool_block {
            return;
        }
        let stop = serde_json::json!({
            "type": "content_block_stop",
            "index": self.block_index,
        });
        events.push(SseEvent::new(Some("content_block_stop"), stop.to_string()));
        self.block_index += 1;
        self.in_tool_block = false;
    }
}

fn extract_anthropic_usage(v: &Value) -> Usage {
    let Some(u) = v.get("usage") else {
        return Usage::default();
    };
    let get_u32 = |key: &str| u.get(key).and_then(|v| v.as_u64()).unwrap_or(0) as u32;
    let get_opt_u32 = |key: &str| u.get(key).and_then(|v| v.as_u64()).map(|n| n as u32);
    let server_tool_use = u.get("server_tool_use").map(|stu| ServerToolUsage {
        web_search_requests: stu
            .get("web_search_requests")
            .and_then(|v| v.as_u64())
            .unwrap_or(0) as u32,
        web_fetch_requests: stu
            .get("web_fetch_requests")
            .and_then(|v| v.as_u64())
            .unwrap_or(0) as u32,
    });
    Usage {
        prompt_tokens: get_u32("input_tokens"),
        completion_tokens: get_u32("output_tokens"),
        cache_read_tokens: get_opt_u32("cache_read_input_tokens"),
        cache_creation_tokens: get_opt_u32("cache_creation_input_tokens"),
        server_tool_use,
        ..Usage::default()
    }
}

/// Append optional Anthropic-specific usage fields to an existing JSON usage object.
/// Omits keys whose values are `None`.
fn extend_usage_json(obj: &mut Value, u: &Usage) {
    if let Some(v) = u.cache_read_tokens {
        obj["cache_read_input_tokens"] = v.into();
    }
    if let Some(v) = u.cache_creation_tokens {
        obj["cache_creation_input_tokens"] = v.into();
    }
    if let Some(ref stu) = u.server_tool_use {
        obj["server_tool_use"] = serde_json::json!({
            "web_search_requests": stu.web_search_requests,
            "web_fetch_requests": stu.web_fetch_requests,
        });
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::ir::{AiResponse, AiStreamDelta};
    use crate::protocol::{
        ResponseDecoder, ResponseEncoder, StreamResponseDecoder, StreamResponseEncoder,
    };

    fn make_sse_block(event: &str, data: &str) -> String {
        format!("event: {event}\ndata: {data}\n\n")
    }

    // ── AnthropicResponseParser ──

    #[test]
    fn test_parse_response_text_only() {
        let resp = serde_json::json!({
            "id": "msg_1",
            "model": "claude-3-5-sonnet",
            "content": [{"type": "text", "text": "hello"}],
            "stop_reason": "end_turn",
            "usage": {"input_tokens": 5, "output_tokens": 3}
        });
        let r = AnthropicResponseParser.parse_response(resp).unwrap();
        assert_eq!(r.content, "hello");
        assert!(r.reasoning_content.is_none());
        assert_eq!(r.stop_reason.as_deref(), Some("stop"));
    }

    #[test]
    fn test_parse_response_thinking_and_text() {
        // Ollama returns thinking + text blocks in non-stream response.
        let resp = serde_json::json!({
            "id": "msg_2",
            "model": "qwen3",
            "content": [
                {"type": "thinking", "thinking": "let me think...", "signature": "sig_resp"},
                {"type": "text", "text": "hi there"}
            ],
            "stop_reason": "end_turn",
            "usage": {"input_tokens": 10, "output_tokens": 20}
        });
        let r = AnthropicResponseParser.parse_response(resp).unwrap();
        assert_eq!(r.content, "hi there");
        assert_eq!(r.reasoning_content.as_deref(), Some("let me think..."));
        assert_eq!(r.reasoning_signature.as_deref(), Some("sig_resp"));
    }

    #[test]
    fn test_format_response_includes_thinking_signature() {
        let mut resp = AiResponse::new("msg_sig", "claude-3-7-sonnet");
        resp.content = "answer".to_string();
        resp.reasoning_content = Some("think".to_string());
        resp.reasoning_signature = Some("sig_resp".to_string());
        resp.stop_reason = Some("stop".to_string());

        let out = AnthropicResponseFormatter.format_response(&resp);
        let thinking = out
            .get("content")
            .and_then(|v| v.as_array())
            .and_then(|arr| arr.first())
            .expect("thinking block");
        assert_eq!(
            thinking.get("type").and_then(|v| v.as_str()),
            Some("thinking")
        );
        assert_eq!(
            thinking.get("signature").and_then(|v| v.as_str()),
            Some("sig_resp")
        );
    }

    #[test]
    fn test_parse_response_tool_use() {
        let resp = serde_json::json!({
            "id": "msg_3",
            "model": "claude-3-5-sonnet",
            "content": [{
                "type": "tool_use",
                "id": "toolu_01",
                "name": "get_weather",
                "input": {"city": "Paris"}
            }],
            "stop_reason": "tool_use",
            "usage": {"input_tokens": 15, "output_tokens": 8}
        });
        let r = AnthropicResponseParser.parse_response(resp).unwrap();
        assert_eq!(r.tool_calls.len(), 1);
        assert_eq!(r.tool_calls[0].name, "get_weather");
        assert_eq!(r.stop_reason.as_deref(), Some("tool_calls"));
    }

    // ── AnthropicStreamParser ──

    #[test]
    fn test_stream_basic_text() {
        let sse = [
            make_sse_block(
                "message_start",
                r#"{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-3-5-sonnet","stop_reason":null,"usage":{"input_tokens":9,"output_tokens":0}}}"#,
            ),
            make_sse_block(
                "content_block_start",
                r#"{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}"#,
            ),
            make_sse_block(
                "content_block_delta",
                r#"{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}"#,
            ),
            make_sse_block("content_block_stop", r#"{"type":"content_block_stop","index":0}"#),
            make_sse_block(
                "message_delta",
                r#"{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}"#,
            ),
            make_sse_block("message_stop", r#"{"type":"message_stop"}"#),
        ]
        .concat();

        let mut parser = AnthropicStreamParser::new();
        let deltas = parser.parse_chunk(&sse).unwrap();

        let has_text = deltas
            .iter()
            .any(|d| matches!(d, AiStreamDelta::TextDelta(t) if t == "hello"));
        let has_done = deltas
            .iter()
            .any(|d| matches!(d, AiStreamDelta::Done { stop_reason } if stop_reason == "stop"));
        assert!(has_text, "expected TextDelta('hello'), got: {deltas:?}");
        assert!(has_done, "expected Done(stop), got: {deltas:?}");
    }

    #[test]
    fn test_stream_thinking_delta_no_signature_delta() {
        // Ollama sends thinking_delta events but omits signature_delta entirely.
        // Parser must not fail and must emit ReasoningDelta.
        let sse = [
            make_sse_block(
                "message_start",
                r#"{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","content":[],"model":"qwen3","stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}"#,
            ),
            make_sse_block(
                "content_block_start",
                r#"{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}"#,
            ),
            make_sse_block(
                "content_block_delta",
                r#"{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"step one"}}"#,
            ),
            make_sse_block(
                "content_block_delta",
                r#"{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" step two"}}"#,
            ),
            // No signature_delta here (Ollama omits it)
            make_sse_block("content_block_stop", r#"{"type":"content_block_stop","index":0}"#),
            make_sse_block(
                "content_block_start",
                r#"{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}"#,
            ),
            make_sse_block(
                "content_block_delta",
                r#"{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"answer"}}"#,
            ),
            make_sse_block("content_block_stop", r#"{"type":"content_block_stop","index":1}"#),
            make_sse_block(
                "message_delta",
                r#"{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}"#,
            ),
            make_sse_block("message_stop", r#"{"type":"message_stop"}"#),
        ]
        .concat();

        let mut parser = AnthropicStreamParser::new();
        let deltas = parser.parse_chunk(&sse).unwrap();

        let reasoning: Vec<_> = deltas
            .iter()
            .filter_map(|d| {
                if let AiStreamDelta::ThinkingDelta(t) = d {
                    Some(t.as_str())
                } else {
                    None
                }
            })
            .collect();
        assert!(
            !reasoning.is_empty(),
            "expected ReasoningDelta events, got: {deltas:?}"
        );
        assert!(
            reasoning.contains(&"step one"),
            "expected 'step one', got: {reasoning:?}"
        );

        let has_text = deltas
            .iter()
            .any(|d| matches!(d, AiStreamDelta::TextDelta(t) if t == "answer"));
        assert!(has_text, "expected TextDelta('answer'), got: {deltas:?}");
    }

    #[test]
    fn test_stream_signature_delta_is_captured() {
        // Native Anthropic sends signature_delta after thinking block.
        let sse = [
            make_sse_block(
                "message_start",
                r#"{"type":"message_start","message":{"id":"msg_3","model":"claude-3-7-sonnet","content":[],"stop_reason":null,"usage":{"input_tokens":8,"output_tokens":0}}}"#,
            ),
            make_sse_block(
                "content_block_delta",
                r#"{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think"}}"#,
            ),
            make_sse_block(
                "content_block_delta",
                r#"{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"abc123"}}"#,
            ),
            make_sse_block(
                "message_delta",
                r#"{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}"#,
            ),
        ]
        .concat();

        let mut parser = AnthropicStreamParser::new();
        let result = parser.parse_chunk(&sse);
        assert!(result.is_ok(), "parser must not fail on signature_delta");

        let deltas = result.unwrap();
        let signature = deltas.iter().find_map(|d| {
            if let AiStreamDelta::ThinkingSignature(sig) = d {
                Some(sig.as_str())
            } else {
                None
            }
        });
        assert_eq!(signature, Some("abc123"));
    }

    #[test]
    fn test_stream_formatter_emits_signature_delta() {
        let mut formatter = AnthropicStreamFormatter::new();
        let events = formatter.format_deltas(&[
            AiStreamDelta::MessageStart {
                id: "msg_4".to_string(),
                model: "claude-3-7-sonnet".to_string(),
            },
            AiStreamDelta::ThinkingDelta("think".to_string()),
            AiStreamDelta::ThinkingSignature("abc123".to_string()),
        ]);

        let has_signature = events
            .iter()
            .filter_map(|event| serde_json::from_str::<Value>(&event.data).ok())
            .any(|json| {
                json.get("delta")
                    .and_then(|delta| delta.get("signature"))
                    .and_then(|signature| signature.as_str())
                    == Some("abc123")
            });
        assert!(has_signature, "expected signature_delta event");
    }

    #[test]
    fn test_stream_tool_use() {
        let sse = [
            make_sse_block(
                "message_start",
                r#"{"type":"message_start","message":{"id":"msg_4","model":"claude-3-5-sonnet","content":[],"stop_reason":null,"usage":{"input_tokens":20,"output_tokens":0}}}"#,
            ),
            make_sse_block(
                "content_block_start",
                r#"{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_01","name":"get_weather","input":{}}}"#,
            ),
            make_sse_block(
                "content_block_delta",
                r#"{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}"#,
            ),
            make_sse_block(
                "content_block_delta",
                r#"{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}"#,
            ),
            make_sse_block("content_block_stop", r#"{"type":"content_block_stop","index":0}"#),
            make_sse_block(
                "message_delta",
                r#"{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":12}}"#,
            ),
        ]
        .concat();

        let mut parser = AnthropicStreamParser::new();
        let deltas = parser.parse_chunk(&sse).unwrap();

        let has_tool_start = deltas.iter().any(
            |d| matches!(d, AiStreamDelta::ToolCallStart { name, .. } if name == "get_weather"),
        );
        let has_tool_delta = deltas
            .iter()
            .any(|d| matches!(d, AiStreamDelta::ToolCallDelta { .. }));
        let has_done_tool = deltas.iter().any(
            |d| matches!(d, AiStreamDelta::Done { stop_reason } if stop_reason == "tool_calls"),
        );
        assert!(
            has_tool_start,
            "expected ToolCallStart(get_weather), got: {deltas:?}"
        );
        assert!(has_tool_delta, "expected ToolCallDelta, got: {deltas:?}");
        assert!(has_done_tool, "expected Done(tool_calls), got: {deltas:?}");
    }

    // ── P2: RawEvent forwarding ───────────────────────────────────────────────

    #[test]
    fn test_unknown_content_block_start_emits_raw_event() {
        // GLM server-side tool (e.g. webReader) sends a content_block_start with
        // type "server_tool_use". The parser must emit RawEvent instead of dropping it.
        let sse = [
            make_sse_block(
                "message_start",
                r#"{"type":"message_start","message":{"id":"msg_5","model":"glm-5","content":[],"stop_reason":null,"usage":{"input_tokens":5,"output_tokens":0}}}"#,
            ),
            make_sse_block(
                "content_block_start",
                r#"{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtool_01","name":"webReader","input":{}}}"#,
            ),
            make_sse_block(
                "content_block_stop",
                r#"{"type":"content_block_stop","index":0}"#,
            ),
            make_sse_block(
                "message_delta",
                r#"{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":4}}"#,
            ),
        ]
        .concat();

        let mut parser = AnthropicStreamParser::new();
        let deltas = parser.parse_chunk(&sse).unwrap();

        let raw_str = deltas.iter().find_map(|d| {
            if let AiStreamDelta::Unknown { raw } = d {
                if raw.contains("content_block_start") {
                    Some(raw.clone())
                } else {
                    None
                }
            } else {
                None
            }
        });
        assert!(
            raw_str.is_some(),
            "expected Unknown for server_tool_use, got: {deltas:?}"
        );
        let raw_str = raw_str.unwrap();
        assert!(
            raw_str.contains("content_block_start"),
            "event type mismatch: {raw_str}"
        );
        let data_part = raw_str.splitn(2, "\ndata: ").nth(1).unwrap_or("{}");
        let data: serde_json::Value = serde_json::from_str(data_part).unwrap_or_default();
        assert_eq!(
            data.pointer("/content_block/type").and_then(|v| v.as_str()),
            Some("server_tool_use"),
        );
    }

    #[test]
    fn test_unknown_top_level_event_emits_raw_event() {
        // A future or provider-specific event type must not be silently dropped.
        let sse = make_sse_block(
            "web_search_result",
            r#"{"type":"web_search_result","results":[{"title":"foo","url":"https://example.com"}]}"#,
        );

        let mut parser = AnthropicStreamParser::new();
        let deltas = parser.parse_chunk(&sse).unwrap();

        let raw = deltas.iter().find(
            |d| matches!(d, AiStreamDelta::Unknown { raw } if raw.contains("web_search_result")),
        );
        assert!(
            raw.is_some(),
            "expected Unknown for web_search_result, got: {deltas:?}"
        );
    }

    #[test]
    fn test_raw_event_forwarded_verbatim_by_formatter() {
        // AnthropicStreamFormatter must emit a verbatim SSE event for RawEvent.
        let raw_data = serde_json::json!({
            "type": "content_block_start",
            "index": 0,
            "content_block": {"type": "server_tool_use", "id": "srv_01", "name": "webSearch", "input": {}}
        });

        let mut formatter = AnthropicStreamFormatter::new();
        let events = formatter.format_deltas(&[
            AiStreamDelta::MessageStart {
                id: "msg_6".to_string(),
                model: "glm-5".to_string(),
            },
            AiStreamDelta::Unknown {
                raw: format!("event: content_block_start\ndata: {raw_data}"),
            },
        ]);

        let raw_forwarded = events.iter().find(|ev| {
            ev.event.as_deref() == Some("content_block_start")
                && ev.data.contains("server_tool_use")
        });
        assert!(
            raw_forwarded.is_some(),
            "formatter must forward RawEvent verbatim; events: {events:?}",
        );
    }

    #[test]
    fn test_unknown_content_block_delta_type_emits_raw_event() {
        // Unknown delta types (citations_delta, web_search_tool_result_delta, etc.)
        // must be forwarded as RawEvent, not silently dropped.
        let sse = make_sse_block(
            "content_block_delta",
            r#"{"type":"content_block_delta","index":0,"delta":{"type":"citations_delta","citation":{"url":"https://example.com","title":"Example"}}}"#,
        );

        let mut parser = AnthropicStreamParser::new();
        let deltas = parser.parse_chunk(&sse).unwrap();

        let raw = deltas.iter().find(
            |d| matches!(d, AiStreamDelta::Unknown { raw } if raw.contains("content_block_delta")),
        );
        assert!(
            raw.is_some(),
            "expected Unknown for citations_delta, got: {deltas:?}"
        );
    }

    // ── Bug-fix ordering tests (Task 0) ──

    #[test]
    fn test_usage_delta_before_message_start() {
        // Bug 0a: Usage must appear BEFORE MessageStart in the delta list so the
        // formatter has the correct input_tokens when it emits message_start SSE.
        let sse = make_sse_block(
            "message_start",
            r#"{"type":"message_start","message":{"id":"msg_1","model":"glm","usage":{"input_tokens":10,"output_tokens":0}}}"#,
        );
        let mut parser = AnthropicStreamParser::new();
        let deltas = parser.parse_chunk(&sse).unwrap();

        let usage_pos = deltas
            .iter()
            .position(|d| matches!(d, AiStreamDelta::Usage(_)));
        let start_pos = deltas
            .iter()
            .position(|d| matches!(d, AiStreamDelta::MessageStart { .. }));
        assert!(
            usage_pos.is_some() && start_pos.is_some(),
            "both deltas must be present; got: {deltas:?}",
        );
        assert!(
            usage_pos.unwrap() < start_pos.unwrap(),
            "Usage must precede MessageStart; got: {deltas:?}",
        );
    }

    #[test]
    fn test_usage_delta_before_done() {
        // Bug 0b: Usage must appear BEFORE Done in the delta list so the formatter
        // has the correct output_tokens when it emits message_delta SSE.
        let sse = make_sse_block(
            "message_delta",
            r#"{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":43}}"#,
        );
        let mut parser = AnthropicStreamParser::new();
        let deltas = parser.parse_chunk(&sse).unwrap();

        let usage_pos = deltas
            .iter()
            .position(|d| matches!(d, AiStreamDelta::Usage(_)));
        let done_pos = deltas
            .iter()
            .position(|d| matches!(d, AiStreamDelta::Done { .. }));
        assert!(
            usage_pos.is_some() && done_pos.is_some(),
            "both deltas must be present; got: {deltas:?}",
        );
        assert!(
            usage_pos.unwrap() < done_pos.unwrap(),
            "Usage must precede Done; got: {deltas:?}",
        );
    }

    #[test]
    fn test_message_delta_input_tokens_read() {
        // Bug 0c: input_tokens from message_delta.usage must be captured.
        // ZhipuAI / MiniMax publish the real input count here instead of message_start.
        let sse = make_sse_block(
            "message_delta",
            r#"{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":60,"output_tokens":43}}"#,
        );
        let mut parser = AnthropicStreamParser::new();
        let deltas = parser.parse_chunk(&sse).unwrap();

        let usage = deltas
            .iter()
            .find_map(|d| {
                if let AiStreamDelta::Usage(u) = d {
                    Some(u)
                } else {
                    None
                }
            })
            .expect("Usage delta must be present");
        assert_eq!(usage.prompt_tokens, 60, "prompt_tokens must be 60");
        assert_eq!(usage.completion_tokens, 43, "completion_tokens must be 43");
    }

    // ── New usage-field extraction tests (Task 2 – parser) ──

    #[test]
    fn test_cache_fields_extracted_from_message_start() {
        let sse = make_sse_block(
            "message_start",
            r#"{"type":"message_start","message":{"id":"m","model":"c","usage":{"input_tokens":100,"output_tokens":0,"cache_read_input_tokens":50,"cache_creation_input_tokens":200}}}"#,
        );
        let mut parser = AnthropicStreamParser::new();
        let deltas = parser.parse_chunk(&sse).unwrap();

        let usage = deltas
            .iter()
            .find_map(|d| {
                if let AiStreamDelta::Usage(u) = d {
                    Some(u)
                } else {
                    None
                }
            })
            .expect("Usage delta must be present");
        assert_eq!(usage.cache_read_tokens, Some(50));
        assert_eq!(usage.cache_creation_tokens, Some(200));
    }

    #[test]
    fn test_server_tool_use_extracted_from_message_delta() {
        let sse = make_sse_block(
            "message_delta",
            r#"{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10,"server_tool_use":{"web_search_requests":3,"web_fetch_requests":1}}}"#,
        );
        let mut parser = AnthropicStreamParser::new();
        let deltas = parser.parse_chunk(&sse).unwrap();

        let usage = deltas
            .iter()
            .find_map(|d| {
                if let AiStreamDelta::Usage(u) = d {
                    Some(u)
                } else {
                    None
                }
            })
            .expect("Usage delta must be present");
        let stu = usage
            .server_tool_use
            .as_ref()
            .expect("server_tool_use must be Some");
        assert_eq!(stu.web_search_requests, 3);
        assert_eq!(stu.web_fetch_requests, 1);
    }

    // ── New usage-field emission tests (Task 2 – formatter) ──

    #[test]
    fn test_formatter_message_start_includes_cache_fields() {
        // Usage delta carrying cache fields must appear in the message_start SSE output.
        let usage = Usage {
            prompt_tokens: 100,
            completion_tokens: 0,
            cache_read_tokens: Some(50),
            cache_creation_tokens: Some(200),
            server_tool_use: None,
            ..Usage::default()
        };
        let mut formatter = AnthropicStreamFormatter::new();
        let events = formatter.format_deltas(&[
            AiStreamDelta::Usage(usage),
            AiStreamDelta::MessageStart {
                id: "m1".into(),
                model: "c".into(),
            },
        ]);

        let start_ev = events
            .iter()
            .find(|e| e.event.as_deref() == Some("message_start"))
            .expect("message_start event must be emitted");
        let json: serde_json::Value = serde_json::from_str(&start_ev.data).unwrap();
        let u = json
            .pointer("/message/usage")
            .expect("/message/usage must exist");
        assert_eq!(u["input_tokens"].as_u64(), Some(100));
        assert_eq!(u["cache_read_input_tokens"].as_u64(), Some(50));
        assert_eq!(u["cache_creation_input_tokens"].as_u64(), Some(200));
        assert!(
            u.get("server_tool_use").is_none(),
            "server_tool_use must be absent when None"
        );
    }

    #[test]
    fn test_formatter_message_delta_includes_new_fields() {
        // message_delta SSE must carry cache and server_tool_use when present.
        let usage = Usage {
            prompt_tokens: 60,
            completion_tokens: 43,
            cache_read_tokens: Some(10),
            cache_creation_tokens: None,
            server_tool_use: Some(ServerToolUsage {
                web_search_requests: 2,
                web_fetch_requests: 0,
            }),
            ..Usage::default()
        };
        let mut formatter = AnthropicStreamFormatter::new();
        let events = formatter.format_deltas(&[
            AiStreamDelta::Usage(usage),
            AiStreamDelta::MessageStart {
                id: "m2".into(),
                model: "c".into(),
            },
            AiStreamDelta::Done {
                stop_reason: "stop".into(),
            },
        ]);

        let delta_ev = events
            .iter()
            .find(|e| e.event.as_deref() == Some("message_delta"))
            .expect("message_delta event must be emitted");
        let json: serde_json::Value = serde_json::from_str(&delta_ev.data).unwrap();
        let u = &json["usage"];
        assert_eq!(u["output_tokens"].as_u64(), Some(43));
        assert_eq!(u["cache_read_input_tokens"].as_u64(), Some(10));
        assert!(
            u.get("cache_creation_input_tokens").is_none(),
            "None field must be absent"
        );
        assert_eq!(
            u["server_tool_use"]["web_search_requests"].as_u64(),
            Some(2)
        );
    }

    #[test]
    fn test_format_response_includes_cache_fields() {
        let mut resp = AiResponse::new("m3", "claude");
        resp.content = "hi".to_string();
        resp.stop_reason = Some("stop".to_string());
        resp.usage = Usage {
            prompt_tokens: 10,
            completion_tokens: 5,
            cache_read_tokens: Some(3),
            cache_creation_tokens: Some(7),
            server_tool_use: Some(ServerToolUsage {
                web_search_requests: 1,
                web_fetch_requests: 0,
            }),
            ..Usage::default()
        };
        let json = AnthropicResponseFormatter.format_response(&resp);
        let u = &json["usage"];
        assert_eq!(u["input_tokens"].as_u64(), Some(10));
        assert_eq!(u["output_tokens"].as_u64(), Some(5));
        assert_eq!(u["cache_read_input_tokens"].as_u64(), Some(3));
        assert_eq!(u["cache_creation_input_tokens"].as_u64(), Some(7));
        assert_eq!(
            u["server_tool_use"]["web_search_requests"].as_u64(),
            Some(1)
        );
    }

    // ── End-to-end round-trip: ZhipuAI pattern (Task 0 + Task 2) ──

    #[test]
    fn test_roundtrip_zhipuai_input_tokens_from_message_delta() {
        // ZhipuAI sends input_tokens=0 in message_start but the real value in message_delta.
        // After Bug 0b+0c fixes, output_tokens in the SSE must be correct and
        // formatter.usage() must capture input_tokens=60 from message_delta.
        let sse_start = make_sse_block(
            "message_start",
            r#"{"type":"message_start","message":{"id":"msg_z","model":"glm-5","usage":{"input_tokens":0,"output_tokens":0}}}"#,
        );
        let sse_text = make_sse_block(
            "content_block_delta",
            r#"{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}"#,
        );
        let sse_delta = make_sse_block(
            "message_delta",
            r#"{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":60,"output_tokens":43}}"#,
        );

        let mut parser = AnthropicStreamParser::new();
        let mut all_deltas = vec![];
        for chunk in &[sse_start, sse_text, sse_delta] {
            all_deltas.extend(parser.parse_chunk(chunk).unwrap());
        }

        let mut formatter = AnthropicStreamFormatter::new();
        let events = formatter.format_deltas(&all_deltas);

        let delta_ev = events
            .iter()
            .find(|e| e.event.as_deref() == Some("message_delta"))
            .expect("message_delta event must be emitted");
        let delta_json: serde_json::Value = serde_json::from_str(&delta_ev.data).unwrap();
        assert_eq!(
            delta_json["usage"]["output_tokens"].as_u64(),
            Some(43),
            "output_tokens must be 43 (Bug 0b: Usage before Done)",
        );

        // formatter.usage() must reflect input_tokens from message_delta (Bug 0c)
        assert_eq!(
            formatter.usage().prompt_tokens,
            60,
            "prompt_tokens=60 from message_delta must be captured in formatter state",
        );
    }
}
