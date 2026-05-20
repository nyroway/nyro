//! OpenAI Responses API egress encoder (PR-09).
//!
//! PR-09 adds forwarding for:
//! - `background` (bool)
//! - `previous_response_id` (string)
//! - Built-in tools (`web_search_preview`, `file_search`, `computer_use_preview`)
//! - `store` (bool — default true per spec; we default false for privacy)
//! - `include` (array of field paths)
//! - `truncation` (object)
//! - `metadata` / `text` / `reasoning` / `parallel_tool_calls` / `service_tier` / `user`

use anyhow::Result;
use reqwest::header::HeaderMap;
use serde_json::Value;

use crate::protocol::RequestEncoder;
use crate::protocol::ir::AiRequest;
use crate::protocol::ir::request::{Role, ToolChoice};

/// Encoder for the OpenAI Responses API (`POST /v1/responses`).
///
/// Forces `stream: true` because the Responses backend only supports SSE;
/// non-streaming ingress is aggregated downstream in the proxy handler.
pub struct ResponsesEncoder;

// Fields that must NOT be copied blindly from extra into the egress body.
const SKIP_FROM_EXTRA: &[&str] = &["messages", "input", "instructions", "stream", "model"];

impl RequestEncoder for ResponsesEncoder {
    fn encode_request(&self, req: &AiRequest) -> Result<(Value, HeaderMap)> {
        let ingress = &req.meta.vendor.ingress;

        let mut instructions: Vec<String> = Vec::new();
        let mut input: Vec<Value> = Vec::new();

        for message in &req.messages {
            match message.role {
                Role::System => {
                    let text = message.content.to_text();
                    if !text.is_empty() {
                        instructions.push(text);
                    }
                }
                Role::User | Role::Assistant => {
                    let text = message.content.to_text();
                    if !text.is_empty() {
                        let role_str = match message.role {
                            Role::User => "user",
                            _ => "assistant",
                        };
                        let content_type = if message.role == Role::Assistant {
                            "output_text"
                        } else {
                            "input_text"
                        };
                        input.push(serde_json::json!({
                            "type": "message",
                            "role": role_str,
                            "content": [{"type": content_type, "text": text}]
                        }));
                    }
                    if let Some(tool_calls) = &message.tool_calls {
                        for tool_call in tool_calls {
                            input.push(serde_json::json!({
                                "type": "function_call",
                                "call_id": tool_call.id,
                                "name": tool_call.name,
                                "arguments": tool_call.arguments,
                            }));
                        }
                    }
                }
                Role::Tool => {
                    input.push(serde_json::json!({
                        "type": "function_call_output",
                        "call_id": message.tool_call_id.clone().unwrap_or_default(),
                        "output": message.content.to_text(),
                    }));
                }
            }
        }

        if input.is_empty() {
            anyhow::bail!("responses request requires at least one input item");
        }

        let instructions_value = if instructions.is_empty() {
            Value::String("You are a helpful assistant.".to_string())
        } else {
            Value::String(instructions.join("\n\n"))
        };

        // Determine `store` — default false unless the request explicitly set it.
        let store = ingress
            .get("store")
            .and_then(|v| v.as_bool())
            .unwrap_or(false);

        let mut body = serde_json::json!({
            "model": req.model,
            "store": store,
            "stream": true,
            "instructions": instructions_value,
            "input": input,
        });
        let obj = body.as_object_mut().unwrap();

        if let Some(t) = req.generation.temperature {
            obj.insert("temperature".into(), t.into());
        }
        if let Some(p) = req.generation.top_p {
            obj.insert("top_p".into(), p.into());
        }

        // ── Tools (function + built-in) ───────────────────────────────────────
        if let Some(ref tools) = req.tools {
            let tools_val: Vec<Value> = tools
                .iter()
                .map(|t| {
                    if t.name.starts_with("__builtin__") {
                        t.parameters.clone()
                    } else {
                        serde_json::json!({
                            "type": "function",
                            "name": t.name,
                            "description": t.description,
                            "parameters": t.parameters,
                        })
                    }
                })
                .collect();
            obj.insert("tools".into(), Value::Array(tools_val));
        }
        if let Some(ref tc) = req.tool_choice {
            obj.insert("tool_choice".into(), tool_choice_to_value(tc));
        }

        for key in &[
            "background",
            "previous_response_id",
            "include",
            "truncation",
            "metadata",
            "text",
            "reasoning",
            "parallel_tool_calls",
            "service_tier",
            "user",
        ] {
            if let Some(v) = ingress.get(*key) {
                obj.entry(key.to_string()).or_insert_with(|| v.clone());
            }
        }

        // Passthrough remaining unknown extra fields.
        for (k, v) in ingress {
            if SKIP_FROM_EXTRA.contains(&k.as_str()) {
                continue;
            }
            obj.entry(k.clone()).or_insert_with(|| v.clone());
        }

        Ok((body, HeaderMap::new()))
    }

    fn egress_path(&self, _model: &str, _stream: bool) -> String {
        "/v1/responses".to_string()
    }
}

fn tool_choice_to_value(tc: &ToolChoice) -> Value {
    match tc {
        ToolChoice::Auto => Value::String("auto".into()),
        ToolChoice::None => Value::String("none".into()),
        ToolChoice::Required => Value::String("required".into()),
        ToolChoice::Named { name } => serde_json::json!({
            "type": "function",
            "function": {"name": name}
        }),
        ToolChoice::Raw(v) => v.clone(),
    }
}
