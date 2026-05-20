use anyhow::Result;
use reqwest::header::HeaderMap;
use serde_json::Value;

use crate::protocol::RequestEncoder;
use crate::protocol::ir::AiRequest;
use crate::protocol::ir::request::{ContentBlock, MediaSource, Message, MessageContent, Role};

pub struct GoogleEncoder;

impl RequestEncoder for GoogleEncoder {
    fn encode_request(&self, req: &AiRequest) -> Result<(Value, HeaderMap)> {
        let ingress = &req.meta.vendor.ingress;

        // ── System instruction ────────────────────────────────────────────────
        let system_val: Option<Value> =
            if let Some(v) = ingress.get("__google_raw_system_instruction") {
                Some(v.clone())
            } else {
                let mut system_parts: Vec<Value> = Vec::new();
                for msg in &req.messages {
                    if msg.role == Role::System {
                        system_parts.push(serde_json::json!({"text": msg.content.to_text()}));
                    }
                }
                if system_parts.is_empty() {
                    None
                } else {
                    Some(serde_json::json!({"parts": system_parts}))
                }
            };

        // ── Contents ─────────────────────────────────────────────────────────
        let mut contents: Vec<Value> = Vec::new();
        for msg in &req.messages {
            if msg.role == Role::System {
                continue;
            }
            contents.push(encode_content(msg)?);
        }

        let mut body = serde_json::json!({ "contents": contents });
        let obj = body.as_object_mut().unwrap();

        if let Some(sv) = system_val {
            obj.insert("systemInstruction".into(), sv);
        }

        // ── generationConfig ──────────────────────────────────────────────────
        let mut gen_config: serde_json::Map<String, Value> =
            if let Some(Value::Object(m)) = ingress.get("__google_generation_config") {
                m.clone()
            } else {
                serde_json::Map::new()
            };

        if let Some(t) = req.generation.temperature {
            gen_config.insert("temperature".into(), t.into());
        }
        if let Some(m) = req.generation.max_tokens {
            gen_config.insert("maxOutputTokens".into(), m.into());
        }
        if let Some(p) = req.generation.top_p {
            gen_config.insert("topP".into(), p.into());
        }

        if !gen_config.is_empty() {
            obj.insert("generationConfig".into(), Value::Object(gen_config));
        }

        // ── Tools ─────────────────────────────────────────────────────────────
        if let Some(raw) = ingress.get("__google_raw_tools") {
            obj.insert("tools".into(), raw.clone());
        } else if let Some(ref tools) = req.tools {
            let mut fn_decls: Vec<Value> = Vec::new();
            let mut builtin_entries: Vec<Value> = Vec::new();

            for t in tools {
                match t.name.as_str() {
                    "__builtin__google_search" => {
                        builtin_entries.push(serde_json::json!({"googleSearch": {}}));
                    }
                    "__builtin__code_execution" => {
                        builtin_entries.push(serde_json::json!({"codeExecution": {}}));
                    }
                    "__builtin__google_search_retrieval" => {
                        builtin_entries.push(serde_json::json!({"googleSearchRetrieval": {}}));
                    }
                    _ => {
                        let mut decl = serde_json::json!({"name": t.name});
                        let d = decl.as_object_mut().unwrap();
                        if let Some(ref desc) = t.description {
                            d.insert("description".into(), Value::String(desc.clone()));
                        }
                        d.insert("parameters".into(), sanitize_gemini_schema(&t.parameters));
                        fn_decls.push(decl);
                    }
                }
            }

            let mut tool_array: Vec<Value> = Vec::new();
            if !fn_decls.is_empty() {
                tool_array.push(serde_json::json!({"functionDeclarations": fn_decls}));
            }
            tool_array.extend(builtin_entries);

            if !tool_array.is_empty() {
                obj.insert("tools".into(), Value::Array(tool_array));
            }
        }

        // ── Extra passthrough fields ───────────────────────────────────────────
        if let Some(v) = ingress.get("__google_tool_config") {
            obj.insert("toolConfig".into(), v.clone());
        }
        if let Some(v) = ingress.get("__google_safety_settings") {
            obj.insert("safetySettings".into(), v.clone());
        }
        if let Some(v) = ingress.get("__google_cached_content") {
            obj.insert("cachedContent".into(), v.clone());
        }

        Ok((body, HeaderMap::new()))
    }

