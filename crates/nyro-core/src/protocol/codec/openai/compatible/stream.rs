use anyhow::Result;
use serde_json::Value;
use uuid::Uuid;

use crate::protocol::ir::request::ToolCall;
use crate::protocol::ir::usage::Usage;
use crate::protocol::ir::{AiResponse, AiStreamDelta};
use crate::protocol::*;

// ── Non-streaming response parser ──

pub struct OpenAIResponseParser;

impl ResponseDecoder for OpenAIResponseParser {
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

        let choice = resp
            .get("choices")
            .and_then(|c| c.as_array())
            .and_then(|a| a.first());

        let message = choice.and_then(|c| c.get("message"));

        let content = message
            .and_then(|m| m.get("content"))
            .and_then(|c| c.as_str())
            .unwrap_or("")
            .to_string();
        let reasoning_content = message.and_then(extract_reasoning_from_message);

        let stop_reason = choice
            .and_then(|c| c.get("finish_reason"))
            .and_then(|v| v.as_str())
            .map(String::from);

        let tool_calls: Vec<ToolCall> = message
            .and_then(|m| m.get("tool_calls"))
            .and_then(|v| v.as_array())
            .map(|arr| {
                arr.iter()
                    .filter_map(|tc| {
                        let func = tc.get("function")?;
                        Some(ToolCall {
                            id: tc.get("id")?.as_str()?.to_string(),
                            name: func.get("name")?.as_str()?.to_string(),
                            arguments: func
                                .get("arguments")
                                .and_then(|a| a.as_str())
                                .unwrap_or("")
                                .to_string(),
                        })
                    })
                    .collect()
            })
            .unwrap_or_default();

        let usage = extract_usage(&resp);

        let mut ai_resp = AiResponse::new(id, model);
        ai_resp.content = content;
        ai_resp.reasoning_content = reasoning_content;
        ai_resp.tool_calls = tool_calls;
        ai_resp.stop_reason = stop_reason;
        ai_resp.usage = usage;
        Ok(ai_resp)
    }
}

// ── Non-streaming response formatter ──

pub struct OpenAIResponseFormatter;

impl ResponseEncoder for OpenAIResponseFormatter {
    fn format_response(&self, resp: &AiResponse) -> Value {
        let finish_reason = if !resp.tool_calls.is_empty() {
            Some("tool_calls")
        } else {
            resp.stop_reason.as_deref()
        };
        let mut message = serde_json::json!({
            "role": "assistant",
            "content": resp.content,
        });
        if let Some(ref reasoning) = resp.reasoning_content {
            message
                .as_object_mut()
                .unwrap()
                .insert("reasoning_content".into(), Value::String(reasoning.clone()));
        }

        if !resp.tool_calls.is_empty() {
            let tcs: Vec<Value> = resp
                .tool_calls
                .iter()
                .map(|tc| {
                    serde_json::json!({
                        "id": tc.id,
                        "type": "function",
                        "function": {
                            "name": tc.name,
                            "arguments": tc.arguments,
                        }
                    })
                })
                .collect();
            message
                .as_object_mut()
                .unwrap()
                .insert("tool_calls".into(), Value::Array(tcs));
        }

        serde_json::json!({
            "id": resp.id,
            "object": "chat.completion",
            "model": resp.model,
            "choices": [{
                "index": 0,
                "message": message,
                "finish_reason": finish_reason,
            }],
            "usage": {
                "prompt_tokens": resp.usage.prompt_tokens,
                "completion_tokens": resp.usage.completion_tokens,
                "total_tokens": resp.usage.prompt_tokens + resp.usage.completion_tokens,
            }
        })
    }
}

// ── Stream parser (upstream OpenAI SSE → deltas) ──

pub struct OpenAIStreamParser {
    buffer: String,
    started: bool,
    done: bool,
    think_buffer: String,
    in_think_block: bool,
}

impl Default for OpenAIStreamParser {
    fn default() -> Self {
        Self::new()
    }
}

impl OpenAIStreamParser {
    pub fn new() -> Self {
        Self {
            buffer: String::new(),
            started: false,
            done: false,
            think_buffer: String::new(),
            in_think_block: false,
        }
    }
}

