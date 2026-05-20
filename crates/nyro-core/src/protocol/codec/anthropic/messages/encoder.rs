use anyhow::Result;
use reqwest::header::{HeaderMap, HeaderValue};
use serde_json::Value;

use crate::protocol::RequestEncoder;
use crate::protocol::ir::AiRequest;
use crate::protocol::ir::request::{
    ContentBlock, MediaSource, Message, MessageContent, Role, ToolChoice,
};

pub struct AnthropicEncoder;

impl RequestEncoder for AnthropicEncoder {
    fn encode_request(&self, req: &AiRequest) -> Result<(Value, HeaderMap)> {
        let ingress = &req.meta.vendor.ingress;

        // ── System ────────────────────────────────────────────────────────────
        // Prefer __anthropic_raw_system (preserves cache_control) if present.
        let system_val: Option<Value> = if let Some(v) = ingress.get("__anthropic_raw_system") {
            Some(v.clone())
        } else {
            let mut system_text = String::new();
            for msg in &req.messages {
                if msg.role == Role::System {
                    if !system_text.is_empty() {
                        system_text.push('\n');
                    }
                    system_text.push_str(&msg.content.to_text());
                }
            }
            if system_text.is_empty() {
                None
            } else {
                Some(Value::String(system_text))
            }
        };

        // ── Messages ──────────────────────────────────────────────────────────
        // Prefer __anthropic_raw_messages (preserves cache_control / exotic
        // blocks) if present; otherwise reconstruct from Message.
        let messages_val: Value = if let Some(v) = ingress.get("__anthropic_raw_messages") {
            v.clone()
        } else {
            let mut raw_messages = Vec::new();
            for msg in &req.messages {
                if msg.role == Role::System {
                    continue;
                }
                raw_messages.push(encode_message(msg)?);
            }
            Value::Array(normalize_anthropic_messages(raw_messages))
        };

        let max_tokens = req.generation.max_tokens.unwrap_or(4096);

        let mut body = serde_json::json!({
            "model": req.model,
            "messages": messages_val,
            "max_tokens": max_tokens,
            "stream": req.stream.enabled,
        });

        let obj = body.as_object_mut().unwrap();

        if let Some(sv) = system_val {
            obj.insert("system".into(), sv);
        }
        if let Some(t) = req.generation.temperature {
            obj.insert("temperature".into(), t.into());
        }
        if let Some(p) = req.generation.top_p {
            obj.insert("top_p".into(), p.into());
        }

        // ── Tools ─────────────────────────────────────────────────────────────
        // Prefer raw tools (preserves cache_control) if present.
        if let Some(raw_tools) = ingress.get("__anthropic_raw_tools") {
            obj.insert("tools".into(), raw_tools.clone());
        } else if let Some(ref tools) = req.tools {
            let tools_val: Vec<Value> = tools
                .iter()
                .map(|t| {
                    if let Some(builtin_type) = t.name.strip_prefix("__builtin__") {
                        let mut entry = serde_json::json!({
                            "type": builtin_type,
                            "name": builtin_type,
                        });
                        if let Some(desc) = &t.description {
                            entry
                                .as_object_mut()
                                .unwrap()
                                .insert("description".into(), Value::String(desc.clone()));
                        }
                        entry
                    } else {
                        serde_json::json!({
                            "name": t.name,
                            "description": t.description,
                            "input_schema": t.parameters,
                        })
                    }
                })
                .collect();
            obj.insert("tools".into(), Value::Array(tools_val));
        }

        // ── Tool choice ───────────────────────────────────────────────────────
        if let Some(ref tc) = req.tool_choice {
            let raw = tool_choice_to_value_raw(tc);
            if let Some(mapped) = map_tool_choice_for_anthropic(&raw) {
                obj.insert("tool_choice".into(), mapped);
            }
        }

        // ── Extra fields ──────────────────────────────────────────────────────
        for key in &["__anthropic_thinking", "__anthropic_context_management"] {
            if let Some(v) = ingress.get(*key) {
                let field_name = key.trim_start_matches("__anthropic_");
                obj.insert(field_name.into(), v.clone());
            }
        }
        for key in &[
            "__anthropic_container",
            "__anthropic_service_tier",
            "__anthropic_metadata",
            "__anthropic_stop_sequences",
            "__anthropic_top_k",
        ] {
            if let Some(v) = ingress.get(*key) {
                let field_name = key.trim_start_matches("__anthropic_");
                obj.insert(field_name.into(), v.clone());
            }
        }

        validate_anthropic_payload(&body)?;

        let mut headers = HeaderMap::new();
        headers.insert("anthropic-version", HeaderValue::from_static("2023-06-01"));

        Ok((body, headers))
    }

