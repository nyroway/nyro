use anyhow::Result;
use serde_json::Value;

use crate::protocol::ir::request::ToolCall;
use crate::protocol::ir::usage::Usage;
use crate::protocol::ir::{AiResponse, AiStreamDelta};
use crate::protocol::{ResponseDecoder, StreamResponseDecoder};

pub struct ResponsesResponseParser;

impl ResponseDecoder for ResponsesResponseParser {
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
        let stop_reason = resp
            .get("status")
            .and_then(|v| v.as_str())
            .map(str::to_string);

        let mut content = String::new();
        let mut tool_calls = Vec::new();

        if let Some(items) = resp.get("output").and_then(|v| v.as_array()) {
            for item in items {
                match item.get("type").and_then(|v| v.as_str()).unwrap_or("") {
                    "message" => {
                        if let Some(blocks) = item.get("content").and_then(|v| v.as_array()) {
                            for block in blocks {
                                if matches!(
                                    block.get("type").and_then(|v| v.as_str()),
                                    Some("output_text" | "text")
                                ) && let Some(text) = block.get("text").and_then(|v| v.as_str())
                                {
                                    content.push_str(text);
                                }
                            }
                        }
                    }
                    "function_call" => {
                        let call_id = item
                            .get("call_id")
                            .or_else(|| item.get("id"))
                            .and_then(|v| v.as_str())
                            .unwrap_or("")
                            .to_string();
                        let name = item
                            .get("name")
                            .and_then(|v| v.as_str())
                            .unwrap_or("")
                            .to_string();
                        let arguments = item
                            .get("arguments")
                            .and_then(|v| v.as_str())
                            .unwrap_or("{}")
                            .to_string();
                        if !call_id.is_empty() && !name.is_empty() {
                            tool_calls.push(ToolCall {
                                id: call_id,
                                name,
                                arguments,
                            });
                        }
                    }
                    _ => {}
                }
            }
        }

        let usage = Usage {
            prompt_tokens: resp
                .get("usage")
                .and_then(|v| v.get("input_tokens"))
                .and_then(|v| v.as_u64())
                .unwrap_or(0) as u32,
            completion_tokens: resp
                .get("usage")
                .and_then(|v| v.get("output_tokens"))
                .and_then(|v| v.as_u64())
                .unwrap_or(0) as u32,
            ..Usage::default()
        };

        let mut ai_resp = AiResponse::new(id, model);
        ai_resp.content = content;
        ai_resp.tool_calls = tool_calls;
        ai_resp.stop_reason = stop_reason;
        ai_resp.usage = usage;
        Ok(ai_resp)
    }
}

pub struct ResponsesStreamParser {
    buffer: String,
    started: bool,
}

impl Default for ResponsesStreamParser {
    fn default() -> Self {
        Self::new()
    }
}

impl ResponsesStreamParser {
    pub fn new() -> Self {
        Self {
            buffer: String::new(),
            started: false,
        }
    }
}

impl StreamResponseDecoder for ResponsesStreamParser {
    fn parse_chunk(&mut self, raw: &str) -> Result<Vec<AiStreamDelta>> {
        self.buffer.push_str(raw);
        let mut deltas = Vec::new();

        while let Some(pos) = self.buffer.find("\n\n") {
            let block = self.buffer[..pos].to_string();
            self.buffer = self.buffer[pos + 2..].to_string();

            let mut event_name: Option<String> = None;
            for line in block.lines() {
                if let Some(event) = line.strip_prefix("event: ") {
                    event_name = Some(event.trim().to_string());
                    continue;
                }
                let Some(data) = line.strip_prefix("data: ") else {
                    continue;
                };
                let data = data.trim();
                if data == "[DONE]" {
                    deltas.push(AiStreamDelta::Done {
                        stop_reason: "stop".to_string(),
                    });
                    continue;
                }
                let Ok(payload) = serde_json::from_str::<Value>(data) else {
                    continue;
                };
                self.parse_event(event_name.as_deref(), &payload, &mut deltas);
            }
        }

        Ok(deltas)
    }

    fn finish(&mut self) -> Result<Vec<AiStreamDelta>> {
        if self.buffer.trim().is_empty() {
            return Ok(Vec::new());
        }
        let remaining = std::mem::take(&mut self.buffer);
        self.parse_chunk(&format!("{remaining}\n\n"))
    }
}