impl StreamResponseDecoder for OpenAIStreamParser {
    fn parse_chunk(&mut self, raw: &str) -> Result<Vec<AiStreamDelta>> {
        self.buffer.push_str(raw);
        let mut deltas = Vec::new();

        while let Some(pos) = self.buffer.find("\n\n") {
            let block = self.buffer[..pos].to_string();
            self.buffer = self.buffer[pos + 2..].to_string();

            for line in block.lines() {
                if let Some(data) = line.strip_prefix("data: ") {
                    let data = data.trim();
                    if data == "[DONE]" {
                        if !self.done {
                            self.done = true;
                            deltas.push(AiStreamDelta::Done {
                                stop_reason: "stop".to_string(),
                            });
                        }
                        continue;
                    }
                    if let Ok(chunk) = serde_json::from_str::<Value>(data) {
                        self.parse_openai_chunk(&chunk, &mut deltas);
                    }
                }
            }
        }

        Ok(deltas)
    }

    fn finish(&mut self) -> Result<Vec<AiStreamDelta>> {
        let mut ai_deltas: Vec<AiStreamDelta> = Vec::new();
        if !self.buffer.trim().is_empty() {
            let remaining = std::mem::take(&mut self.buffer);
            ai_deltas.extend(self.parse_chunk(&format!("{remaining}\n\n"))?);
        }
        ai_deltas.extend(self.flush_pending_text());
        Ok(ai_deltas)
    }
}

impl OpenAIStreamParser {
    fn parse_openai_chunk(&mut self, chunk: &Value, deltas: &mut Vec<AiStreamDelta>) {
        if !self.started
            && let (Some(id), Some(model)) = (
                chunk.get("id").and_then(|v| v.as_str()),
                chunk.get("model").and_then(|v| v.as_str()),
            )
        {
            self.started = true;
            deltas.push(AiStreamDelta::MessageStart {
                id: id.to_string(),
                model: model.to_string(),
            });
        }

        let Some(choice) = chunk
            .get("choices")
            .and_then(|v| v.as_array())
            .and_then(|a| a.first())
        else {
            let u = extract_usage(chunk);
            if u.prompt_tokens > 0 || u.completion_tokens > 0 {
                deltas.push(AiStreamDelta::Usage(u));
            }
            return;
        };

        if let Some(delta) = choice.get("delta") {
            if let Some(reasoning) = extract_reasoning_from_message(delta)
                && !reasoning.is_empty()
            {
                deltas.push(AiStreamDelta::ThinkingDelta(reasoning));
            }
            if let Some(text) = delta.get("content").and_then(|v| v.as_str()) {
                self.parse_text_with_think_tags(text, deltas);
            }

            if let Some(tcs) = delta.get("tool_calls").and_then(|v| v.as_array()) {
                for tc in tcs {
                    let idx = tc.get("index").and_then(|v| v.as_u64()).unwrap_or(0) as usize;
                    if let Some(func) = tc.get("function") {
                        if let Some(name) = func.get("name").and_then(|v| v.as_str()) {
                            let id = tc
                                .get("id")
                                .and_then(|v| v.as_str())
                                .unwrap_or("")
                                .to_string();
                            deltas.push(AiStreamDelta::ToolCallStart {
                                index: idx,
                                id,
                                name: name.to_string(),
                            });
                        }
                        if let Some(args) = func.get("arguments").and_then(|v| v.as_str())
                            && !args.is_empty()
                        {
                            deltas.push(AiStreamDelta::ToolCallDelta {
                                index: idx,
                                arguments: args.to_string(),
                            });
                        }
                    }
                }
            }
        }

        if !self.done {
            if let Some(reason) = choice.get("finish_reason").and_then(|v| v.as_str()) {
                if !reason.is_empty() {
                    self.done = true;
                    deltas.push(AiStreamDelta::Done {
                        stop_reason: reason.to_string(),
                    });
                }
            }
        }

        let u = extract_usage(chunk);
        if u.prompt_tokens > 0 || u.completion_tokens > 0 {
            deltas.push(AiStreamDelta::Usage(u));
        }
    }