    fn egress_path(&self, _model: &str, _stream: bool) -> String {
        "/v1/messages".to_string()
    }
}

// ── tool_choice helpers ───────────────────────────────────────────────────────

fn tool_choice_to_value_raw(tc: &ToolChoice) -> Value {
    match tc {
        ToolChoice::Auto => Value::String("auto".into()),
        ToolChoice::None => Value::String("none".into()),
        ToolChoice::Required => Value::String("required".into()),
        ToolChoice::Named { name } => serde_json::json!({
            "type": "tool",
            "name": name,
        }),
        ToolChoice::Raw(v) => v.clone(),
    }
}

fn map_tool_choice_for_anthropic(raw: &Value) -> Option<Value> {
    if let Some(s) = raw.as_str() {
        return match s {
            "auto" => Some(serde_json::json!({ "type": "auto" })),
            "required" => Some(serde_json::json!({ "type": "any" })),
            "none" => None,
            _ => None,
        };
    }

    let obj = raw.as_object()?;
    let kind = obj.get("type").and_then(|v| v.as_str()).unwrap_or("");
    let disable_parallel = obj
        .get("disable_parallel_tool_use")
        .and_then(|v| v.as_bool())
        .unwrap_or(false);

    let mut result = match kind {
        "auto" => serde_json::json!({ "type": "auto" }),
        "required" | "any" => serde_json::json!({ "type": "any" }),
        "none" => return None,
        "tool" => {
            let name = obj.get("name").and_then(|v| v.as_str()).unwrap_or("");
            if name.is_empty() {
                return None;
            }
            serde_json::json!({ "type": "tool", "name": name })
        }
        "function" => {
            let name = obj
                .get("name")
                .and_then(|v| v.as_str())
                .or_else(|| {
                    obj.get("function")
                        .and_then(|f| f.get("name"))
                        .and_then(|v| v.as_str())
                })
                .unwrap_or("");
            if name.is_empty() {
                return None;
            }
            serde_json::json!({ "type": "tool", "name": name })
        }
        _ => return None,
    };

    if disable_parallel {
        result
            .as_object_mut()
            .unwrap()
            .insert("disable_parallel_tool_use".into(), Value::Bool(true));
    }

    Some(result)
}

// ── Payload validation ────────────────────────────────────────────────────────

const ALLOWED_BLOCK_TYPES: &[&str] = &[
    "text",
    "image",
    "thinking",
    "tool_use",
    "tool_result",
    "document",
    "input_audio",
];