    fn egress_path(&self, model: &str, stream: bool) -> String {
        if stream {
            format!("/v1beta/models/{}:streamGenerateContent?alt=sse", model)
        } else {
            format!("/v1beta/models/{}:generateContent", model)
        }
    }
}

// ── Schema sanitisation ───────────────────────────────────────────────────────

fn sanitize_gemini_schema(value: &Value) -> Value {
    match value {
        Value::Object(map) => {
            let mut out = serde_json::Map::new();
            for (k, v) in map {
                if matches!(
                    k.as_str(),
                    "$schema" | "additionalProperties" | "$ref" | "ref" | "definitions" | "$defs"
                ) {
                    continue;
                }
                out.insert(k.clone(), sanitize_gemini_schema(v));
            }
            Value::Object(out)
        }
        Value::Array(arr) => Value::Array(arr.iter().map(sanitize_gemini_schema).collect()),
        _ => value.clone(),
    }
}

// ── Content encoding ──────────────────────────────────────────────────────────

fn encode_content(msg: &Message) -> Result<Value> {
    let role = match msg.role {
        Role::User | Role::Tool => "user",
        Role::Assistant => "model",
        Role::System => unreachable!("system handled separately"),
    };

    let parts = match &msg.content {
        MessageContent::Text(t) => {
            if msg.tool_call_id.is_some() {
                vec![serde_json::json!({
                    "functionResponse": {
                        "name": msg.tool_call_id,
                        "response": {"result": t}
                    }
                })]
            } else if let Some(ref tcs) = msg.tool_calls {
                let mut parts = Vec::new();
                if !t.is_empty() {
                    parts.push(serde_json::json!({"text": t}));
                }
                for tc in tcs {
                    let args: Value = serde_json::from_str(&tc.arguments)
                        .unwrap_or(Value::Object(Default::default()));
                    parts
                        .push(serde_json::json!({"functionCall": {"name": tc.name, "args": args}}));
                }
                parts
            } else {
                vec![serde_json::json!({"text": t})]
            }
        }
        MessageContent::Blocks(blocks) => blocks
            .iter()
            .map(|b| encode_content_block_for_gemini(b))
            .collect(),
    };

    Ok(serde_json::json!({"role": role, "parts": parts}))
}

fn encode_content_block_for_gemini(b: &ContentBlock) -> Value {
    match b {
        ContentBlock::Text { text, .. } => serde_json::json!({"text": text}),
        ContentBlock::Image { source, .. } => match source {
            MediaSource::Base64 { media_type, data } => serde_json::json!({
                "inlineData": {
                    "mimeType": media_type,
                    "data": data,
                }
            }),
            MediaSource::Url(url) => serde_json::json!({"fileData": {"fileUri": url}}),
            MediaSource::FileId { file_id, .. } => {
                serde_json::json!({"fileData": {"fileUri": file_id}})
            }
        },
        ContentBlock::ToolUse { name, input, .. } => {
            serde_json::json!({"functionCall": {"name": name, "args": input}})
        }
        ContentBlock::ToolResult {
            tool_use_id,
            content,
            ..
        } => {
            serde_json::json!({
                "functionResponse": {"name": tool_use_id, "response": content}
            })
        }
        ContentBlock::Thinking { thinking, .. } => serde_json::json!({"text": thinking}),
        ContentBlock::Unknown { raw } => raw.clone(),
        other => serde_json::to_value(other).unwrap_or(Value::Null),
    }
}