    fn parse_text_with_think_tags(&mut self, text: &str, deltas: &mut Vec<AiStreamDelta>) {
        if text.is_empty() {
            return;
        }
        self.think_buffer.push_str(text);

        loop {
            if self.in_think_block {
                if let Some(end_idx) = self.think_buffer.find("</think>") {
                    let thought = self.think_buffer[..end_idx].trim().to_string();
                    if !thought.is_empty() {
                        deltas.push(AiStreamDelta::ThinkingDelta(thought));
                    }
                    self.think_buffer = self.think_buffer[end_idx + "</think>".len()..].to_string();
                    self.in_think_block = false;
                    continue;
                }
                break;
            }

            if let Some(start_idx) = self.think_buffer.find("<think>") {
                let before = self.think_buffer[..start_idx].to_string();
                if !before.is_empty() {
                    deltas.push(AiStreamDelta::TextDelta(before));
                }
                self.think_buffer = self.think_buffer[start_idx + "<think>".len()..].to_string();
                self.in_think_block = true;
                continue;
            }

            let keep = longest_suffix_that_can_start_tag(&self.think_buffer, "<think>");
            if self.think_buffer.len() > keep {
                let emit = self.think_buffer[..self.think_buffer.len() - keep].to_string();
                if !emit.is_empty() {
                    deltas.push(AiStreamDelta::TextDelta(emit));
                }
                self.think_buffer = self.think_buffer[self.think_buffer.len() - keep..].to_string();
            }
            break;
        }
    }

    fn flush_pending_text(&mut self) -> Vec<AiStreamDelta> {
        if self.think_buffer.is_empty() {
            return vec![];
        }
        if self.in_think_block {
            let mut fallback = String::from("<think>");
            fallback.push_str(&self.think_buffer);
            self.think_buffer.clear();
            self.in_think_block = false;
            vec![AiStreamDelta::TextDelta(fallback)]
        } else {
            let remaining = std::mem::take(&mut self.think_buffer);
            vec![AiStreamDelta::TextDelta(remaining)]
        }
    }
}

fn longest_suffix_that_can_start_tag(text: &str, tag: &str) -> usize {
    let max = std::cmp::min(text.len(), tag.len().saturating_sub(1));
    for len in (1..=max).rev() {
        if text.ends_with(&tag[..len]) {
            return len;
        }
    }
    0
}

// ── Stream formatter (deltas → OpenAI SSE) ──

pub struct OpenAIStreamFormatter {
    usage: Usage,
    id: String,
    model: String,
    saw_tool_call: bool,
}

impl Default for OpenAIStreamFormatter {
    fn default() -> Self {
        Self::new()
    }
}

impl OpenAIStreamFormatter {
    pub fn new() -> Self {
        Self {
            usage: Usage::default(),
            id: format!("chatcmpl-{}", Uuid::new_v4()),
            model: String::new(),
            saw_tool_call: false,
        }
    }
}