fn validate_anthropic_payload(body: &Value) -> Result<()> {
    let obj = body
        .as_object()
        .ok_or_else(|| anyhow::anyhow!("anthropic payload must be object"))?;
    let _model = obj
        .get("model")
        .and_then(|v| v.as_str())
        .filter(|v| !v.trim().is_empty())
        .ok_or_else(|| anyhow::anyhow!("anthropic payload missing model"))?;
    let _max_tokens = obj
        .get("max_tokens")
        .and_then(|v| v.as_u64())
        .ok_or_else(|| anyhow::anyhow!("anthropic payload missing max_tokens"))?;
    let messages = obj
        .get("messages")
        .and_then(|v| v.as_array())
        .ok_or_else(|| anyhow::anyhow!("anthropic payload missing messages"))?;
    if messages.is_empty() {
        anyhow::bail!("anthropic payload has empty messages");
    }
    for (idx, msg) in messages.iter().enumerate() {
        let role = msg
            .get("role")
            .and_then(|v| v.as_str())
            .ok_or_else(|| anyhow::anyhow!("anthropic payload message[{idx}] missing role"))?;
        if role != "user" && role != "assistant" {
            anyhow::bail!("anthropic payload message[{idx}] invalid role: {role}");
        }

        if let Some(content) = msg.get("content") {
            match content {
                Value::String(_) => {}
                Value::Array(blocks) => {
                    for (bidx, block) in blocks.iter().enumerate() {
                        let btype =
                            block.get("type").and_then(|v| v.as_str()).ok_or_else(|| {
                                anyhow::anyhow!(
                                    "anthropic payload message[{idx}] block[{bidx}] missing type"
                                )
                            })?;
                        if !ALLOWED_BLOCK_TYPES.contains(&btype) {
                            anyhow::bail!(
                                "anthropic payload message[{idx}] unsupported block type: {btype}"
                            );
                        }
                        match btype {
                            "tool_use" => {
                                let id = block.get("id").and_then(|v| v.as_str()).unwrap_or("");
                                let name = block.get("name").and_then(|v| v.as_str()).unwrap_or("");
                                if id.is_empty() || name.is_empty() {
                                    anyhow::bail!(
                                        "anthropic payload message[{idx}] tool_use block[{bidx}] missing id/name"
                                    );
                                }
                            }
                            "tool_result" => {
                                let tool_use_id = block
                                    .get("tool_use_id")
                                    .and_then(|v| v.as_str())
                                    .unwrap_or("");
                                if tool_use_id.is_empty() {
                                    anyhow::bail!(
                                        "anthropic payload message[{idx}] tool_result block[{bidx}] missing tool_use_id"
                                    );
                                }
                            }
                            _ => {}
                        }
                    }
                }
                _ => {
                    anyhow::bail!(
                        "anthropic payload message[{idx}] content must be string or array"
                    );
                }
            }
        } else {
            anyhow::bail!("anthropic payload message[{idx}] missing content");
        }
    }

    if let Some(tool_choice) = obj.get("tool_choice") {
        let tc = tool_choice
            .as_object()
            .ok_or_else(|| anyhow::anyhow!("anthropic tool_choice must be object"))?;
        let t = tc.get("type").and_then(|v| v.as_str()).unwrap_or("");
        if t != "auto" && t != "any" && t != "tool" {
            anyhow::bail!("anthropic tool_choice invalid type: {t}");
        }
        if t == "tool"
            && tc
                .get("name")
                .and_then(|v| v.as_str())
                .unwrap_or("")
                .is_empty()
        {
            anyhow::bail!("anthropic tool_choice=tool missing name");
        }
    }

    Ok(())
}

// ── Message encoding helpers ──────────────────────────────────────────────────

fn encode_message(msg: &Message) -> Result<Value> {
    let role = match msg.role {
        Role::User | Role::Tool => "user",
        Role::Assistant => "assistant",
        Role::System => unreachable!("system handled separately"),
    };

    if msg.role == Role::Tool {
        let (tool_content, hinted_tool_use_id) = anthropic_tool_result_payload(msg);
        let tool_use_id = msg
            .tool_call_id
            .clone()
            .filter(|v| !v.trim().is_empty())
            .or(hinted_tool_use_id)
            .map(|v| normalize_anthropic_tool_id(&v))
            .unwrap_or_else(|| normalize_anthropic_tool_id("tool_result"));
        return Ok(serde_json::json!({
            "role": role,
            "content": [{
                "type": "tool_result",
                "tool_use_id": tool_use_id,
                "content": tool_content,
            }],
        }));
    }

    let meta_obj = msg.meta.as_ref().and_then(|m| m.as_object());
    let content = match &msg.content {
        MessageContent::Text(t) => {
            let reasoning = meta_obj
                .and_then(|m| m.get("reasoning_content"))
                .and_then(|v| v.as_str())
                .filter(|v| !v.trim().is_empty());
            let reasoning_signature = meta_obj
                .and_then(|m| m.get("reasoning_signature"))
                .and_then(|v| v.as_str())
                .filter(|v| !v.trim().is_empty());

            if reasoning.is_some() || msg.tool_calls.is_some() {
                let mut blocks: Vec<Value> = vec![];
                if let Some(text) = reasoning {
                    let mut block = serde_json::json!({
                        "type": "thinking",
                        "thinking": text,
                    });
                    if let Some(signature) = reasoning_signature
                        && let Some(obj) = block.as_object_mut()
                    {
                        obj.insert("signature".into(), serde_json::json!(signature));
                    }
                    blocks.push(block);
                }
                if !t.is_empty() {
                    blocks.push(serde_json::json!({"type": "text", "text": t}));
                }
                if let Some(ref tcs) = msg.tool_calls {
                    for tc in tcs {
                        let input: Value = serde_json::from_str(&tc.arguments)
                            .unwrap_or(Value::Object(Default::default()));
                        blocks.push(serde_json::json!({
                            "type": "tool_use",
                            "id": normalize_anthropic_tool_id(&tc.id),
                            "name": tc.name,
                            "input": input,
                        }));
                    }
                }
                Value::Array(blocks)
            } else {
                Value::String(t.clone())
            }
        }
        MessageContent::Blocks(blocks) => {
            let arr: Vec<Value> = blocks
                .iter()
                .map(|b| encode_content_block_for_anthropic(b))
                .collect();
            Value::Array(arr)
        }
    };

    Ok(serde_json::json!({
        "role": role,
        "content": content,
    }))
}

