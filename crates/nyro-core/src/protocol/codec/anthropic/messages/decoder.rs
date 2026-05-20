//! Anthropic Messages ingress decoder — produces `AiRequest` directly.
//!
//! Cache-control and exotic blocks (Document, InputAudio) are now carried
//! natively in `ContentBlock`. Raw wire values are also preserved in
//! `meta.vendor.ingress` so the old Anthropic encoder (pre-PR-3) can still
//! round-trip them.

use anyhow::Result;
use serde_json::Value;

use crate::protocol::RequestDecoder;
use crate::protocol::ids::ANTHROPIC_MESSAGES_2023_06_01;
use crate::protocol::ir::DocumentSource as IrDocumentSource;
use crate::protocol::ir::{
    AiRequest, AnthropicExt, CacheTtl, ContentBlock, GenerationConfig, MediaSource, Message,
    MessageContent, ProtocolExt, ReasoningConfig, Role, StreamConfig, ToolCall, ToolChoice,
    ToolSpec,
};

use super::types::{
    AnthropicContent, AnthropicContentBlock, AnthropicImageSource, AnthropicMessage,
    AnthropicRequest, AnthropicSystem, CacheControl as WireCacheControl,
    DocumentSource as WireDocumentSource,
};

pub struct AnthropicDecoder;

