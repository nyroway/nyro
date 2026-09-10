//! Gemini Chat conversion. Unsupported native extensions fail rather than disappear.
use super::CodecError;
use crate::ir::*;
use nyro_protocol::{gemini as w, openai::chat as o};
use serde_json::Value;
use std::collections::{BTreeMap, BTreeSet};
fn bad(s: &str) -> CodecError {
    CodecError(s.into())
}
fn message(role: Role) -> Message {
    Message {
        role,
        content: None,
        tool_calls: None,
        tool_call_id: None,
        tool_error: false,
        name: None,
        refusal: None,
        audio: None,
    }
}
fn content_parts(content: &Option<Content>, images: bool) -> Result<Vec<w::Part>, CodecError> {
    match content {
        None => Ok(vec![]),
        Some(Content::Text(s)) => Ok(vec![w::Part {
            text: Some(s.clone()),
            ..Default::default()
        }]),
        Some(Content::Parts(parts)) => parts
            .iter()
            .map(|p| match p {
                ContentPart::Text { text } => Ok(w::Part {
                    text: Some(text.clone()),
                    ..Default::default()
                }),
                ContentPart::ImageUrl { image_url } if images => {
                    super::image::default_detail(image_url)?;
                    let super::image::Source::Base64 { mime, data } =
                        super::image::source(image_url)?
                    else {
                        return Err(bad("Gemini image conversion requires inline data"));
                    };
                    if mime == "image/gif" {
                        return Err(bad("GIF images unsupported by Gemini"));
                    }
                    Ok(w::Part {
                        inline_data: Some(w::Blob {
                            mime_type: mime.into(),
                            data: data.into(),
                        }),
                        ..Default::default()
                    })
                }
                _ => Err(bad("Gemini supports text content only in this increment")),
            })
            .collect(),
    }
}
fn check_part(p: &w::Part) -> Result<(), CodecError> {
    if usize::from(p.text.is_some())
        + usize::from(p.inline_data.is_some())
        + usize::from(p.function_call.is_some())
        + usize::from(p.function_response.is_some())
        != 1
    {
        return Err(bad("part requires exactly one supported payload"));
    }
    Ok(())
}
fn call(c: w::FunctionCall, id: String) -> Result<ToolCall, CodecError> {
    if c.name.is_empty() || !c.args.is_object() || id.is_empty() {
        return Err(bad("invalid function call"));
    }
    Ok(ToolCall::Function {
        id,
        function: FunctionCall {
            name: c.name,
            arguments: serde_json::to_string(&c.args)?,
        },
    })
}
pub fn decode_chat(value: Value, model: &str, streaming: bool) -> Result<ChatRequest, CodecError> {
    let wire: w::Request = serde_json::from_value(value)?;
    if wire.contents.is_empty() || wire.contents.iter().any(|c| c.parts.is_empty()) {
        return Err(bad("contents and parts must be nonempty"));
    }
    let mut messages = vec![];
    let mut pending: Vec<(String, String)> = vec![];
    let mut seq = 0;
    // Reserve explicit identities across the history before assigning omitted IDs.
    let mut used_ids: BTreeSet<String> = wire
        .contents
        .iter()
        .flat_map(|c| &c.parts)
        .filter_map(|p| p.function_call.as_ref()?.id.clone())
        .collect();
    if let Some(s) = wire.system_instruction {
        let mut m = message(Role::System);
        let mut parts = vec![];
        for p in s.parts {
            check_part(&p)?;
            parts.push(ContentPart::Text {
                text: p
                    .text
                    .ok_or_else(|| bad("system instruction must be text"))?,
            });
        }
        m.content = Some(Content::Parts(parts));
        messages.push(m);
    }
    for c in wire.contents {
        let role = match c.role.as_deref().unwrap_or("user") {
            "user" => Role::User,
            "model" => Role::Assistant,
            _ => return Err(bad("unsupported content role")),
        };
        let mut m = message(role.clone());
        let mut parts = vec![];
        let mut calls = vec![];
        for p in c.parts {
            check_part(&p)?;
            if let Some(text) = p.text {
                if !calls.is_empty() {
                    return Err(bad(
                        "text after function calls cannot be represented in chat messages",
                    ));
                }
                parts.push(ContentPart::Text { text });
            } else if let Some(blob) = p.inline_data {
                if role != Role::User
                    || !matches!(
                        blob.mime_type.as_str(),
                        "image/png" | "image/jpeg" | "image/webp"
                    )
                {
                    return Err(bad("unsupported inline image type or role"));
                }
                parts.push(ContentPart::ImageUrl {
                    image_url: ImageUrl {
                        url: format!("data:{};base64,{}", blob.mime_type, blob.data),
                        detail: None,
                    },
                });
            } else if let Some(c) = p.function_call {
                if role != Role::Assistant {
                    return Err(bad("function calls require model role"));
                }
                let id = c.id.clone().unwrap_or_else(|| {
                    loop {
                        let id = format!("gemini_call_{seq}");
                        seq += 1;
                        if used_ids.insert(id.clone()) {
                            break id;
                        }
                    }
                });
                if pending.iter().any(|(i, _)| i == &id) {
                    return Err(bad("duplicate tool call id"));
                }
                pending.push((id.clone(), c.name.clone()));
                calls.push(call(c, id)?);
            } else if let Some(r) = p.function_response {
                if role != Role::User || !r.response.is_object() {
                    return Err(bad("invalid function response"));
                }
                let matches: Vec<_> = pending
                    .iter()
                    .enumerate()
                    .filter(|(_, (id, name))| {
                        name == &r.name && r.id.as_ref().is_none_or(|v| v == id)
                    })
                    .map(|(i, _)| i)
                    .collect();
                if matches.len() != 1 {
                    return Err(bad("unmatched or ambiguous function response"));
                }
                let (id, _) = pending.remove(matches[0]);
                let mut t = message(Role::Tool);
                t.tool_call_id = Some(id);
                t.content = Some(Content::Text(serde_json::to_string(&r.response)?));
                if !parts.is_empty() {
                    let mut text = message(Role::User);
                    text.content = Some(Content::Parts(std::mem::take(&mut parts)));
                    messages.push(text);
                }
                messages.push(t);
            }
        }
        if !parts.is_empty() {
            m.content = Some(Content::Parts(parts));
        }
        if !calls.is_empty() {
            m.tool_calls = Some(calls);
        }
        if m.content.is_some() || m.tool_calls.is_some() {
            messages.push(m);
        }
    }
    let g = wire.generation_config.unwrap_or_default();
    let tools = wire
        .tools
        .map(|tools| {
            tools
                .into_iter()
                .flat_map(|t| t.function_declarations)
                .map(|f| {
                    if f.parameters.is_some() && f.parameters_json_schema.is_some() {
                        return Err(bad("conflicting function schemas"));
                    }
                    let parameters = match f.parameters {
                        Some(v) => Some(schema_to_json(v)?),
                        None => f.parameters_json_schema,
                    };
                    Ok(o::Tool::Function {
                        function: o::FunctionDefinition {
                            name: f.name,
                            description: f.description,
                            parameters,
                            strict: None,
                        },
                    })
                })
                .collect::<Result<Vec<_>, CodecError>>()
        })
        .transpose()?;
    let tool_choice = wire
        .tool_config
        .map(|t| {
            let c = t.function_calling_config;
            match (c.mode.as_str(), c.allowed_function_names) {
                ("AUTO", None) => Ok(o::ToolChoice::Mode(o::ToolMode::Auto)),
                ("NONE", None) => Ok(o::ToolChoice::Mode(o::ToolMode::None)),
                ("ANY", None) => Ok(o::ToolChoice::Mode(o::ToolMode::Required)),
                ("ANY", Some(names)) if names.len() == 1 => {
                    Ok(o::ToolChoice::Named(o::NamedTool::Function {
                        function: o::FunctionName {
                            name: names[0].clone(),
                        },
                    }))
                }
                _ => Err(bad("unsupported function calling configuration")),
            }
        })
        .transpose()?;
    let r = ChatRequest {
        model: model.into(),
        messages,
        stream: Some(streaming),
        generation: Generation {
            temperature: g.temperature,
            max_tokens: g.max_output_tokens,
            top_p: g.top_p,
            frequency_penalty: g.frequency_penalty,
            presence_penalty: g.presence_penalty,
            seed: g.seed,
            stop: g.stop_sequences.map(o::Stop::Multiple),
            n: g.candidate_count,
            ..Default::default()
        },
        openai: Box::new(OpenAiOptions {
            tools,
            tool_choice,
            ..Default::default()
        }),
    };
    super::openai::encode_chat(&r)?;
    if r.generation.n.is_some_and(|n| n != 1) {
        return Err(bad("only one Gemini candidate is supported"));
    }
    Ok(r)
}
// Convert only schema positions. Defaults are literal data, not nested schemas.
fn schema_to_json(mut v: Value) -> Result<Value, CodecError> {
    let obj = v
        .as_object_mut()
        .ok_or_else(|| bad("function schema must be an object"))?;
    let nullable = match obj.remove("nullable") {
        None | Some(Value::Bool(false)) => false,
        Some(Value::Bool(true)) => true,
        _ => return Err(bad("invalid nullable")),
    };
    // Gemini documents nullable independently of enum/anyOf. Do not guess
    // whether it overrides their constraints when converting to JSON Schema.
    if nullable && (obj.contains_key("enum") || obj.contains_key("anyOf")) {
        return Err(bad(
            "nullable with enum or anyOf requires parametersJsonSchema",
        ));
    }
    for (key, value) in obj.iter_mut() {
        match key.as_str() {
            "type" => {
                let kind = value
                    .as_str()
                    .ok_or_else(|| bad("invalid schema type"))?
                    .to_ascii_lowercase();
                if ![
                    "string", "number", "integer", "boolean", "array", "object", "null",
                ]
                .contains(&kind.as_str())
                {
                    return Err(bad("unsupported schema type"));
                }
                *value = Value::String(kind);
            }
            "properties" => {
                let properties = value
                    .as_object_mut()
                    .ok_or_else(|| bad("schema properties must be an object"))?;
                for property in properties.values_mut() {
                    *property = schema_to_json(property.take())?;
                }
            }
            "items" => *value = schema_to_json(value.take())?,
            "anyOf" => {
                let alternatives = value
                    .as_array_mut()
                    .filter(|v| !v.is_empty())
                    .ok_or_else(|| bad("schema anyOf must be a nonempty array"))?;
                for alternative in alternatives {
                    *alternative = schema_to_json(alternative.take())?;
                }
            }
            "minItems" | "maxItems" | "minProperties" | "maxProperties" | "minLength"
            | "maxLength" => {
                // Proto JSON encodes int64 as strings; JSON Schema requires numbers.
                // Accept ordinary nonnegative integers too, without rounding floats.
                let count = match value {
                    Value::String(s) if !s.is_empty() && s.bytes().all(|c| c.is_ascii_digit()) => {
                        s.parse::<i64>().ok()
                    }
                    _ => value.as_i64(),
                }
                .filter(|n| *n >= 0)
                .ok_or_else(|| bad("schema count must be a nonnegative int64"))?;
                *value = Value::from(count);
            }
            "required" | "enum" => {
                if value
                    .as_array()
                    .is_none_or(|values| values.iter().any(|v| !v.is_string()))
                {
                    return Err(bad("schema required and enum must contain strings"));
                }
            }
            "title" | "description" | "format" | "pattern" => {
                if !value.is_string() {
                    return Err(bad("schema annotation must be a string"));
                }
            }
            "minimum" | "maximum" => {
                if !value.is_number() {
                    return Err(bad("schema bound must be a number"));
                }
            }
            "default" => {}
            _ => {
                return Err(bad(
                    "unsupported Gemini Schema field; use parametersJsonSchema",
                ));
            }
        }
    }
    if obj.contains_key("enum") && obj.get("type").and_then(Value::as_str) != Some("string") {
        return Err(bad("Gemini Schema enum supports strings only"));
    }
    if nullable {
        let kind = obj
            .remove("type")
            .ok_or_else(|| bad("nullable schema requires type"))?;
        obj.insert(
            "type".into(),
            if kind == "null" {
                kind
            } else {
                serde_json::json!([kind, "null"])
            },
        );
    }
    Ok(v)
}
pub fn encode_chat(r: &ChatRequest) -> Result<Value, CodecError> {
    super::openai::encode_chat(r)?;
    let mut options = (*r.openai).clone();
    options.tools = None;
    options.tool_choice = None;
    options.stream_options = None;
    if options != OpenAiOptions::default()
        || r.openai
            .stream_options
            .as_ref()
            .is_some_and(|s| s.include_obfuscation.is_some())
    {
        return Err(bad("unsupported OpenAI options for Gemini"));
    }
    let g = &r.generation;
    if g.logit_bias.is_some() || g.max_completion_tokens.is_some() || g.n.is_some_and(|n| n != 1) {
        return Err(bad("unsupported generation options for Gemini"));
    }
    let mut wire = w::Request::default();
    let mut pending = BTreeMap::new();
    let mut results = vec![];
    let mut after_results = false;
    for m in &r.messages {
        if m.role != Role::Tool && !pending.is_empty() {
            return Err(bad(
                "tool results must immediately follow all calls in a batch",
            ));
        }
        if m.refusal.is_some()
            || m.audio.is_some()
            || m.name.is_some()
            || (m.role != Role::Tool && m.tool_call_id.is_some())
        {
            return Err(bad("unsupported message metadata"));
        }
        let mut parts = content_parts(&m.content, m.role == Role::User)?;
        if m.role == Role::Tool {
            if parts.len() > 1 {
                return Err(bad(
                    "multiple tool result text parts cannot be represented in Gemini",
                ));
            }
            let id = m
                .tool_call_id
                .as_ref()
                .ok_or_else(|| bad("missing tool id"))?;
            let name = pending
                .remove(id)
                .ok_or_else(|| bad("unknown tool response id"))?;
            // An empty or single text part maps to one object without joining blocks.
            let text = parts.first().and_then(|p| p.text.as_deref()).unwrap_or("");
            let response = match serde_json::from_str::<Value>(text) {
                Ok(v) if v.is_object() => v,
                _ => serde_json::json!({"result":text}),
            };
            results.push(w::Part {
                function_response: Some(w::FunctionResponse {
                    id: Some(id.clone()),
                    name,
                    response,
                }),
                ..Default::default()
            });
            if pending.is_empty() {
                wire.contents.push(w::Content {
                    role: Some("user".into()),
                    parts: std::mem::take(&mut results),
                });
                after_results = true;
            }
            continue;
        }
        if let Some(calls) = &m.tool_calls {
            for ToolCall::Function { id, function } in calls {
                let args: Value = serde_json::from_str(&function.arguments)?;
                if !args.is_object()
                    || id.is_empty()
                    || function.name.is_empty()
                    || pending.insert(id.clone(), function.name.clone()).is_some()
                {
                    return Err(bad("invalid function call"));
                }
                parts.push(w::Part {
                    function_call: Some(w::FunctionCall {
                        id: Some(id.clone()),
                        name: function.name.clone(),
                        args,
                    }),
                    ..Default::default()
                });
            }
        }
        match m.role {
            Role::System => {
                if !wire.contents.is_empty() {
                    return Err(bad("system instruction must precede conversation"));
                }
                wire.system_instruction
                    .get_or_insert_with(Default::default)
                    .parts
                    .extend(parts);
            }
            Role::Developer => return Err(bad("developer role is not representable in Gemini")),
            _ => {
                if m.role == Role::User && after_results {
                    wire.contents.last_mut().unwrap().parts.extend(parts);
                } else {
                    wire.contents.push(w::Content {
                        role: Some(
                            if m.role == Role::Assistant {
                                "model"
                            } else {
                                "user"
                            }
                            .into(),
                        ),
                        parts,
                    });
                }
                after_results = false;
            }
        }
    }
    if !pending.is_empty() {
        return Err(bad("missing tool results"));
    }
    if wire.contents.is_empty() {
        return Err(bad("conversation must be nonempty"));
    }
    wire.generation_config = Some(w::GenerationConfig {
        temperature: g.temperature,
        max_output_tokens: g.max_tokens,
        top_p: g.top_p,
        frequency_penalty: g.frequency_penalty,
        presence_penalty: g.presence_penalty,
        seed: g.seed,
        candidate_count: g.n,
        stop_sequences: g.stop.as_ref().map(|s| match s {
            o::Stop::Single(s) => vec![s.clone()],
            o::Stop::Multiple(v) => v.clone(),
        }),
    });
    wire.tools = r
        .openai
        .tools
        .as_ref()
        .map(|ts| {
            ts.iter()
                .map(|o::Tool::Function { function: f }| {
                    if f.strict == Some(true) {
                        return Err(bad("strict tools unsupported"));
                    }
                    Ok(w::FunctionDeclaration {
                        name: f.name.clone(),
                        description: f.description.clone(),
                        parameters: None,
                        parameters_json_schema: f.parameters.clone(),
                    })
                })
                .collect::<Result<Vec<_>, CodecError>>()
                .map(|function_declarations| {
                    vec![w::Tool {
                        function_declarations,
                    }]
                })
        })
        .transpose()?;
    wire.tool_config = r.openai.tool_choice.as_ref().map(|c| {
        let (mode, names) = match c {
            o::ToolChoice::Mode(m) => (
                match m {
                    o::ToolMode::Auto => "AUTO",
                    o::ToolMode::None => "NONE",
                    o::ToolMode::Required => "ANY",
                },
                None,
            ),
            o::ToolChoice::Named(o::NamedTool::Function { function }) => {
                ("ANY", Some(vec![function.name.clone()]))
            }
        };
        w::ToolConfig {
            function_calling_config: w::FunctionCallingConfig {
                mode: mode.into(),
                allowed_function_names: names,
            },
        }
    });
    Ok(serde_json::to_value(wire)?)
}
fn stop(s: &str, tools: bool) -> Result<String, CodecError> {
    Ok(match s {
        "STOP" => {
            if tools {
                "tool_calls"
            } else {
                "stop"
            }
        }
        "MAX_TOKENS" => "length",
        "SAFETY" | "RECITATION" | "BLOCKLIST" | "PROHIBITED_CONTENT" | "SPII" => "content_filter",
        _ => return Err(bad("unsupported or failed Gemini finish reason")),
    }
    .into())
}
fn native_stop(s: &str) -> Result<String, CodecError> {
    Ok(match s {
        "stop" | "tool_calls" => "STOP",
        "length" => "MAX_TOKENS",
        "content_filter" => "SAFETY",
        _ => return Err(bad("unsupported finish reason")),
    }
    .into())
}
fn usage(u: w::Usage) -> Result<Usage, CodecError> {
    let prompt_tokens = u.prompt_token_count.unwrap_or(0);
    // Gemini candidates exclude thoughts; the IR completion count includes them.
    let completion_tokens = u
        .candidates_token_count
        .unwrap_or(0)
        .checked_add(u.thoughts_token_count.unwrap_or(0))
        .ok_or_else(|| bad("token count overflow"))?;
    let total_tokens = prompt_tokens
        .checked_add(completion_tokens)
        .ok_or_else(|| bad("token count overflow"))?;
    if u.total_token_count.is_some_and(|n| n != total_tokens)
        || u.cached_content_token_count
            .is_some_and(|n| n > prompt_tokens)
    {
        return Err(bad("inconsistent token counts"));
    }
    Ok(Usage {
        prompt_tokens,
        completion_tokens,
        total_tokens,
        cache_creation: None,
        prompt_tokens_details: u.cached_content_token_count.map(|n| PromptTokensDetails {
            cached_tokens: Some(n),
            audio_tokens: None,
        }),
        completion_tokens_details: u.thoughts_token_count.map(|n| CompletionTokensDetails {
            reasoning_tokens: Some(n),
            audio_tokens: None,
            accepted_prediction_tokens: None,
            rejected_prediction_tokens: None,
        }),
    })
}
fn native_usage(u: &Usage) -> Result<w::Usage, CodecError> {
    if u.cache_creation.is_some() {
        return Err(bad("Gemini cannot represent cache creation usage"));
    }
    if u.prompt_tokens_details
        .as_ref()
        .is_some_and(|d| d.audio_tokens.is_some())
        || u.completion_tokens_details.as_ref().is_some_and(|d| {
            d.audio_tokens.is_some()
                || d.accepted_prediction_tokens.is_some()
                || d.rejected_prediction_tokens.is_some()
        })
    {
        return Err(bad("unsupported token usage details"));
    }
    let reasoning = u
        .completion_tokens_details
        .as_ref()
        .and_then(|d| d.reasoning_tokens)
        .unwrap_or(0);
    let candidates = u
        .completion_tokens
        .checked_sub(reasoning)
        .ok_or_else(|| bad("reasoning tokens exceed completion count"))?;
    if u.prompt_tokens.checked_add(u.completion_tokens) != Some(u.total_tokens)
        || u.prompt_tokens_details
            .as_ref()
            .and_then(|d| d.cached_tokens)
            .is_some_and(|n| n > u.prompt_tokens)
    {
        return Err(bad("inconsistent token counts"));
    }
    Ok(w::Usage {
        prompt_token_count: Some(u.prompt_tokens),
        candidates_token_count: Some(candidates),
        total_token_count: Some(u.total_tokens),
        cached_content_token_count: u
            .prompt_tokens_details
            .as_ref()
            .and_then(|d| d.cached_tokens),
        thoughts_token_count: u
            .completion_tokens_details
            .as_ref()
            .and_then(|d| d.reasoning_tokens),
    })
}
pub fn decode_chat_response(v: Value) -> Result<ChatResponse, CodecError> {
    let w: w::Response = serde_json::from_value(v)?;
    let cs = w.candidates.ok_or_else(|| bad("missing candidates"))?;
    if cs.len() != 1 {
        return Err(bad("expected one candidate"));
    }
    let mut choices = vec![];
    for c in cs {
        let mut text = String::new();
        let mut calls = vec![];
        if let Some(content) = c.content {
            if content.role.as_deref().is_some_and(|r| r != "model") {
                return Err(bad("invalid response role"));
            }
            for p in content.parts {
                check_part(&p)?;
                if let Some(t) = p.text {
                    if !calls.is_empty() {
                        return Err(bad(
                            "text after function calls cannot be represented in chat messages",
                        ));
                    }
                    text.push_str(&t);
                } else if let Some(c) = p.function_call {
                    let id =
                        c.id.clone()
                            .unwrap_or_else(|| format!("gemini_call_{}", calls.len()));
                    calls.push(call(c, id)?);
                } else {
                    return Err(bad("function response in model output"));
                }
            }
        }
        let finish = stop(
            c.finish_reason
                .as_deref()
                .ok_or_else(|| bad("missing finish reason"))?,
            !calls.is_empty(),
        )?;
        choices.push(Choice {
            index: c.index.unwrap_or(0),
            message: ResponseMessage {
                role: Role::Assistant,
                content: (!text.is_empty()).then_some(Content::Text(text)),
                tool_calls: (!calls.is_empty()).then_some(calls),
                refusal: None,
                audio: None,
            },
            finish_reason: Some(finish),
            logprobs: None,
        });
    }
    Ok(ChatResponse {
        id: w.response_id.unwrap_or_default(),
        object: "chat.completion".into(),
        created: 0,
        model: w.model_version.unwrap_or_default(),
        choices,
        usage: w.usage_metadata.map(usage).transpose()?,
        system_fingerprint: None,
        service_tier: None,
    })
}
pub fn encode_chat_response(r: &ChatResponse) -> Result<Value, CodecError> {
    if r.choices.len() != 1 || r.service_tier.is_some() || r.system_fingerprint.is_some() {
        return Err(bad("unsupported response metadata or choices"));
    }
    let mut candidates = vec![];
    for c in &r.choices {
        if c.message.role != Role::Assistant
            || c.message.refusal.is_some()
            || c.message.audio.is_some()
            || c.logprobs.is_some()
        {
            return Err(bad("unsupported response payload"));
        }
        let mut parts = content_parts(&c.message.content, false)?;
        if let Some(calls) = &c.message.tool_calls {
            for ToolCall::Function { id, function } in calls {
                let args: Value = serde_json::from_str(&function.arguments)?;
                if !args.is_object() {
                    return Err(bad("function arguments must be object"));
                }
                parts.push(w::Part {
                    function_call: Some(w::FunctionCall {
                        id: Some(id.clone()),
                        name: function.name.clone(),
                        args,
                    }),
                    ..Default::default()
                });
            }
        }
        candidates.push(w::Candidate {
            content: Some(w::Content {
                role: Some("model".into()),
                parts,
            }),
            finish_reason: Some(native_stop(
                c.finish_reason
                    .as_deref()
                    .ok_or_else(|| bad("missing finish reason"))?,
            )?),
            index: Some(c.index),
        });
    }
    Ok(serde_json::to_value(w::Response {
        candidates: Some(candidates),
        usage_metadata: r.usage.as_ref().map(native_usage).transpose()?,
        model_version: Some(r.model.clone()),
        response_id: Some(r.id.clone()),
    })?)
}
const DEFAULT_LIMIT: usize = 1024 * 1024;
pub struct StreamDecoder {
    terminal: bool,
    failed: bool,
    done: bool,
    tools: u32,
    max_bytes: usize,
}
impl Default for StreamDecoder {
    fn default() -> Self {
        Self::new()
    }
}
impl StreamDecoder {
    pub fn new() -> Self {
        Self::with_limit(DEFAULT_LIMIT)
    }
    pub fn with_limit(max_bytes: usize) -> Self {
        Self {
            max_bytes,
            terminal: false,
            failed: false,
            done: false,
            tools: 0,
        }
    }
    pub fn push(
        &mut self,
        event: &nyro_protocol::framing::Event,
    ) -> Result<Vec<ChatEvent>, CodecError> {
        let result = self.push_inner(event);
        if result.is_err() {
            self.failed = true;
        }
        result
    }
    fn push_inner(
        &mut self,
        event: &nyro_protocol::framing::Event,
    ) -> Result<Vec<ChatEvent>, CodecError> {
        if self.failed
            || self.done
            || event.data.len() > self.max_bytes
            || event.event.as_deref().is_some_and(|e| e != "message")
        {
            return Err(bad("invalid Gemini stream event"));
        }
        let w: w::Response = serde_json::from_str(&event.data)?;
        let mut choices = vec![];
        for c in w.candidates.unwrap_or_default() {
            if self.terminal || c.index.unwrap_or(0) != 0 || !choices.is_empty() {
                return Err(bad("unexpected stream candidate"));
            }
            let mut delta = Delta {
                role: Some(Role::Assistant),
                ..Default::default()
            };
            let mut text = String::new();
            let mut calls = vec![];
            if let Some(content) = c.content {
                if content.role.as_deref().is_some_and(|r| r != "model") {
                    return Err(bad("invalid response role"));
                }
                for p in content.parts {
                    check_part(&p)?;
                    if let Some(t) = p.text {
                        if self.tools > 0 {
                            return Err(bad("text after streamed tool calls is unsupported"));
                        }
                        text.push_str(&t);
                    } else if let Some(c) = p.function_call {
                        let id =
                            c.id.clone()
                                .unwrap_or_else(|| format!("gemini_call_{}", self.tools));
                        let ToolCall::Function { id, function } = call(c, id)?;
                        calls.push(ToolCallDelta {
                            index: self.tools,
                            id: Some(id),
                            r#type: Some(FunctionType::Function),
                            function: Some(FunctionDelta {
                                name: Some(function.name),
                                arguments: Some(function.arguments),
                            }),
                        });
                        self.tools = self
                            .tools
                            .checked_add(1)
                            .ok_or_else(|| bad("too many tool calls"))?;
                    } else {
                        return Err(bad("unexpected function response"));
                    }
                }
            }
            delta.content = (!text.is_empty()).then_some(text);
            delta.tool_calls = (!calls.is_empty()).then_some(calls);
            let finish = c
                .finish_reason
                .as_deref()
                .map(|s| stop(s, self.tools > 0))
                .transpose()?;
            if finish.is_some() {
                self.terminal = true;
            }
            choices.push(StreamChoice {
                index: 0,
                delta,
                finish_reason: finish,
                logprobs: None,
            });
        }
        if choices.is_empty() && w.usage_metadata.is_none() {
            return Err(bad("empty Gemini stream event"));
        }
        Ok(vec![ChatEvent::Chunk(Box::new(ChatChunk {
            id: w.response_id.unwrap_or_default(),
            object: "chat.completion.chunk".into(),
            created: 0,
            model: w.model_version.unwrap_or_default(),
            choices,
            usage: w.usage_metadata.map(usage).transpose()?,
            system_fingerprint: None,
            service_tier: None,
            obfuscation: None,
        }))])
    }
    pub fn finish(&mut self) -> Result<Vec<ChatEvent>, CodecError> {
        if self.failed || !self.terminal || self.done {
            self.failed = true;
            return Err(bad("Gemini stream ended without terminal finish reason"));
        }
        self.done = true;
        Ok(vec![ChatEvent::Done])
    }
}
#[derive(Default)]
struct PendingCall {
    id: String,
    name: String,
    args: String,
}
pub struct StreamEncoder {
    pending_finish: Option<String>,
    model: String,
    calls: BTreeMap<u32, PendingCall>,
    bytes: usize,
    max_bytes: usize,
    terminal: bool,
    done: bool,
    failed: bool,
}
impl StreamEncoder {
    pub fn new(public_model: String) -> Self {
        Self::with_limit(public_model, DEFAULT_LIMIT)
    }
    pub fn with_limit(public_model: String, max_bytes: usize) -> Self {
        Self {
            model: public_model,
            pending_finish: None,
            calls: BTreeMap::new(),
            bytes: 0,
            max_bytes,
            terminal: false,
            done: false,
            failed: false,
        }
    }
    pub fn push(&mut self, event: &ChatEvent) -> Result<String, CodecError> {
        let result = self.push_inner(event);
        if result.is_err() {
            self.failed = true;
        }
        result
    }
    fn push_inner(&mut self, event: &ChatEvent) -> Result<String, CodecError> {
        if self.failed || self.done {
            return Err(bad("Gemini encoder already ended"));
        }
        let ChatEvent::Chunk(c) = event else {
            if !self.terminal || !self.calls.is_empty() {
                return Err(bad("stream ended without completed candidate"));
            }
            self.done = true;
            let response = w::Response {
                candidates: Some(vec![w::Candidate {
                    finish_reason: self.pending_finish.take(),
                    index: Some(0),
                    content: None,
                }]),
                model_version: Some(self.model.clone()),
                ..Default::default()
            };
            return Ok(format!("data: {}\n\n", serde_json::to_string(&response)?));
        };
        if c.choices.len() > 1
            || c.service_tier.is_some()
            || c.system_fingerprint.is_some()
            || c.obfuscation.is_some()
        {
            return Err(bad("unsupported stream metadata or choices"));
        }
        let mut candidates = vec![];
        for choice in &c.choices {
            if self.terminal
                || choice.index != 0
                || choice.logprobs.is_some()
                || choice.delta.refusal.is_some()
                || choice
                    .delta
                    .role
                    .as_ref()
                    .is_some_and(|r| *r != Role::Assistant)
            {
                return Err(bad("invalid stream choice"));
            }
            let mut parts = vec![];
            if let Some(text) = &choice.delta.content {
                if !self.calls.is_empty() {
                    return Err(bad("text after streamed tool calls is unsupported"));
                }
                parts.push(w::Part {
                    text: Some(text.clone()),
                    ..Default::default()
                });
            }
            if let Some(calls) = &choice.delta.tool_calls {
                for d in calls {
                    let added = if self.calls.contains_key(&d.index) {
                        0
                    } else {
                        std::mem::size_of::<PendingCall>() + std::mem::size_of::<u32>()
                    };
                    let added = added
                        + d.id.as_ref().map_or(0, String::len)
                        + d.function.as_ref().map_or(0, |f| {
                            f.name.as_ref().map_or(0, String::len)
                                + f.arguments.as_ref().map_or(0, String::len)
                        });
                    self.bytes = self
                        .bytes
                        .checked_add(added)
                        .ok_or_else(|| bad("tool state limit"))?;
                    if self.bytes > self.max_bytes
                        || self.calls.len() >= self.max_bytes && !self.calls.contains_key(&d.index)
                    {
                        return Err(bad("tool arguments exceed limit"));
                    }
                    let call = self.calls.entry(d.index).or_default();
                    if let Some(id) = &d.id {
                        if !call.id.is_empty() {
                            return Err(bad("duplicate tool id"));
                        }
                        call.id = id.clone();
                    }
                    if let Some(f) = &d.function {
                        if let Some(name) = &f.name {
                            if !call.name.is_empty() {
                                return Err(bad("duplicate tool name"));
                            }
                            call.name = name.clone();
                        }
                        if let Some(args) = &f.arguments {
                            call.args.push_str(args);
                        }
                    }
                }
            }
            let finish = choice
                .finish_reason
                .as_deref()
                .map(native_stop)
                .transpose()?;
            if finish.is_some() {
                for (_, call) in std::mem::take(&mut self.calls) {
                    let args: Value = serde_json::from_str(&call.args)?;
                    if !args.is_object() || call.id.is_empty() || call.name.is_empty() {
                        return Err(bad("incomplete tool call"));
                    }
                    parts.push(w::Part {
                        function_call: Some(w::FunctionCall {
                            id: Some(call.id),
                            name: call.name,
                            args,
                        }),
                        ..Default::default()
                    });
                }
                self.bytes = 0;
                self.terminal = true;
                self.pending_finish = finish.clone();
            }
            if !parts.is_empty() {
                candidates.push(w::Candidate {
                    content: (!parts.is_empty()).then_some(w::Content {
                        role: Some("model".into()),
                        parts,
                    }),
                    finish_reason: None,
                    index: Some(0),
                });
            }
        }
        let usage_metadata = c.usage.as_ref().map(native_usage).transpose()?;
        if candidates.is_empty() && usage_metadata.is_none() {
            return Ok(String::new());
        }
        let response = w::Response {
            candidates: (!candidates.is_empty()).then_some(candidates),
            usage_metadata,
            model_version: Some(self.model.clone()),
            response_id: Some(c.id.clone()),
        };
        Ok(format!("data: {}\n\n", serde_json::to_string(&response)?))
    }
}