fn encode_content_block_for_anthropic(b: &ContentBlock) -> Value {
    match b {
        ContentBlock::Text {
            text,
            cache_control,
        } => {
            let mut block = serde_json::json!({"type": "text", "text": text});
            if let Some(cc) = cache_control {
                block["cache_control"] = serde_json::to_value(cc).unwrap_or(Value::Null);
            }
            block
        }
        ContentBlock::Image {
            source,
            cache_control,
        } => {
            let src = match source {
                MediaSource::Base64 { media_type, data } => serde_json::json!({
                    "type": "base64",
                    "media_type": media_type,
                    "data": data,
                }),
                MediaSource::Url(url) => serde_json::json!({
                    "type": "url",
                    "url": url,
                }),
                MediaSource::FileId { file_id, .. } => serde_json::json!({
                    "type": "file",
                    "file_id": file_id,
                }),
            };
            let mut block = serde_json::json!({"type": "image", "source": src});
            if let Some(cc) = cache_control {
                block["cache_control"] = serde_json::to_value(cc).unwrap_or(Value::Null);
            }
            block
        }
        ContentBlock::Thinking {
            thinking,
            signature,
        } => {
            let mut block = serde_json::json!({
                "type": "thinking",
                "thinking": thinking,
            });
            if let Some(sig) = signature
                && !sig.trim().is_empty()
                && let Some(obj) = block.as_object_mut()
            {
                obj.insert("signature".into(), serde_json::json!(sig));
            }
            block
        }
        ContentBlock::RedactedThinking { data } => {
            serde_json::json!({"type": "redacted_thinking", "data": data})
        }
        ContentBlock::ToolUse {
            id,
            name,
            input,
            cache_control,
        } => {
            let mut block = serde_json::json!({
                "type": "tool_use",
                "id": normalize_anthropic_tool_id(id),
                "name": name,
                "input": input,
            });
            if let Some(cc) = cache_control {
                block["cache_control"] = serde_json::to_value(cc).unwrap_or(Value::Null);
            }
            block
        }
        ContentBlock::ToolResult {
            tool_use_id,
            content,
            is_error,
            cache_control,
        } => {
            let mut block = serde_json::json!({
                "type": "tool_result",
                "tool_use_id": normalize_anthropic_tool_id(tool_use_id),
                "content": content,
            });
            if let Some(err) = is_error {
                block["is_error"] = Value::Bool(*err);
            }
            if let Some(cc) = cache_control {
                block["cache_control"] = serde_json::to_value(cc).unwrap_or(Value::Null);
            }
            block
        }
        ContentBlock::Unknown { raw } => raw.clone(),
        other => serde_json::to_value(other).unwrap_or(Value::Null),
    }
}