impl StreamResponseEncoder for OpenAIStreamFormatter {
    fn format_deltas(&mut self, deltas: &[AiStreamDelta]) -> Vec<SseEvent> {
        let mut events = Vec::new();
        for delta in deltas {
            match delta {
                AiStreamDelta::MessageStart { id, model } => {
                    self.id = id.clone();
                    self.model = model.clone();
                    let chunk = serde_json::json!({
                        "id": self.id,
                        "object": "chat.completion.chunk",
                        "model": self.model,
                        "choices": [{"index": 0, "delta": {"role": "assistant"}, "finish_reason": null}]
                    });
                    events.push(SseEvent::new(None, chunk.to_string()));
                }
                AiStreamDelta::ThinkingDelta(text) => {
                    let chunk = serde_json::json!({
                        "id": self.id,
                        "object": "chat.completion.chunk",
                        "model": self.model,
                        "choices": [{"index": 0, "delta": {"reasoning_content": text}, "finish_reason": null}]
                    });
                    events.push(SseEvent::new(None, chunk.to_string()));
                }
                AiStreamDelta::ThinkingSignature(_) => {}
                AiStreamDelta::TextDelta(text) => {
                    let chunk = serde_json::json!({
                        "id": self.id,
                        "object": "chat.completion.chunk",
                        "model": self.model,
                        "choices": [{"index": 0, "delta": {"content": text}, "finish_reason": null}]
                    });
                    events.push(SseEvent::new(None, chunk.to_string()));
                }
                AiStreamDelta::ToolCallStart { index, id, name } => {
                    self.saw_tool_call = true;
                    let chunk = serde_json::json!({
                        "id": self.id,
                        "object": "chat.completion.chunk",
                        "model": self.model,
                        "choices": [{"index": 0, "delta": {
                            "tool_calls": [{"index": index, "id": id, "type": "function", "function": {"name": name, "arguments": ""}}]
                        }, "finish_reason": null}]
                    });
                    events.push(SseEvent::new(None, chunk.to_string()));
                }
                AiStreamDelta::ToolCallDelta { index, arguments } => {
                    self.saw_tool_call = true;
                    let chunk = serde_json::json!({
                        "id": self.id,
                        "object": "chat.completion.chunk",
                        "model": self.model,
                        "choices": [{"index": 0, "delta": {
                            "tool_calls": [{"index": index, "function": {"arguments": arguments}}]
                        }, "finish_reason": null}]
                    });
                    events.push(SseEvent::new(None, chunk.to_string()));
                }
                AiStreamDelta::Usage(u) => {
                    self.usage = u.clone();
                }
                AiStreamDelta::Done { stop_reason } => {
                    let final_reason = if self.saw_tool_call {
                        "tool_calls".to_string()
                    } else {
                        stop_reason.clone()
                    };
                    let chunk = serde_json::json!({
                        "id": self.id,
                        "object": "chat.completion.chunk",
                        "model": self.model,
                        "choices": [{"index": 0, "delta": {}, "finish_reason": final_reason}],
                        "usage": {
                            "prompt_tokens": self.usage.prompt_tokens,
                            "completion_tokens": self.usage.completion_tokens,
                            "total_tokens": self.usage.prompt_tokens + self.usage.completion_tokens,
                        }
                    });
                    events.push(SseEvent::new(None, chunk.to_string()));
                    events.push(SseEvent::new(None, "[DONE]"));
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

fn extract_usage(v: &Value) -> Usage {
    let usage = v.get("usage").or_else(|| v.get("usageMetadata"));
    let Some(u) = usage else {
        return Usage::default();
    };

    let input = first_u64(
        u,
        &[
            "prompt_tokens",
            "promptTokenCount",
            "input_tokens",
            "inputTokenCount",
        ],
    )
    .unwrap_or(0);
    let output = first_u64(
        u,
        &[
            "completion_tokens",
            "candidatesTokenCount",
            "output_tokens",
            "outputTokenCount",
        ],
    )
    .unwrap_or(0);

    // Cache-hit tokens are reported by different providers under different
    // keys. Whichever shape we see, surface it under `Usage.cache_read_tokens`
    // so downstream cost analytics stop treating cached prompt tokens as
    // full-price input.
    //
    // - DeepSeek native:  `usage.prompt_cache_hit_tokens`
    // - OpenAI newer fmt: `usage.prompt_tokens_details.cached_tokens`
    // - Gemini-compat:    `usage.cached_content_token_count`
    let cache_read = u
        .get("prompt_cache_hit_tokens")
        .and_then(Value::as_u64)
        .or_else(|| {
            u.get("prompt_tokens_details")
                .and_then(|d| d.get("cached_tokens"))
                .and_then(Value::as_u64)
        })
        .or_else(|| u.get("cached_content_token_count").and_then(Value::as_u64));

    Usage {
        prompt_tokens: input as u32,
        completion_tokens: output as u32,
        cache_read_tokens: cache_read.map(|v| v as u32),
        ..Usage::default()
    }
}

fn first_u64(obj: &Value, keys: &[&str]) -> Option<u64> {
    keys.iter()
        .find_map(|k| obj.get(*k).and_then(|v| v.as_u64()))
}

pub(crate) fn extract_reasoning_from_message(message: &Value) -> Option<String> {
    if let Some(reasoning) = message.get("reasoning_content").and_then(|v| v.as_str()) {
        return Some(reasoning.to_string());
    }
    // Some backends (e.g. mlx-lm) send the field as "reasoning" instead
    // of "reasoning_content".  Accept both.
    if let Some(reasoning) = message.get("reasoning").and_then(|v| v.as_str()) {
        return Some(reasoning.to_string());
    }

    let details = message
        .get("reasoning_details")
        .and_then(|v| v.as_array())?;
    let mut parts: Vec<String> = Vec::new();
    for detail in details {
        if let Some(text) = detail
            .get("text")
            .or_else(|| detail.get("content"))
            .and_then(|v| v.as_str())
            && !text.is_empty()
        {
            parts.push(text.to_string());
        }
    }
    if parts.is_empty() {
        None
    } else {
        Some(parts.join("\n"))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::ir::{AiResponse, AiStreamDelta};
    use crate::protocol::{ResponseDecoder, StreamResponseDecoder};

    fn data_sse(json: &str) -> String {
        format!("data: {json}\n\n")
    }

    // ── extract_usage cache-token variants ──

    #[test]
    fn extract_usage_deepseek_prompt_cache_hit_tokens() {
        let resp = serde_json::json!({
            "usage": {
                "prompt_tokens": 1000,
                "completion_tokens": 50,
                "prompt_cache_hit_tokens": 800,
                "prompt_cache_miss_tokens": 200
            }
        });
        let u = extract_usage(&resp);
        assert_eq!(u.prompt_tokens, 1000);
        assert_eq!(u.completion_tokens, 50);
        assert_eq!(u.cache_read_tokens, Some(800));
    }

    #[test]
    fn extract_usage_openai_prompt_tokens_details_cached() {
        let resp = serde_json::json!({
            "usage": {
                "prompt_tokens": 1500,
                "completion_tokens": 100,
                "prompt_tokens_details": { "cached_tokens": 1200 }
            }
        });
        let u = extract_usage(&resp);
        assert_eq!(u.prompt_tokens, 1500);
        assert_eq!(u.cache_read_tokens, Some(1200));
    }

    #[test]
    fn extract_usage_gemini_cached_content_token_count() {
        let resp = serde_json::json!({
            "usage": {
                "prompt_tokens": 2000,
                "completion_tokens": 200,
                "cached_content_token_count": 1700
            }
        });
        let u = extract_usage(&resp);
        assert_eq!(u.cache_read_tokens, Some(1700));
    }

    #[test]
    fn extract_usage_no_cache_field_yields_none() {
        let resp = serde_json::json!({
            "usage": {
                "prompt_tokens": 500,
                "completion_tokens": 50
            }
        });
        let u = extract_usage(&resp);
        assert_eq!(u.prompt_tokens, 500);
        assert_eq!(u.cache_read_tokens, None);
    }

    // ── OpenAIResponseParser ──

    #[test]
    fn test_parse_response_basic() {
        let resp = serde_json::json!({
            "id": "chatcmpl-1",
            "model": "gpt-4o",
            "choices": [{"index": 0, "message": {"role": "assistant", "content": "hi"}, "finish_reason": "stop"}],
            "usage": {"prompt_tokens": 5, "completion_tokens": 2, "total_tokens": 7}
        });
        let r = OpenAIResponseParser.parse_response(resp).unwrap();
        assert_eq!(r.content, "hi");
        assert_eq!(r.stop_reason.as_deref(), Some("stop"));
        assert_eq!(r.usage.prompt_tokens, 5);
        assert_eq!(r.usage.completion_tokens, 2);
    }

    #[test]
    fn test_parse_response_with_reasoning_content() {
        let resp = serde_json::json!({
            "id": "chatcmpl-2",
            "model": "deepseek-r1",
            "choices": [{
                "index": 0,
                "message": {
                    "role": "assistant",
                    "content": "answer",
                    "reasoning_content": "my reasoning"
                },
                "finish_reason": "stop"
            }],
            "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
        });
        let r = OpenAIResponseParser.parse_response(resp).unwrap();
        assert_eq!(r.content, "answer");
        assert_eq!(r.reasoning_content.as_deref(), Some("my reasoning"));
    }

    // ── OpenAIStreamParser – tool call streaming ──

    #[test]
    fn test_stream_tool_call_fragments() {
        // First chunk carries id + name with empty arguments.
        // Subsequent chunks carry only argument fragments (no id).
        let chunks = [
            data_sse(r#"{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}"#),
            data_sse(r#"{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_abc","type":"function","function":{"name":"get_weather","arguments":""}}]},"finish_reason":null}]}"#),
            data_sse(r#"{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cit"}}]},"finish_reason":null}]}"#),
            data_sse(r#"{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"y\":\"Paris\"}"}}]},"finish_reason":null}]}"#),
            data_sse(r#"{"id":"chatcmpl-1","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}"#),
            data_sse("[DONE]"),
        ]
        .concat();

        let mut parser = OpenAIStreamParser::new();
        let deltas = parser.parse_chunk(&chunks).unwrap();

        let has_tool_start = deltas
            .iter()
            .any(|d| matches!(d, AiStreamDelta::ToolCallStart { id, name, .. } if id == "call_abc" && name == "get_weather"));
        assert!(
            has_tool_start,
            "expected ToolCallStart with id+name, got: {deltas:?}"
        );

        let args: String = deltas
            .iter()
            .filter_map(|d| {
                if let AiStreamDelta::ToolCallDelta { arguments, .. } = d {
                    Some(arguments.as_str())
                } else {
                    None
                }
            })
            .collect();
        assert!(
            args.contains("Paris"),
            "tool call arguments fragments not accumulated: {args}"
        );
    }

    #[test]
    fn test_stream_think_tags_across_chunks() {
        // <think> and </think> may span chunk boundaries.
        let chunks = [
            data_sse(r#"{"id":"chatcmpl-2","model":"qwen3","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}"#),
            data_sse(r#"{"id":"chatcmpl-2","model":"qwen3","choices":[{"index":0,"delta":{"content":"<think>rea"},"finish_reason":null}]}"#),
            data_sse(r#"{"id":"chatcmpl-2","model":"qwen3","choices":[{"index":0,"delta":{"content":"soning</think>answer"},"finish_reason":null}]}"#),
            data_sse(r#"{"id":"chatcmpl-2","model":"qwen3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}"#),
            data_sse("[DONE]"),
        ]
        .concat();

        let mut parser = OpenAIStreamParser::new();
        let mut deltas = parser.parse_chunk(&chunks).unwrap();
        deltas.extend(parser.finish().unwrap());

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
        let full_reasoning = reasoning.concat();
        assert!(
            full_reasoning.contains("reasoning"),
            "expected reasoning content in ThinkingDelta, got: {full_reasoning}"
        );

        let text: Vec<_> = deltas
            .iter()
            .filter_map(|d| {
                if let AiStreamDelta::TextDelta(t) = d {
                    Some(t.as_str())
                } else {
                    None
                }
            })
            .collect();
        assert!(
            text.iter().any(|t| t.contains("answer")),
            "expected 'answer' in TextDelta, got: {text:?}"
        );
    }

    #[test]
    fn test_stream_no_think_tags() {
        let chunks = [
            data_sse(r#"{"id":"chatcmpl-3","model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}"#),
            data_sse(r#"{"id":"chatcmpl-3","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}"#),
            data_sse(r#"{"id":"chatcmpl-3","model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}"#),
            data_sse("[DONE]"),
        ]
        .concat();

        let mut parser = OpenAIStreamParser::new();
        let mut deltas = parser.parse_chunk(&chunks).unwrap();
        deltas.extend(parser.finish().unwrap());

        let has_text = deltas
            .iter()
            .any(|d| matches!(d, AiStreamDelta::TextDelta(t) if t.contains("hello")));
        let has_reasoning = deltas
            .iter()
            .any(|d| matches!(d, AiStreamDelta::ThinkingDelta(_)));
        assert!(has_text, "expected TextDelta('hello'), got: {deltas:?}");
        assert!(
            !has_reasoning,
            "should not have ThinkingDelta when no think tags, got: {deltas:?}"
        );
    }
    #[test]
    fn test_extract_reasoning_mlx_field_name() {
        // mlx-lm uses "reasoning" instead of "reasoning_content".
        // Both field names must produce a reasoning delta.
        let msg = serde_json::json!({"role": "assistant", "content": "answer", "reasoning": "my reasoning"});
        let extracted = extract_reasoning_from_message(&msg);
        assert_eq!(
            extracted.as_deref(),
            Some("my reasoning"),
            "extract_reasoning_from_message must accept 'reasoning' field name (mlx-lm compat)"
        );
    }

    #[test]
    fn test_parse_response_with_reasoning_field() {
        // Non-streaming response from mlx-lm: message has "reasoning" not "reasoning_content".
        let resp = serde_json::json!({
            "id": "chatcmpl-mlx",
            "model": "qwen3-35b",
            "choices": [{
                "index": 0,
                "message": {
                    "role": "assistant",
                    "content": "final answer",
                    "reasoning": "step by step thinking"
                },
                "finish_reason": "stop"
            }],
            "usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
        });
        let r = OpenAIResponseParser.parse_response(resp).unwrap();
        assert_eq!(r.content, "final answer");
        assert_eq!(
            r.reasoning_content.as_deref(),
            Some("step by step thinking"),
            "parse_response must extract reasoning from 'reasoning' field (mlx-lm compat)"
        );
    }

    #[test]
    fn test_format_response_includes_reasoning_content() {
        // The response formatter must emit reasoning_content when it is present.
        let mut internal = AiResponse::new("chatcmpl-test", "qwen3");
        internal.content = "visible text".to_string();
        internal.reasoning_content = Some("hidden chain of thought".to_string());
        internal.stop_reason = Some("stop".to_string());
        internal.usage = Usage {
            prompt_tokens: 10,
            completion_tokens: 5,
            ..Usage::default()
        };
        let formatted = OpenAIResponseFormatter.format_response(&internal);
        let msg = &formatted["choices"][0]["message"];
        assert_eq!(msg["content"].as_str(), Some("visible text"));
        assert_eq!(
            msg["reasoning_content"].as_str(),
            Some("hidden chain of thought"),
            "format_response must include reasoning_content in the message"
        );
    }

    #[test]
    fn test_stream_reasoning_field_from_mlx() {
        // Streaming SSE chunks from mlx-lm use "reasoning" in the delta.
        let chunks = [
            data_sse(r#"{"id":"chatcmpl-mlx","model":"qwen3","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}"#),
            data_sse(r#"{"id":"chatcmpl-mlx","model":"qwen3","choices":[{"index":0,"delta":{"content":"final ","reasoning":"thinking"},"finish_reason":null}]}"#),
            data_sse(r#"{"id":"chatcmpl-mlx","model":"qwen3","choices":[{"index":0,"delta":{"content":"answer","reasoning":" done"},"finish_reason":null}]}"#),
            data_sse(r#"{"id":"chatcmpl-mlx","model":"qwen3","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}"#),
            data_sse("[DONE]"),
        ]
        .concat();

        let mut parser = OpenAIStreamParser::new();
        let mut deltas = parser.parse_chunk(&chunks).unwrap();
        deltas.extend(parser.finish().unwrap());

        let reasoning: String = deltas
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
            reasoning.contains("thinking"),
            "expected 'thinking' in ThinkingDelta, got: {reasoning}"
        );
        assert!(
            reasoning.contains("done"),
            "expected 'done' in ThinkingDelta, got: {reasoning}"
        );

        let text: String = deltas
            .iter()
            .filter_map(|d| {
                if let AiStreamDelta::TextDelta(t) = d {
                    Some(t.as_str())
                } else {
                    None
                }
            })
            .collect();
        assert!(
            text.contains("final answer"),
            "expected 'final answer' in TextDelta, got: {text:?}"
        );
    }

    #[test]
    fn test_stream_finish_reason_empty_string_ignored() {
        let chunks = [
            data_sse(r#"{"id":"chatcmpl-zh","model":"claude-opus-4p7","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":""}]}"#),
            data_sse(r#"{"id":"chatcmpl-zh","model":"claude-opus-4p7","choices":[{"index":0,"delta":{"content":"Hello!"},"finish_reason":""}]}"#),
            data_sse(r#"{"id":"chatcmpl-zh","model":"claude-opus-4p7","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}"#),
            data_sse("[DONE]"),
        ]
        .concat();

        let mut parser = OpenAIStreamParser::new();
        let deltas = parser.parse_chunk(&chunks).unwrap();

        let done_count = deltas
            .iter()
            .filter(|d| matches!(d, AiStreamDelta::Done { .. }))
            .count();
        assert_eq!(
            done_count, 1,
            "expected exactly 1 Done, got {done_count}: {deltas:?}"
        );

        let done = deltas
            .iter()
            .find_map(|d| {
                if let AiStreamDelta::Done { stop_reason } = d {
                    Some(stop_reason.clone())
                } else {
                    None
                }
            })
            .unwrap();
        assert_eq!(done, "stop", "Done stop_reason must be 'stop'");
    }

    #[test]
    fn test_stream_duplicate_done_only_one_emitted() {
        let chunks = [
            data_sse(r#"{"id":"chatcmpl-mi","model":"mimo-v2.5","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}"#),
            data_sse(r#"{"id":"chatcmpl-mi","model":"mimo-v2.5","choices":[{"index":0,"delta":{"content":"Hi"},"finish_reason":null}]}"#),
            data_sse(r#"{"id":"chatcmpl-mi","model":"mimo-v2.5","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}"#),
            data_sse("[DONE]"),
        ]
        .concat();

        let mut parser = OpenAIStreamParser::new();
        let deltas = parser.parse_chunk(&chunks).unwrap();

        let done_count = deltas
            .iter()
            .filter(|d| matches!(d, AiStreamDelta::Done { .. }))
            .count();
        assert_eq!(
            done_count, 1,
            "expected exactly 1 Done (finish_reason + [DONE] deduped), got {done_count}: {deltas:?}"
        );
    }
}
