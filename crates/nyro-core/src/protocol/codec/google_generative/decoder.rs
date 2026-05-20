//! Google Generative AI ingress decoder — produces `AiRequest` directly.
//!
//! `decode_with_model` accepts the model and stream flag extracted from the URL
//! path by the ingress shell handler, since Google embeds the model in the path
//! rather than the request body.

use anyhow::Result;
use serde_json::Value;

use crate::protocol::RequestDecoder;
use crate::protocol::ids::GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA;
use crate::protocol::ir::{
    AiRequest, ContentBlock, GenerationConfig, GoogleExt, MediaSource, Message, MessageContent,
    ProtocolExt, ReasoningConfig, Role, SafetySettings, StreamConfig, ToolCall, ToolSpec,
};

use super::types::*;

pub struct GoogleDecoder;

impl GoogleDecoder {
    pub fn decode_with_model(&self, body: Value, model: &str, stream: bool) -> Result<AiRequest> {
        let req: GoogleRequest = serde_json::from_value(body)?;

        // ── System instruction ────────────────────────────────────────────────
        let needs_raw_system = req.system_instruction.as_ref().is_some_and(|si| {
            si.parts.len() > 1
                || si
                    .parts
                    .iter()
                    .any(|p| !matches!(p, GooglePart::Text { .. }))
        });
        let raw_system: Option<Value> = if needs_raw_system {
            req.system_instruction
                .as_ref()
                .and_then(|s| serde_json::to_value(s).ok())
        } else {
            None
        };

        // ── Tools: detect built-ins ───────────────────────────────────────────
        let has_builtin_tools = req.tools.as_ref().is_some_and(|ts| {
            ts.iter().any(|t| {
                t.google_search.is_some()
                    || t.code_execution.is_some()
                    || t.google_search_retrieval.is_some()
            })
        });
        let raw_tools: Option<Value> = if has_builtin_tools {
            req.tools
                .as_ref()
                .and_then(|t| serde_json::to_value(t).ok())
        } else {
            None
        };

        // ── Messages ──────────────────────────────────────────────────────────
        let mut messages: Vec<Message> = Vec::new();

        // System message from system_instruction
        if let Some(si) = &req.system_instruction {
            let text: String = si
                .parts
                .iter()
                .filter_map(|p| match p {
                    GooglePart::Text { text } => Some(text.as_str()),
                    _ => None,
                })
                .collect::<Vec<_>>()
                .join("\n");
            if !text.is_empty() {
                messages.push(Message {
                    role: Role::System,
                    content: MessageContent::Text(text),
                    tool_calls: None,
                    tool_call_id: None,
                    meta: None,
                });
            }
        }

        for content in req.contents {
            messages.push(decode_content(content)?);
        }

        // ── Tools ─────────────────────────────────────────────────────────────
        let tools = req.tools.as_ref().map(|entries| {
            let mut defs: Vec<ToolSpec> = Vec::new();
            for entry in entries {
                if let Some(decls) = &entry.function_declarations {
                    for fd in decls {
                        defs.push(ToolSpec {
                            name: fd.name.clone(),
                            description: fd.description.clone(),
                            parameters: fd
                                .parameters
                                .clone()
                                .unwrap_or(Value::Object(Default::default())),
                            strict: None,
                            cache_control: None,
                            meta: None,
                        });
                    }
                }
                if entry.google_search.is_some() {
                    defs.push(ToolSpec {
                        name: "__builtin__google_search".into(),
                        description: None,
                        parameters: Value::Object(Default::default()),
                        strict: None,
                        cache_control: None,
                        meta: None,
                    });
                }
                if entry.code_execution.is_some() {
                    defs.push(ToolSpec {
                        name: "__builtin__code_execution".into(),
                        description: None,
                        parameters: Value::Object(Default::default()),
                        strict: None,
                        cache_control: None,
                        meta: None,
                    });
                }
                if entry.google_search_retrieval.is_some() {
                    defs.push(ToolSpec {
                        name: "__builtin__google_search_retrieval".into(),
                        description: None,
                        parameters: Value::Object(Default::default()),
                        strict: None,
                        cache_control: None,
                        meta: None,
                    });
                }
            }
            defs
        });

        // ── generationConfig → IR fields + GoogleExt ──────────────────────────
        let gc = req.generation_config.as_ref();
        let max_tokens = gc.and_then(|c| c.max_output_tokens);
        let temperature = gc.and_then(|c| c.temperature);
        let top_p = gc.and_then(|c| c.top_p);
        let stop = gc.and_then(|c| c.stop_sequences.clone());
        let seed = gc.and_then(|c| c.seed.map(|s| s as i64));
        let frequency_penalty = gc.and_then(|c| c.frequency_penalty);
        let presence_penalty = gc.and_then(|c| c.presence_penalty);

        // Reasoning from thinkingConfig
        let reasoning = if let Some(tc) = gc.and_then(|c| c.thinking_config.as_ref()) {
            let budget = tc
                .get("thinkingBudget")
                .and_then(|v| v.as_u64())
                .map(|v| v as u32);
            ReasoningConfig {
                enabled: budget.map(|b| b > 0).unwrap_or(false),
                budget_tokens: budget,
                ..Default::default()
            }
        } else {
            ReasoningConfig::default()
        };

        // Safety settings
        let safety_settings = req.safety_settings.as_ref().map(|ss| {
            ss.iter()
                .map(|s| SafetySettings {
                    category: s.category.clone(),
                    threshold: s.threshold.clone(),
                })
                .collect()
        });

        // GoogleExt
        let google_ext = GoogleExt {
            top_k: gc.and_then(|c| c.top_k.map(|v| v as u32)),
            candidate_count: gc.and_then(|c| c.candidate_count),
            response_logprobs: gc.and_then(|c| c.response_logprobs),
            logprobs: gc.and_then(|c| c.logprobs.map(|v| v as u32)),
            response_mime_type: gc.and_then(|c| c.response_mime_type.clone()),
            response_json_schema: gc.and_then(|c| c.response_schema.clone()),
            tool_config: req.tool_config.clone(),
            cached_content: req.cached_content.clone(),
            thinking_config: gc.and_then(|c| c.thinking_config.clone()),
            ..Default::default()
        };

        // ── Vendor ingress bag — backward compat for old Google encoder ────────
        let mut ingress = std::collections::HashMap::new();

        if let Some(ref gen_cfg) = req.generation_config
            && let Ok(v) = serde_json::to_value(gen_cfg)
        {
            ingress.insert("__google_generation_config".into(), v);
        }
        if let Some(v) = raw_system {
            ingress.insert("__google_raw_system_instruction".into(), v);
        }
        if let Some(v) = raw_tools {
            ingress.insert("__google_raw_tools".into(), v);
        }
        if let Some(ref ss) = req.safety_settings
            && let Ok(v) = serde_json::to_value(ss)
        {
            ingress.insert("__google_safety_settings".into(), v);
        }
        if let Some(ref tc) = req.tool_config {
            ingress.insert("__google_tool_config".into(), tc.clone());
        }
        if let Some(ref cc) = req.cached_content {
            ingress.insert("__google_cached_content".into(), Value::String(cc.clone()));
        }

        // ── Build AiRequest ───────────────────────────────────────────────────
        let tools_opt = tools.filter(|t| !t.is_empty());

        let mut ai_req = AiRequest::new(model.to_string(), messages);
        ai_req.generation = GenerationConfig {
            temperature,
            max_tokens,
            top_p,
            seed,
            stop,
            frequency_penalty,
            presence_penalty,
            ..Default::default()
        };
        ai_req.stream = StreamConfig {
            enabled: stream,
            include_usage: false,
        };
        ai_req.tools = tools_opt;
        ai_req.reasoning = reasoning;
        ai_req.safety_settings = safety_settings;
        ai_req.ext = Some(ProtocolExt::Google(google_ext));
        ai_req.meta.source_protocol = Some(GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA);
        ai_req.meta.vendor.ingress = ingress;

        Ok(ai_req)
    }
}