impl ResponsesStreamParser {
    fn parse_event(
        &mut self,
        event: Option<&str>,
        payload: &Value,
        deltas: &mut Vec<AiStreamDelta>,
    ) {
        match event.unwrap_or("") {
            "response.created" | "response.in_progress" => {
                if self.started {
                    return;
                }
                let response = payload.get("response").unwrap_or(payload);
                let id = response
                    .get("id")
                    .and_then(|v| v.as_str())
                    .unwrap_or("")
                    .to_string();
                let model = response
                    .get("model")
                    .and_then(|v| v.as_str())
                    .unwrap_or("")
                    .to_string();
                if !id.is_empty() || !model.is_empty() {
                    self.started = true;
                    deltas.push(AiStreamDelta::MessageStart { id, model });
                }
            }
            "response.output_text.delta" => {
                if let Some(text) = payload.get("delta").and_then(|v| v.as_str())
                    && !text.is_empty()
                {
                    deltas.push(AiStreamDelta::TextDelta(text.to_string()));
                }
            }
            "response.reasoning_summary_text.delta" => {
                // Emitted by Ollama's Responses API when the model includes reasoning.
                // Must be handled independently from response.output_text.delta —
                // they carry semantically different content (reasoning vs answer text).
                if let Some(text) = payload.get("delta").and_then(|v| v.as_str())
                    && !text.is_empty()
                {
                    deltas.push(AiStreamDelta::ThinkingDelta(text.to_string()));
                }
            }
            "response.function_call_arguments.delta" => {
                let index = payload
                    .get("output_index")
                    .and_then(|v| v.as_u64())
                    .unwrap_or(0) as usize;
                if let Some(arguments) = payload.get("delta").and_then(|v| v.as_str())
                    && !arguments.is_empty()
                {
                    deltas.push(AiStreamDelta::ToolCallDelta {
                        index,
                        arguments: arguments.to_string(),
                    });
                }
            }
            "response.output_item.added" | "response.output_item.done" => {
                let index = payload
                    .get("output_index")
                    .and_then(|v| v.as_u64())
                    .unwrap_or(0) as usize;
                let item = payload.get("item").unwrap_or(payload);
                if item.get("type").and_then(|v| v.as_str()) == Some("function_call") {
                    let id = item
                        .get("call_id")
                        .or_else(|| item.get("id"))
                        .and_then(|v| v.as_str())
                        .unwrap_or("")
                        .to_string();
                    let name = item
                        .get("name")
                        .and_then(|v| v.as_str())
                        .unwrap_or("")
                        .to_string();
                    if !id.is_empty() && !name.is_empty() {
                        deltas.push(AiStreamDelta::ToolCallStart { index, id, name });
                    }
                }
            }
            "response.completed" => {
                let response = payload.get("response").unwrap_or(payload);
                let usage = Usage {
                    prompt_tokens: response
                        .get("usage")
                        .and_then(|v| v.get("input_tokens"))
                        .and_then(|v| v.as_u64())
                        .unwrap_or(0) as u32,
                    completion_tokens: response
                        .get("usage")
                        .and_then(|v| v.get("output_tokens"))
                        .and_then(|v| v.as_u64())
                        .unwrap_or(0) as u32,
                    ..Usage::default()
                };
                if usage.prompt_tokens > 0 || usage.completion_tokens > 0 {
                    deltas.push(AiStreamDelta::Usage(usage));
                }
                deltas.push(AiStreamDelta::Done {
                    stop_reason: response
                        .get("status")
                        .and_then(|v| v.as_str())
                        .unwrap_or("completed")
                        .to_string(),
                });
            }
            _ => {}
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::ir::AiStreamDelta;
    use crate::protocol::{ResponseDecoder, StreamResponseDecoder};

    fn sse_event(event: &str, data: &str) -> String {
        format!("event: {event}\ndata: {data}\n\n")
    }

    fn sse_data(data: &str) -> String {
        format!("data: {data}\n\n")
    }

    // ── ResponsesResponseParser ──

    #[test]
    fn test_parse_response_message_output() {
        let resp = serde_json::json!({
            "id": "resp_1",
            "model": "gpt-4o",
            "status": "completed",
            "output": [
                {
                    "type": "message",
                    "id": "msg_1",
                    "status": "completed",
                    "role": "assistant",
                    "content": [{"type": "output_text", "text": "hello"}]
                }
            ],
            "usage": {"input_tokens": 5, "output_tokens": 3}
        });
        let r = ResponsesResponseParser.parse_response(resp).unwrap();
        assert_eq!(r.content, "hello");
        assert_eq!(r.stop_reason.as_deref(), Some("completed"));
        assert_eq!(r.usage.prompt_tokens, 5);
    }

    #[test]
    fn test_parse_response_with_encrypted_content_plaintext() {
        // Ollama's Responses API returns reasoning as plaintext in encrypted_content field.
        // The parser must not fail, and should extract text from the content array.
        let resp = serde_json::json!({
            "id": "resp_2",
            "model": "qwen3",
            "status": "completed",
            "output": [
                {
                    "type": "reasoning",
                    "id": "rs_1",
                    "summary": [{"type": "summary_text", "text": "thinking..."}],
                    // encrypted_content is plaintext in Ollama — parser must not crash
                    "encrypted_content": "plaintext-not-base64"
                },
                {
                    "type": "message",
                    "id": "msg_1",
                    "status": "completed",
                    "role": "assistant",
                    "content": [{"type": "output_text", "text": "answer"}]
                }
            ],
            "usage": {"input_tokens": 10, "output_tokens": 20}
        });
        let result = ResponsesResponseParser.parse_response(resp);
        assert!(
            result.is_ok(),
            "parser must not fail on plaintext encrypted_content"
        );
        let r = result.unwrap();
        assert_eq!(r.content, "answer");
    }

    #[test]
    fn test_parse_response_function_call_output() {
        let resp = serde_json::json!({
            "id": "resp_3",
            "model": "gpt-4o",
            "status": "completed",
            "output": [
                {
                    "type": "function_call",
                    "id": "fc_1",
                    "call_id": "call_abc",
                    "name": "get_weather",
                    "arguments": "{\"city\":\"Paris\"}"
                }
            ],
            "usage": {"input_tokens": 15, "output_tokens": 10}
        });
        let r = ResponsesResponseParser.parse_response(resp).unwrap();
        assert_eq!(r.tool_calls.len(), 1);
        assert_eq!(r.tool_calls[0].id, "call_abc");
        assert_eq!(r.tool_calls[0].name, "get_weather");
    }

    // ── ResponsesStreamParser ──

    #[test]
    fn test_stream_output_text_delta() {
        let sse = [
            sse_event(
                "response.created",
                r#"{"type":"response.created","response":{"id":"resp_1","model":"gpt-4o","status":"in_progress"}}"#,
            ),
            sse_event(
                "response.output_text.delta",
                r#"{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hello"}"#,
            ),
            sse_event(
                "response.completed",
                r#"{"type":"response.completed","response":{"id":"resp_1","model":"gpt-4o","status":"completed","output":[],"usage":{"input_tokens":5,"output_tokens":3}}}"#,
            ),
        ]
        .concat();

        let mut parser = ResponsesStreamParser::new();
        let deltas = parser.parse_chunk(&sse).unwrap();

        let has_text = deltas
            .iter()
            .any(|d| matches!(d, AiStreamDelta::TextDelta(t) if t == "hello"));
        let has_done = deltas
            .iter()
            .any(|d| matches!(d, AiStreamDelta::Done { .. }));
        assert!(has_text, "expected TextDelta('hello'), got: {deltas:?}");
        assert!(has_done, "expected Done, got: {deltas:?}");
    }

    #[test]
    fn test_stream_reasoning_summary_text_delta() {
        // Ollama sends response.reasoning_summary_text.delta for reasoning content.
        // This event must be handled independently and produce ReasoningDelta — NOT TextDelta.
        let sse = [
            sse_event(
                "response.created",
                r#"{"type":"response.created","response":{"id":"resp_2","model":"qwen3","status":"in_progress"}}"#,
            ),
            sse_event(
                "response.reasoning_summary_text.delta",
                r#"{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":1,"summary_index":0,"delta":"thinking step"}"#,
            ),
            sse_event(
                "response.output_text.delta",
                r#"{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"answer text"}"#,
            ),
            sse_event(
                "response.completed",
                r#"{"type":"response.completed","response":{"id":"resp_2","model":"qwen3","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":20}}}"#,
            ),
        ]
        .concat();

        let mut parser = ResponsesStreamParser::new();
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
            reasoning.contains(&"thinking step"),
            "response.reasoning_summary_text.delta must produce ThinkingDelta, got: {deltas:?}"
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
            text.contains(&"answer text"),
            "response.output_text.delta must produce TextDelta, got: {deltas:?}"
        );
    }

    #[test]
    fn test_stream_done_sentinel() {
        let sse = sse_data("[DONE]");
        let mut parser = ResponsesStreamParser::new();
        let deltas = parser.parse_chunk(&sse).unwrap();
        let has_done = deltas
            .iter()
            .any(|d| matches!(d, AiStreamDelta::Done { .. }));
        assert!(
            has_done,
            "expected Done on [DONE] sentinel, got: {deltas:?}"
        );
    }
}