fn anthropic_tool_result_payload(msg: &Message) -> (Value, Option<String>) {
    match &msg.content {
        MessageContent::Text(t) => (Value::String(t.clone()), None),
        MessageContent::Blocks(blocks) => {
            for block in blocks {
                if let ContentBlock::ToolResult {
                    tool_use_id,
                    content,
                    ..
                } = block
                {
                    return (content.clone(), Some(tool_use_id.clone()));
                }
            }
            (Value::String(msg.content.to_text()), None)
        }
    }
}

fn normalize_anthropic_tool_id(raw: &str) -> String {
    let trimmed = raw.trim();
    if trimmed.is_empty() {
        return "toolu_nyro".to_string();
    }
    if trimmed.starts_with("toolu_")
        && trimmed
            .chars()
            .all(|ch| ch.is_ascii_alphanumeric() || ch == '_' || ch == '-')
    {
        return trimmed.to_string();
    }
    let sanitized: String = trimmed
        .chars()
        .map(|ch| {
            if ch.is_ascii_alphanumeric() || ch == '_' || ch == '-' {
                ch
            } else {
                '_'
            }
        })
        .collect();
    format!("toolu_{sanitized}")
}

fn normalize_anthropic_messages(messages: Vec<Value>) -> Vec<Value> {
    let mut normalized: Vec<Value> = Vec::new();
    for msg in messages {
        let Some(role) = msg.get("role").and_then(|v| v.as_str()) else {
            continue;
        };
        let blocks = content_to_blocks(msg.get("content").cloned().unwrap_or(Value::Null));
        if blocks.is_empty() {
            continue;
        }

        if let Some(last) = normalized.last_mut() {
            let same_role = last.get("role").and_then(|v| v.as_str()) == Some(role);
            if same_role {
                if let Some(last_obj) = last.as_object_mut() {
                    let mut merged =
                        content_to_blocks(last_obj.get("content").cloned().unwrap_or(Value::Null));
                    merged.extend(blocks);
                    last_obj.insert("content".into(), Value::Array(merged));
                }
                continue;
            }
        }

        normalized.push(serde_json::json!({
            "role": role,
            "content": Value::Array(blocks),
        }));
    }

    // DeepSeek's Anthropic-compatible endpoint requires assistant tool_use
    // blocks to trail the assistant turn when the next user turn contains
    // matching tool_result blocks. Codex Responses input may place assistant
    // commentary after function_call items, which otherwise normalizes into
    // `[tool_use, tool_use, text]`.
    for msg in &mut normalized {
        if msg.get("role").and_then(|v| v.as_str()) != Some("assistant") {
            continue;
        }
        let Some(arr) = msg.get_mut("content").and_then(|v| v.as_array_mut()) else {
            continue;
        };
        if !arr
            .iter()
            .any(|b| b.get("type").and_then(|v| v.as_str()) == Some("tool_use"))
        {
            continue;
        }
        let mut thinking: Vec<Value> = Vec::new();
        let mut others: Vec<Value> = Vec::new();
        let mut tool_uses: Vec<Value> = Vec::new();
        for b in arr.drain(..) {
            match b.get("type").and_then(|v| v.as_str()).unwrap_or("") {
                "thinking" => thinking.push(b),
                "tool_use" => tool_uses.push(b),
                _ => others.push(b),
            }
        }
        let mut reordered = thinking;
        reordered.extend(others);
        reordered.extend(tool_uses);
        if let Some(obj) = msg.as_object_mut() {
            obj.insert("content".into(), Value::Array(reordered));
        }
    }

    normalized
}

fn content_to_blocks(content: Value) -> Vec<Value> {
    match content {
        Value::String(s) => {
            if s.trim().is_empty() {
                Vec::new()
            } else {
                vec![serde_json::json!({"type":"text","text":s})]
            }
        }
        Value::Array(arr) => arr
            .into_iter()
            .filter(|v| {
                let t = v.get("type").and_then(|x| x.as_str()).unwrap_or("");
                if t == "text" {
                    !v.get("text")
                        .and_then(|x| x.as_str())
                        .unwrap_or("")
                        .trim()
                        .is_empty()
                } else {
                    true
                }
            })
            .collect(),
        _ => Vec::new(),
    }
}