impl RequestDecoder for GoogleDecoder {
    fn decode_request(&self, body: Value) -> Result<AiRequest> {
        self.decode_with_model(body, "gemini-2.0-flash", false)
    }
}

// ── Content decoding ──────────────────────────────────────────────────────────

fn decode_content(content: GoogleContent) -> Result<Message> {
    let mut role = match content.role.as_deref() {
        Some("user") | None => Role::User,
        Some("model") => Role::Assistant,
        Some(other) => anyhow::bail!("unknown Gemini role: {other}"),
    };

    let mut text_parts: Vec<String> = Vec::new();
    let mut blocks: Vec<ContentBlock> = Vec::new();
    let mut tool_calls: Vec<ToolCall> = Vec::new();
    let mut has_function_response = false;

    for part in content.parts {
        match part {
            GooglePart::Text { text } => {
                text_parts.push(text.clone());
                blocks.push(ContentBlock::Text {
                    text,
                    cache_control: None,
                });
            }
            GooglePart::InlineData { inline_data } => {
                blocks.push(ContentBlock::Image {
                    source: MediaSource::Base64 {
                        media_type: inline_data.mime_type,
                        data: inline_data.data,
                    },
                    cache_control: None,
                });
            }
            GooglePart::FileData { file_data } => {
                let mime = file_data.mime_type.unwrap_or_default();
                let source = if mime.starts_with("image/") || mime.is_empty() {
                    MediaSource::Url(file_data.file_uri)
                } else {
                    MediaSource::Url(file_data.file_uri)
                };
                blocks.push(ContentBlock::File { source });
            }
            GooglePart::FunctionCall { function_call } => {
                let id = format!("call_{}", uuid::Uuid::new_v4().simple());
                tool_calls.push(ToolCall {
                    id: id.clone(),
                    name: function_call.name.clone(),
                    arguments: function_call.args.to_string(),
                });
                blocks.push(ContentBlock::ToolUse {
                    id,
                    name: function_call.name,
                    input: function_call.args,
                    cache_control: None,
                });
            }
            GooglePart::FunctionResponse { function_response } => {
                has_function_response = true;
                blocks.push(ContentBlock::ToolResult {
                    tool_use_id: function_response.name,
                    content: function_response.response,
                    is_error: None,
                    cache_control: None,
                });
            }
            GooglePart::ExecutableCode { executable_code } => {
                blocks.push(ContentBlock::ExecutableCode {
                    code: executable_code.code,
                    language: executable_code.language,
                    id: None,
                });
            }
            GooglePart::CodeExecutionResult {
                code_execution_result,
            } => {
                let output = code_execution_result.output.unwrap_or_default();
                blocks.push(ContentBlock::CodeExecutionResult {
                    return_code: 0,
                    stdout: output,
                    stderr: String::new(),
                    id: None,
                });
            }
            GooglePart::Other(v) => {
                // Detect thought parts (Gemini 2.5 extended thinking).
                let is_thought = v.get("thought").and_then(|t| t.as_bool()).unwrap_or(false);
                if is_thought {
                    let thinking = v
                        .get("text")
                        .and_then(|t| t.as_str())
                        .unwrap_or("")
                        .to_string();
                    blocks.push(ContentBlock::Thinking {
                        thinking,
                        signature: None,
                    });
                } else if let Some(text) = v.get("text").and_then(|t| t.as_str()) {
                    text_parts.push(text.to_string());
                    blocks.push(ContentBlock::Text {
                        text: text.to_string(),
                        cache_control: None,
                    });
                }
            }
        }
    }

    let msg_content = if blocks.len() == 1 && text_parts.len() == 1 {
        MessageContent::Text(text_parts.into_iter().next().unwrap())
    } else {
        MessageContent::Blocks(blocks)
    };

    let tool_calls_opt = if tool_calls.is_empty() {
        None
    } else {
        Some(tool_calls)
    };
    if has_function_response {
        role = Role::Tool;
    }

    Ok(Message {
        role,
        content: msg_content,
        tool_calls: tool_calls_opt,
        tool_call_id: None,
        meta: None,
    })
}