impl RequestDecoder for AnthropicDecoder {
    fn decode_request(&self, body: Value) -> Result<AiRequest> {
        let req: AnthropicRequest = serde_json::from_value(body)?;

        // ── Cache-control / exotic-block detection (for raw preservation) ──────
        let needs_raw_msgs = req.messages.iter().any(|m| {
            if let AnthropicContent::Blocks(blocks) = &m.content {
                blocks
                    .iter()
                    .any(|b| b.cache_control().is_some() || b.is_exotic())
            } else {
                false
            }
        });

        let needs_raw_system = match &req.system {
            Some(AnthropicSystem::Blocks(blocks)) => {
                blocks.iter().any(|b| b.cache_control.is_some())
            }
            _ => false,
        };

        let needs_raw_tools = req
            .tools
            .as_ref()
            .map(|ts| ts.iter().any(|t| t.cache_control.is_some()))
            .unwrap_or(false);

        // Snapshot raw wire values before consuming req.
        let raw_messages: Option<Value> = if needs_raw_msgs {
            serde_json::to_value(&req.messages).ok()
        } else {
            None
        };
        let raw_system: Option<Value> = if needs_raw_system {
            req.system
                .as_ref()
                .and_then(|s| serde_json::to_value(s).ok())
        } else {
            None
        };
        let raw_tools: Option<Value> = if needs_raw_tools {
            req.tools
                .as_ref()
                .and_then(|t| serde_json::to_value(t).ok())
        } else {
            None
        };

        // ── System prompt ─────────────────────────────────────────────────────
        // Extracted as the leading system message (backward compat with old encoders).
        let mut messages: Vec<Message> = Vec::new();

        if let Some(system) = &req.system {
            let text = match system {
                AnthropicSystem::Text(t) => t.clone(),
                AnthropicSystem::Blocks(blocks) => blocks
                    .iter()
                    .filter_map(|b| {
                        if b.kind == "text" {
                            Some(b.text.as_str())
                        } else {
                            None
                        }
                    })
                    .collect::<Vec<_>>()
                    .join("\n"),
            };
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

        for msg in req.messages {
            messages.extend(decode_message(msg)?);
        }

        // ── Tools ─────────────────────────────────────────────────────────────
        let mut user_tools: Vec<ToolSpec> = Vec::new();
        let mut server_tools: Vec<Value> = Vec::new();

        if let Some(tools) = req.tools {
            for t in tools {
                if let Some(ref tool_type) = t.tool_type {
                    // Built-in / server tool — preserve as raw Value in AnthropicExt.
                    let mut raw = serde_json::Map::new();
                    raw.insert("type".to_string(), Value::String(tool_type.clone()));
                    if let Some(d) = &t.description {
                        raw.insert("description".to_string(), Value::String(d.clone()));
                    }
                    if let Some(s) = &t.input_schema {
                        raw.insert("input_schema".to_string(), s.clone());
                    }
                    server_tools.push(Value::Object(raw));
                    // Also keep sentinel ToolSpec so dispatcher knows about it.
                    user_tools.push(ToolSpec {
                        name: format!("__builtin__{}", tool_type),
                        description: t.description,
                        parameters: t.input_schema.unwrap_or(Value::Object(Default::default())),
                        strict: None,
                        cache_control: t.cache_control.as_ref().map(map_cache_control),
                        meta: None,
                    });
                } else if let Some(schema) = t.input_schema {
                    user_tools.push(ToolSpec {
                        name: t.name,
                        description: t.description,
                        parameters: schema,
                        strict: None,
                        cache_control: t.cache_control.as_ref().map(map_cache_control),
                        meta: None,
                    });
                }
            }
        }

        // ── tool_choice ───────────────────────────────────────────────────────
        let tool_choice = req.tool_choice.map(parse_tool_choice);

        // ── Reasoning config ──────────────────────────────────────────────────
        let reasoning = if let Some(ref thinking) = req.thinking {
            let enabled = thinking.kind == "enabled";
            ReasoningConfig {
                enabled,
                budget_tokens: thinking.budget_tokens,
                ..Default::default()
            }
        } else {
            ReasoningConfig::default()
        };

        // ── AnthropicExt ──────────────────────────────────────────────────────
        let ant_ext = AnthropicExt {
            top_k: req.top_k,
            container: req.container.as_ref().map(|c| Value::String(c.clone())),
            service_tier: req.service_tier.clone(),
            server_tools: if server_tools.is_empty() {
                None
            } else {
                Some(server_tools)
            },
            ..Default::default()
        };

        // ── Vendor ingress bag — backward compat for old Anthropic encoder ────
        let mut ingress = std::collections::HashMap::new();

        if let Some(v) = raw_messages {
            ingress.insert("__anthropic_raw_messages".into(), v);
        }
        if let Some(v) = raw_system {
            ingress.insert("__anthropic_raw_system".into(), v);
        }
        if let Some(v) = raw_tools {
            ingress.insert("__anthropic_raw_tools".into(), v);
        }
        if let Some(ref thinking) = req.thinking
            && let Ok(v) = serde_json::to_value(thinking)
        {
            ingress.insert("__anthropic_thinking".into(), v);
        }
        if let Some(ref cm) = req.context_management {
            ingress.insert("__anthropic_context_management".into(), cm.clone());
        }
        if let Some(ref c) = req.container {
            ingress.insert("__anthropic_container".into(), Value::String(c.clone()));
        }
        if let Some(ref st) = req.service_tier {
            ingress.insert("__anthropic_service_tier".into(), Value::String(st.clone()));
        }
        if let Some(ref meta) = req.metadata {
            ingress.insert("__anthropic_metadata".into(), meta.clone());
        }
        if let Some(ref stops) = req.stop_sequences
            && let Ok(v) = serde_json::to_value(stops)
        {
            ingress.insert("__anthropic_stop_sequences".into(), v);
        }
        if let Some(k) = req.top_k {
            ingress.insert("__anthropic_top_k".into(), Value::Number(k.into()));
        }

        // ── Build AiRequest ───────────────────────────────────────────────────
        let tools_opt = if user_tools.is_empty() {
            None
        } else {
            Some(user_tools)
        };

        let mut ai_req = AiRequest::new(req.model, messages);
        ai_req.generation = GenerationConfig {
            temperature: req.temperature,
            max_tokens: Some(req.max_tokens),
            top_p: req.top_p,
            stop: req.stop_sequences,
            ..Default::default()
        };
        ai_req.stream = StreamConfig {
            enabled: req.stream,
            include_usage: false,
        };
        ai_req.tools = tools_opt;
        ai_req.tool_choice = tool_choice;
        ai_req.reasoning = reasoning;
        ai_req.ext = Some(ProtocolExt::Anthropic(ant_ext));
        ai_req.meta.source_protocol = Some(ANTHROPIC_MESSAGES_2023_06_01);
        ai_req.meta.vendor.ingress = ingress;

        Ok(ai_req)
    }
}

// ── Message decoding helpers ──────────────────────────────────────────────────

fn decode_message(msg: AnthropicMessage) -> Result<Vec<Message>> {
    let role = match msg.role.as_str() {
        "user" => Role::User,
        "assistant" => Role::Assistant,
        other => anyhow::bail!("unknown Anthropic role: {other}"),
    };

    if role == Role::User
        && let AnthropicContent::Blocks(blocks) = msg.content
    {
        return decode_user_blocks(blocks);
    }

    let (content, tool_calls, tool_call_id) = match msg.content {
        AnthropicContent::Text(t) => (MessageContent::Text(t), None, None),
        AnthropicContent::Blocks(blocks) => {
            let mut content_blocks: Vec<ContentBlock> = Vec::new();
            let mut tcs: Vec<ToolCall> = Vec::new();
            let mut tc_id: Option<String> = None;
            let mut thinking_texts: Vec<String> = Vec::new();

            for block in blocks {
                match block {
                    AnthropicContentBlock::Text {
                        text,
                        cache_control,
                        ..
                    } => {
                        content_blocks.push(ContentBlock::Text {
                            text,
                            cache_control: cache_control.as_ref().map(map_cache_control),
                        });
                    }
                    AnthropicContentBlock::Thinking {
                        thinking,
                        signature,
                        ..
                    } => {
                        if role == Role::Assistant && !thinking.is_empty() {
                            thinking_texts.push(thinking.clone());
                        }
                        content_blocks.push(ContentBlock::Thinking {
                            thinking,
                            signature,
                        });
                    }
                    AnthropicContentBlock::Image {
                        source,
                        cache_control,
                        ..
                    } => {
                        content_blocks.push(ContentBlock::Image {
                            source: map_image_source(source),
                            cache_control: cache_control.as_ref().map(map_cache_control),
                        });
                    }
                    AnthropicContentBlock::ToolUse {
                        id,
                        name,
                        input,
                        cache_control,
                        ..
                    } => {
                        tcs.push(ToolCall {
                            id: id.clone(),
                            name: name.clone(),
                            arguments: input.to_string(),
                        });
                        content_blocks.push(ContentBlock::ToolUse {
                            id,
                            name,
                            input,
                            cache_control: cache_control.as_ref().map(map_cache_control),
                        });
                    }
                    AnthropicContentBlock::ToolResult {
                        tool_use_id,
                        content,
                        cache_control,
                        ..
                    } => {
                        tc_id = Some(tool_use_id.clone());
                        content_blocks.push(ContentBlock::ToolResult {
                            tool_use_id,
                            content: content.unwrap_or(Value::Null),
                            is_error: None,
                            cache_control: cache_control.as_ref().map(map_cache_control),
                        });
                    }
                    AnthropicContentBlock::Document {
                        source,
                        title,
                        context,
                        cache_control,
                        ..
                    } => {
                        content_blocks.push(ContentBlock::Document {
                            source: map_doc_source(source),
                            title,
                            context,
                            cache_control: cache_control.as_ref().map(map_cache_control),
                        });
                    }
                    AnthropicContentBlock::InputAudio { source, .. } => {
                        content_blocks.push(ContentBlock::Audio {
                            source: MediaSource::Base64 {
                                media_type: source.media_type,
                                data: source.data,
                            },
                        });
                    }
                }
            }

            let tool_calls_opt = if tcs.is_empty() { None } else { Some(tcs) };

            let meta = build_reasoning_meta(&thinking_texts);

            if content_blocks.len() == 1
                && let ContentBlock::Text { text, .. } = &content_blocks[0]
            {
                return Ok(vec![Message {
                    role,
                    content: MessageContent::Text(text.clone()),
                    tool_calls: tool_calls_opt,
                    tool_call_id: tc_id,
                    meta,
                }]);
            }

            return Ok(vec![Message {
                role,
                content: MessageContent::Blocks(content_blocks),
                tool_calls: tool_calls_opt,
                tool_call_id: tc_id,
                meta,
            }]);
        }
    };

    Ok(vec![Message {
        role,
        content,
        tool_calls,
        tool_call_id,
        meta: None,
    }])
}

/// Build `meta = {"reasoning_content": ...}` so the OpenAI-compat encoder can
/// re-emit thinking text as a top-level `reasoning_content` field on assistant
/// messages (required by providers like Xiaomi Mimo / DeepSeek thinking mode).
fn build_reasoning_meta(thinking_texts: &[String]) -> Option<Value> {
    if thinking_texts.is_empty() {
        return None;
    }
    let joined = thinking_texts.join("\n\n");
    if joined.is_empty() {
        return None;
    }
    Some(serde_json::json!({ "reasoning_content": joined }))
}

fn decode_user_blocks(blocks: Vec<AnthropicContentBlock>) -> Result<Vec<Message>> {
    let mut messages: Vec<Message> = Vec::new();
    let mut user_blocks: Vec<ContentBlock> = Vec::new();

    for block in blocks {
        match block {
            AnthropicContentBlock::ToolResult {
                tool_use_id,
                content,
                ..
            } => {
                let tool_text = match content.unwrap_or(Value::Null) {
                    Value::String(s) => s,
                    Value::Null => String::new(),
                    other => other.to_string(),
                };
                messages.push(Message {
                    role: Role::Tool,
                    content: MessageContent::Text(tool_text),
                    tool_calls: None,
                    tool_call_id: Some(tool_use_id),
                    meta: None,
                });
            }
            AnthropicContentBlock::Text {
                text,
                cache_control,
                ..
            } => {
                user_blocks.push(ContentBlock::Text {
                    text,
                    cache_control: cache_control.as_ref().map(map_cache_control),
                });
            }
            AnthropicContentBlock::Thinking {
                thinking,
                signature,
                ..
            } => {
                user_blocks.push(ContentBlock::Thinking {
                    thinking,
                    signature,
                });
            }
            AnthropicContentBlock::Image {
                source,
                cache_control,
                ..
            } => {
                user_blocks.push(ContentBlock::Image {
                    source: map_image_source(source),
                    cache_control: cache_control.as_ref().map(map_cache_control),
                });
            }
            AnthropicContentBlock::ToolUse {
                id,
                name,
                input,
                cache_control,
                ..
            } => {
                user_blocks.push(ContentBlock::ToolUse {
                    id,
                    name,
                    input,
                    cache_control: cache_control.as_ref().map(map_cache_control),
                });
            }
            AnthropicContentBlock::Document {
                source,
                title,
                context,
                cache_control,
                ..
            } => {
                user_blocks.push(ContentBlock::Document {
                    source: map_doc_source(source),
                    title,
                    context,
                    cache_control: cache_control.as_ref().map(map_cache_control),
                });
            }
            AnthropicContentBlock::InputAudio { source, .. } => {
                user_blocks.push(ContentBlock::Audio {
                    source: MediaSource::Base64 {
                        media_type: source.media_type,
                        data: source.data,
                    },
                });
            }
        }
    }

    if !user_blocks.is_empty() {
        let content = if user_blocks.len() == 1
            && let ContentBlock::Text { text, .. } = &user_blocks[0]
        {
            MessageContent::Text(text.clone())
        } else {
            MessageContent::Blocks(user_blocks)
        };
        messages.insert(
            0,
            Message {
                role: Role::User,
                content,
                tool_calls: None,
                tool_call_id: None,
                meta: None,
            },
        );
    }

    Ok(messages)
}

// ── Type-mapping helpers ──────────────────────────────────────────────────────

fn map_cache_control(_cc: &WireCacheControl) -> crate::protocol::ir::CacheControl {
    // Anthropic only has "ephemeral" TTL.
    crate::protocol::ir::CacheControl {
        ttl: CacheTtl::Ephemeral5m,
        breakpoint_priority: 0,
    }
}

fn map_image_source(src: AnthropicImageSource) -> MediaSource {
    match src.source_type.as_str() {
        "base64" => MediaSource::Base64 {
            media_type: src.media_type.unwrap_or_default(),
            data: src.data.unwrap_or_default(),
        },
        "url" => MediaSource::Url(src.url.unwrap_or_default()),
        _ => MediaSource::Base64 {
            media_type: src.media_type.unwrap_or_default(),
            data: src.data.unwrap_or_default(),
        },
    }
}

fn map_doc_source(src: WireDocumentSource) -> IrDocumentSource {
    match src.kind.as_str() {
        "base64" => IrDocumentSource::Base64Pdf {
            data: src.data.unwrap_or_default(),
        },
        "text" => IrDocumentSource::PlainText {
            data: src.data.unwrap_or_default(),
        },
        "url" => IrDocumentSource::Url(src.url.unwrap_or_default()),
        "content" => IrDocumentSource::Blocks {
            content: Vec::new(), // encoder uses raw JSON; best-effort stub
        },
        _ => IrDocumentSource::PlainText {
            data: src.data.unwrap_or_default(),
        },
    }
}

fn parse_tool_choice(v: Value) -> ToolChoice {
    match v.get("type").and_then(|t| t.as_str()) {
        Some("auto") => ToolChoice::Auto,
        Some("any") => ToolChoice::Required,
        Some("none") => ToolChoice::None,
        Some("tool") => {
            if let Some(name) = v.get("name").and_then(|n| n.as_str()) {
                ToolChoice::Named {
                    name: name.to_string(),
                }
            } else {
                ToolChoice::Raw(v)
            }
        }
        _ => ToolChoice::Raw(v),
    }
}
