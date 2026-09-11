//! Stateless text and client function Responses conversion.
use super::{CodecError, invalid as bad};
use crate::ir::*;
use nyro_protocol::openai::{chat, responses as wire};
use serde_json::{Value, json};
use std::collections::BTreeSet;
mod stream;
pub use stream::{StreamDecoder, StreamEncoder};

fn message(role: Role, content: Option<Content>) -> Message {
    Message {
        role,
        content,
        tool_calls: None,
        tool_call_id: None,
        tool_error: false,
        name: None,
        refusal: None,
        audio: None,
    }
}
fn nonempty(s: &str) -> Result<(), CodecError> {
    if s.trim().is_empty() {
        Err(bad("Responses identity/name must be nonempty"))
    } else {
        Ok(())
    }
}
fn history_status(status: Option<&str>) -> Result<(), CodecError> {
    if status.is_some_and(|s| !matches!(s, "completed" | "incomplete")) {
        return Err(bad("unfinished Responses history item"));
    }
    Ok(())
}
pub fn decode_chat(value: Value) -> Result<ChatRequest, CodecError> {
    let r: wire::Request = serde_json::from_value(value)?;
    if r.store == Some(true)
        || r.background == Some(true)
        || r.truncation.as_deref().is_some_and(|s| s != "disabled")
        || r.include.as_ref().is_some_and(|v| !v.is_empty())
    {
        return Err(bad(
            "Responses state, background, includes and truncation are unsupported",
        ));
    }
    if r.stream_options
        .as_ref()
        .is_some_and(|o| o.include_obfuscation == Some(true))
    {
        return Err(bad("Responses obfuscation is unsupported"));
    }
    if r.stream_options.is_some() && r.stream != Some(true) {
        return Err(bad("stream_options requires streaming"));
    }
    let mut messages = Vec::new();
    if let Some(instructions) = r.instructions {
        messages.push(message(Role::System, Some(Content::Text(instructions))));
    }
    match r.input {
        wire::Input::Text(text) => messages.push(message(Role::User, Some(Content::Text(text)))),
        wire::Input::Items(items) => {
            for item in items {
                match item {
                    wire::InputItem::Message(m) => {
                        if m.r#type.as_deref().is_some_and(|s| s != "message") {
                            return Err(bad("unsupported Responses input item"));
                        }
                        history_status(m.status.as_deref())?;
                        let role = match m.role.as_str() {
                            "system" => Role::System,
                            "developer" => Role::Developer,
                            "user" => Role::User,
                            "assistant" => Role::Assistant,
                            _ => return Err(bad("invalid Responses message role")),
                        };
                        let content = match m.content {
                            wire::InputContent::Text(text) => Content::Text(text),
                            wire::InputContent::Parts(parts) => {
                                if parts.is_empty() {
                                    return Err(bad("Responses message content must be nonempty"));
                                }
                                Content::Parts(
                                    parts
                                        .into_iter()
                                        .map(|part| match part {
                                            wire::InputPart::InputImage {
                                                image_url,
                                                detail,
                                                prompt_cache_breakpoint,
                                            } if role == Role::User => Ok(ContentPart::ImageUrl {
                                                prompt_cache_breakpoint,
                                                image_url: ImageUrl {
                                                    url: image_url,
                                                    detail,
                                                },
                                            }),
                                            wire::InputPart::InputText {
                                                text,
                                                prompt_cache_breakpoint,
                                            } => Ok(ContentPart::Text {
                                                text,
                                                prompt_cache_breakpoint,
                                            }),
                                            wire::InputPart::OutputText {
                                                text,
                                                annotations,
                                                logprobs,
                                            } if role == Role::Assistant
                                                && annotations.is_empty()
                                                && logprobs.is_empty() =>
                                            {
                                                Ok(ContentPart::Text {
                                                    text,
                                                    prompt_cache_breakpoint: None,
                                                })
                                            }
                                            wire::InputPart::Refusal { refusal }
                                                if role == Role::Assistant =>
                                            {
                                                Ok(ContentPart::Refusal { refusal })
                                            }
                                            _ => Err(bad(
                                                "unsupported Responses content/annotations",
                                            )),
                                        })
                                        .collect::<Result<_, _>>()?,
                                )
                            }
                        };
                        messages.push(message(role, Some(content)));
                    }
                    wire::InputItem::Item(wire::HistoryItem::FunctionCall {
                        call_id,
                        name,
                        arguments,
                        status,
                        ..
                    }) => {
                        nonempty(&call_id)?;
                        nonempty(&name)?;
                        history_status(status.as_deref())?;
                        if messages.last().is_none_or(|m| m.role != Role::Assistant) {
                            messages.push(message(Role::Assistant, None));
                        }
                        messages
                            .last_mut()
                            .unwrap()
                            .tool_calls
                            .get_or_insert_with(Vec::new)
                            .push(ToolCall::Function {
                                id: call_id,
                                function: FunctionCall { name, arguments },
                            });
                    }
                    wire::InputItem::Item(wire::HistoryItem::FunctionCallOutput {
                        call_id,
                        output,
                        status,
                        ..
                    }) => {
                        nonempty(&call_id)?;
                        history_status(status.as_deref())?;
                        let content = match output {
                            wire::FunctionOutput::Text(text) => Content::Text(text),
                            wire::FunctionOutput::Parts(parts) => Content::Parts(
                                parts
                                    .into_iter()
                                    .map(
                                        |wire::FunctionOutputPart::InputText {
                                             text,
                                             prompt_cache_breakpoint,
                                         }| {
                                            ContentPart::Text {
                                                text,
                                                prompt_cache_breakpoint,
                                            }
                                        },
                                    )
                                    .collect(),
                            ),
                        };
                        let mut m = message(Role::Tool, Some(content));
                        m.tool_call_id = Some(call_id);
                        messages.push(m);
                    }
                }
            }
        }
    }
    let tools = r.tools.map(|tools| tools.into_iter().map(|tool| {
        let wire::FunctionTool::Function { name, description, parameters, strict } = tool;
        nonempty(&name)?;
        let strict = match strict { Some(true)=>Some(true), Some(false)=>None, None=>return Err(bad("Responses function tools require explicit strict:true or strict:false")) };
        if parameters.as_ref().is_some_and(|v| !v.is_object()) { return Err(bad("function parameters must be an object")); }
        Ok(chat::Tool::Function { function: chat::FunctionDefinition { name, description, parameters, strict } })
    }).collect::<Result<_,CodecError>>()).transpose()?;
    let tool_choice = r.tool_choice.map(|c| match c {
        wire::ToolChoice::Mode(m) => chat::ToolChoice::Mode(m),
        wire::ToolChoice::Named(wire::NamedTool::Function { name }) => {
            chat::ToolChoice::Named(chat::NamedTool::Function {
                function: chat::FunctionName { name },
            })
        }
    });
    let response_format = r.text.map(|t| match t.format {
        wire::TextFormat::Text => chat::ResponseFormat::Text,
        wire::TextFormat::JsonObject => chat::ResponseFormat::JsonObject,
        wire::TextFormat::JsonSchema {
            name,
            schema,
            strict,
            description,
        } => chat::ResponseFormat::JsonSchema {
            json_schema: chat::JsonSchema {
                name,
                schema,
                strict,
                description,
            },
        },
    });
    let request = ChatRequest {
        model: r.model,
        messages,
        stream: r.stream,
        generation: Generation {
            max_tokens: r.max_output_tokens,
            temperature: r.temperature,
            top_p: r.top_p,
            ..Default::default()
        },
        openai: Box::new(OpenAiOptions {
            tools,
            tool_choice,
            parallel_tool_calls: r.parallel_tool_calls,
            response_format,
            metadata: r.metadata,
            service_tier: r.service_tier,
            user: r.user,
            safety_identifier: r.safety_identifier,
            prompt_cache_key: r.prompt_cache_key,
            prompt_cache_retention: r.prompt_cache_retention,
            prompt_cache_options: r.prompt_cache_options,
            ..Default::default()
        }),
    };
    super::validate_chat(&request)?;
    Ok(request)
}
fn content_parts(
    content: &Content,
    assistant: bool,
    images: bool,
    input: bool,
) -> Result<Vec<Value>, CodecError> {
    // A marked assistant message uses EasyInputMessage's input content list
    // as a whole; mixing input_text with output_text/refusal is not that shape.
    let assistant = assistant
        && !(input
            && matches!(content, Content::Parts(parts) if parts.iter().any(|p|
                matches!(p, ContentPart::Text { prompt_cache_breakpoint: Some(_), .. })
            )));
    match content {
        Content::Text(text) => Ok(vec![if assistant {
            json!({"type":"output_text","text":text,"annotations":[]})
        } else {
            json!({"type":"input_text","text":text})
        }]),
        Content::Parts(parts) => parts
            .iter()
            .map(|p| match p {
                ContentPart::Text {
                    text,
                    prompt_cache_breakpoint,
                } => {
                    if !input && prompt_cache_breakpoint.is_some() {
                        return Err(bad(
                            "cache breakpoints are input controls, not generated output",
                        ));
                    }
                    // EasyInputMessage accepts assistant input_text history. Generated
                    // output_text has no breakpoint field; never place one on it.
                    let mut part = if assistant && prompt_cache_breakpoint.is_none() {
                        json!({"type":"output_text","text":text,"annotations":[]})
                    } else {
                        json!({"type":"input_text","text":text})
                    };
                    if let Some(marker) = prompt_cache_breakpoint {
                        part["prompt_cache_breakpoint"] = json!(marker);
                    }
                    Ok(part)
                }
                ContentPart::ImageUrl {
                    image_url,
                    prompt_cache_breakpoint,
                } if images => {
                    super::super::image::source(image_url)?;
                    let mut part = json!({"type":"input_image","image_url":image_url.url});
                    if let Some(detail) = &image_url.detail {
                        part["detail"] = json!(detail);
                    }
                    if let Some(marker) = prompt_cache_breakpoint {
                        part["prompt_cache_breakpoint"] = json!(marker);
                    }
                    Ok(part)
                }
                ContentPart::Refusal { refusal } if assistant => {
                    Ok(json!({"type":"refusal","refusal":refusal}))
                }
                _ => Err(bad("Responses supports text/refusal content only")),
            })
            .collect(),
    }
}
pub fn encode_chat(r: &ChatRequest) -> Result<Value, CodecError> {
    super::validate_chat(r)?;
    let g = &r.generation;
    let o = &r.openai;
    if g.frequency_penalty.is_some()
        || g.presence_penalty.is_some()
        || g.seed.is_some()
        || g.stop.is_some()
        || g.logit_bias.is_some()
        || g.n.is_some_and(|n| n != 1)
        || (g.max_tokens.is_some()
            && g.max_completion_tokens.is_some()
            && g.max_tokens != g.max_completion_tokens)
        || o.audio.is_some()
        || o.prediction.is_some()
        || o.reasoning_effort.is_some()
        || o.store == Some(true)
        || o.modalities
            .as_ref()
            .is_some_and(|m| m.iter().any(|v| !matches!(v, chat::Modality::Text)))
    {
        return Err(bad(
            "request options cannot be represented in stateless Responses",
        ));
    }
    let mut input = Vec::new();
    for m in &r.messages {
        if m.tool_error {
            return Err(bad("Responses cannot represent tool result error status"));
        }
        if m.name.is_some()
            || m.audio.is_some()
            || (m.role != Role::Tool && m.tool_call_id.is_some())
        {
            return Err(bad("unsupported Responses message metadata"));
        }
        if m.role == Role::Tool {
            let output = match &m.content {
                Some(Content::Text(s)) => json!(s),
                Some(content @ Content::Parts(_)) => {
                    json!(content_parts(content, false, false, true)?)
                }
                None => return Err(bad("function result requires text")),
            };
            if m.refusal.is_some() {
                return Err(bad("function result cannot contain refusal"));
            }
            input.push(
                json!({"type":"function_call_output","call_id":m.tool_call_id,"output":output}),
            );
            continue;
        }
        let mut parts = m
            .content
            .as_ref()
            .map(|c| content_parts(c, m.role == Role::Assistant, m.role == Role::User, true))
            .transpose()?
            .unwrap_or_default();
        if let Some(refusal) = &m.refusal {
            if m.role != Role::Assistant {
                return Err(bad("refusal requires assistant role"));
            }
            if parts.iter().any(|p| p["type"] == "input_text") {
                return Err(bad("marked assistant input cannot contain refusal"));
            }
            parts.push(json!({"type":"refusal","refusal":refusal}));
        }
        if !parts.is_empty() {
            input.push(json!({"type":"message","role":m.role,"content":parts}));
        }
        for ToolCall::Function { id, function } in m.tool_calls.iter().flatten() {
            nonempty(id)?;
            nonempty(&function.name)?;
            input.push(json!({"type":"function_call","call_id":id,"name":function.name,"arguments":function.arguments}));
        }
    }
    let mut v = json!({"model":r.model,"input":input,"store":false});
    let map = v.as_object_mut().unwrap();
    for (key, value) in [
        ("stream", json!(r.stream)),
        ("temperature", json!(g.temperature)),
        ("top_p", json!(g.top_p)),
        (
            "max_output_tokens",
            json!(g.max_completion_tokens.or(g.max_tokens)),
        ),
        ("parallel_tool_calls", json!(o.parallel_tool_calls)),
        ("metadata", json!(o.metadata)),
        ("service_tier", json!(o.service_tier)),
        ("user", json!(o.user)),
        ("safety_identifier", json!(o.safety_identifier)),
        ("prompt_cache_key", json!(o.prompt_cache_key)),
        ("prompt_cache_retention", json!(o.prompt_cache_retention)),
        ("prompt_cache_options", json!(o.prompt_cache_options)),
    ] {
        if !value.is_null() {
            map.insert(key.into(), value);
        }
    }
    if r.stream == Some(true) {
        v["stream_options"] = json!({"include_obfuscation":false});
    }
    if let Some(tools) = &o.tools {
        v["tools"] = Value::Array(
            tools
                .iter()
                .map(|t| {
                    let chat::Tool::Function { function } = t;
                    nonempty(&function.name)?;
                    if function.parameters.as_ref().is_some_and(|v| !v.is_object()) {
                        return Err(bad("function parameters must be an object"));
                    }
                    Ok(serde_json::to_value(wire::FunctionTool::Function {
                        name: function.name.clone(),
                        description: function.description.clone(),
                        parameters: function.parameters.clone(),
                        strict: Some(function.strict.unwrap_or(false)),
                    })?)
                })
                .collect::<Result<_, CodecError>>()?,
        );
    }
    if let Some(choice) = &o.tool_choice {
        v["tool_choice"] = match choice {
            chat::ToolChoice::Mode(mode) => json!(mode),
            chat::ToolChoice::Named(chat::NamedTool::Function { function }) => {
                nonempty(&function.name)?;
                json!({"type":"function","name":function.name})
            }
        };
    }
    if let Some(format) = &o.response_format {
        v["text"] = json!({"format":match format {
            chat::ResponseFormat::Text=>json!({"type":"text"}),
            chat::ResponseFormat::JsonObject=>json!({"type":"json_object"}),
            chat::ResponseFormat::JsonSchema { json_schema }=>{
                let mut f=serde_json::to_value(json_schema)?; f["type"]=json!("json_schema"); f
            }
        }});
    }
    Ok(v)
}

fn validate_envelope(r: &wire::Response) -> Result<(), CodecError> {
    nonempty(&r.id)?;
    nonempty(&r.model)?;
    if r.object != "response" || r.error.is_some() {
        return Err(bad("invalid/failed Responses envelope"));
    }
    for (k, v) in &r.extra {
        let valid = match k.as_str() {
            "store" | "background" => v.is_null() || v == false,
            "previous_response_id" | "conversation" => v.is_null(),
            "reasoning" => {
                v.is_null()
                    || v.as_object().is_some_and(|m| {
                        m.iter()
                            .all(|(k, v)| matches!(k.as_str(), "effort" | "summary") && v.is_null())
                    })
            }
            "truncation" => v.is_null() || v == "disabled",
            "tools" => serde_json::from_value::<Vec<wire::FunctionTool>>(v.clone()).is_ok(),
            "text" => {
                v.is_null() || {
                    let mut text = v.clone();
                    let verbosity = text.as_object_mut().and_then(|m| m.remove("verbosity"));
                    verbosity.as_ref().is_none_or(|v| {
                        v.is_null() || matches!(v.as_str(), Some("low" | "medium" | "high"))
                    }) && serde_json::from_value::<wire::TextConfig>(text).is_ok()
                }
            }
            "tool_choice" => {
                v.is_null() || serde_json::from_value::<wire::ToolChoice>(v.clone()).is_ok()
            }
            "metadata" => {
                v.is_null()
                    || serde_json::from_value::<std::collections::BTreeMap<String, String>>(
                        v.clone(),
                    )
                    .is_ok()
            }
            "temperature" | "top_p" => v.is_null() || v.is_number(),
            "max_output_tokens" | "max_tool_calls" | "completed_at" | "top_logprobs" => {
                v.is_null() || v.as_u64().is_some()
            }
            "parallel_tool_calls" => v.is_null() || v.is_boolean(),
            "prompt_cache_options" => {
                v.is_null()
                    || serde_json::from_value::<nyro_protocol::openai::PromptCacheOptions>(
                        v.clone(),
                    )
                    .is_ok()
            }
            "instructions"
            | "service_tier"
            | "user"
            | "safety_identifier"
            | "prompt_cache_key"
            | "prompt_cache_retention" => v.is_null() || v.is_string(),
            _ => false,
        };
        if !valid {
            return Err(bad("unsupported Responses envelope field"));
        }
    }
    Ok(())
}
fn validate_part(part: &wire::OutputPart) -> Result<(), CodecError> {
    if matches!(part,wire::OutputPart::OutputText { annotations,logprobs,.. } if !annotations.is_empty() || !logprobs.is_empty())
    {
        return Err(bad("Responses annotations/logprobs cannot be represented"));
    }
    Ok(())
}
fn validate_items(items: &[wire::OutputItem], terminal: bool) -> Result<(), CodecError> {
    let mut ids = BTreeSet::new();
    let mut calls = BTreeSet::new();
    let mut saw_tool = false;
    let mut saw_refusal = false;
    for item in items {
        nonempty(item.id())?;
        if !ids.insert(item.id())
            || !matches!(item.status(), "in_progress" | "completed" | "incomplete")
            || (terminal && item.status() == "in_progress")
        {
            return Err(bad("invalid Responses output identity/status"));
        }
        match item {
            wire::OutputItem::Message { role, content, .. } => {
                if role != "assistant" || saw_tool {
                    return Err(bad("Responses output order/role cannot be represented"));
                }
                for part in content {
                    validate_part(part)?;
                    match part {
                        wire::OutputPart::Refusal { .. } => saw_refusal = true,
                        wire::OutputPart::OutputText { .. } if saw_refusal => {
                            return Err(bad("text after refusal cannot be represented"));
                        }
                        _ => {}
                    }
                }
            }
            wire::OutputItem::FunctionCall { call_id, name, .. } => {
                nonempty(call_id)?;
                nonempty(name)?;
                if !calls.insert(call_id) {
                    return Err(bad("duplicate Responses call_id"));
                }
                saw_tool = true;
            }
        }
    }
    Ok(())
}
fn decode_usage(u: wire::Usage) -> Result<Usage, CodecError> {
    if u.input_tokens.checked_add(u.output_tokens) != Some(u.total_tokens)
        || u.input_tokens_details
            .as_ref()
            .is_some_and(|d| d.cached_tokens > u.input_tokens)
        || u.output_tokens_details
            .as_ref()
            .is_some_and(|d| d.reasoning_tokens > u.output_tokens)
    {
        return Err(bad("invalid Responses token usage"));
    }
    let usage = Usage {
        prompt_tokens: u.input_tokens,
        completion_tokens: u.output_tokens,
        total_tokens: u.total_tokens,
        cache_creation: u
            .input_tokens_details
            .as_ref()
            .and_then(|d| d.cache_write_tokens)
            .filter(|n| *n != 0)
            .map(|input_tokens| {
                Box::new(CacheCreationUsage {
                    input_tokens,
                    ephemeral_5m_input_tokens: None,
                    ephemeral_1h_input_tokens: None,
                })
            }),
        prompt_tokens_details: u
            .input_tokens_details
            .filter(|d| d.cached_tokens != 0)
            .map(|d| PromptTokensDetails {
                cached_tokens: Some(d.cached_tokens),
                audio_tokens: None,
            }),
        completion_tokens_details: u
            .output_tokens_details
            .filter(|d| d.reasoning_tokens != 0)
            .map(|d| CompletionTokensDetails {
                reasoning_tokens: Some(d.reasoning_tokens),
                audio_tokens: None,
                accepted_prediction_tokens: None,
                rejected_prediction_tokens: None,
            }),
    };
    super::cache::validate(&usage)?;
    Ok(usage)
}
fn encode_usage(u: &Usage) -> Result<Value, CodecError> {
    super::cache::validate(u)?;
    if u.prompt_tokens.checked_add(u.completion_tokens) != Some(u.total_tokens)
        || u.prompt_tokens_details
            .as_ref()
            .and_then(|d| d.cached_tokens)
            .is_some_and(|n| n > u.prompt_tokens)
        || u.completion_tokens_details
            .as_ref()
            .and_then(|d| d.reasoning_tokens)
            .is_some_and(|n| n > u.completion_tokens)
    {
        return Err(bad("invalid canonical token usage"));
    }
    if u.prompt_tokens_details
        .as_ref()
        .is_some_and(|d| d.audio_tokens.is_some_and(|n| n != 0))
        || u.completion_tokens_details.as_ref().is_some_and(|d| {
            [
                d.audio_tokens,
                d.accepted_prediction_tokens,
                d.rejected_prediction_tokens,
            ]
            .into_iter()
            .flatten()
            .any(|n| n != 0)
        })
    {
        return Err(bad("Responses cannot represent audio/prediction usage"));
    }
    let mut value = json!({"input_tokens":u.prompt_tokens,"output_tokens":u.completion_tokens,"total_tokens":u.total_tokens,
        "input_tokens_details":{"cached_tokens":u.prompt_tokens_details.as_ref().and_then(|d|d.cached_tokens).unwrap_or(0)},
        "output_tokens_details":{"reasoning_tokens":u.completion_tokens_details.as_ref().and_then(|d|d.reasoning_tokens).unwrap_or(0)}});
    if let Some(creation) = &u.cache_creation {
        value["input_tokens_details"]["cache_write_tokens"] = json!(creation.input_tokens);
    }
    Ok(value)
}
fn finish_reason(r: &wire::Response) -> Result<&'static str, CodecError> {
    match r.status.as_str() {
        "completed"
            if r.incomplete_details.is_none()
                && r.output.iter().all(|i| i.status() == "completed") =>
        {
            Ok(
                if r.output
                    .iter()
                    .any(|i| matches!(i, wire::OutputItem::FunctionCall { .. }))
                {
                    "tool_calls"
                } else {
                    "stop"
                },
            )
        }
        "incomplete" => match r.incomplete_details.as_ref().map(|d| d.reason.as_str()) {
            Some("max_output_tokens") => Ok("length"),
            Some("content_filter") => Ok("content_filter"),
            _ => Err(bad("unsupported Responses incomplete reason")),
        },
        _ => Err(bad("Responses response is not successfully terminal")),
    }
}
fn decode_response(r: wire::Response) -> Result<ChatResponse, CodecError> {
    validate_envelope(&r)?;
    validate_items(&r.output, true)?;
    let finish = finish_reason(&r)?;
    let mut text = String::new();
    let mut refusal = String::new();
    let mut saw_text = false;
    let mut saw_refusal = false;
    let mut calls = Vec::new();
    for item in r.output {
        match item {
            wire::OutputItem::Message { content, .. } => {
                for part in content {
                    match part {
                        wire::OutputPart::OutputText { text: t, .. } => {
                            if saw_refusal {
                                return Err(bad("text after refusal cannot be represented"));
                            }
                            text.push_str(&t);
                            saw_text = true;
                        }
                        wire::OutputPart::Refusal { refusal: r } => {
                            refusal.push_str(&r);
                            saw_refusal = true;
                        }
                    }
                }
            }
            wire::OutputItem::FunctionCall {
                call_id,
                name,
                arguments,
                ..
            } => calls.push(ToolCall::Function {
                id: call_id,
                function: FunctionCall { name, arguments },
            }),
        }
    }
    Ok(ChatResponse {
        id: r.id,
        object: "chat.completion".into(),
        created: r.created_at,
        model: r.model,
        choices: vec![Choice {
            index: 0,
            message: ResponseMessage {
                role: Role::Assistant,
                content: saw_text.then_some(Content::Text(text)),
                tool_calls: (!calls.is_empty()).then_some(calls),
                refusal: saw_refusal.then_some(refusal),
                audio: None,
            },
            finish_reason: Some(finish.into()),
            logprobs: None,
        }],
        usage: r.usage.map(decode_usage).transpose()?,
        system_fingerprint: None,
        service_tier: None,
    })
}
pub fn decode_chat_response(value: Value) -> Result<ChatResponse, CodecError> {
    decode_response(serde_json::from_value(value)?)
}
fn status(finish: &str) -> Result<(&'static str, Value), CodecError> {
    match finish {
        "stop" | "tool_calls" => Ok(("completed", Value::Null)),
        "length" => Ok(("incomplete", json!({"reason":"max_output_tokens"}))),
        "content_filter" => Ok(("incomplete", json!({"reason":"content_filter"}))),
        _ => Err(bad("unsupported finish reason for Responses")),
    }
}
fn envelope(
    id: &str,
    created: u64,
    model: &str,
    output: Value,
    finish: Option<&str>,
    usage: Option<&Usage>,
) -> Result<Value, CodecError> {
    nonempty(id)?;
    nonempty(model)?;
    let (status, details) = finish
        .map(status)
        .transpose()?
        .unwrap_or(("in_progress", Value::Null));
    Ok(
        json!({"id":id,"object":"response","created_at":created,"model":model,"status":status,"output":output,"error":null,"incomplete_details":details,"store":false,"background":false,"usage":usage.map(encode_usage).transpose()?}),
    )
}
pub fn encode_chat_response(r: &ChatResponse) -> Result<Value, CodecError> {
    if r.choices.len() != 1 || r.choices[0].index != 0 {
        return Err(bad("Responses requires one choice"));
    }
    let c = &r.choices[0];
    let m = &c.message;
    if m.role != Role::Assistant
        || m.audio.is_some()
        || c.logprobs.is_some()
        || r.system_fingerprint.is_some()
    {
        return Err(bad("unsupported Responses response role/audio/logprobs"));
    }
    let mut output = Vec::new();
    let mut parts = m
        .content
        .as_ref()
        .map(|c| content_parts(c, true, false, false))
        .transpose()?
        .unwrap_or_default();
    if let Some(refusal) = &m.refusal {
        parts.push(json!({"type":"refusal","refusal":refusal}));
    }
    let finish = c
        .finish_reason
        .as_deref()
        .ok_or_else(|| bad("Responses requires terminal finish reason"))?;
    let (state, _) = status(finish)?;
    if !parts.is_empty() {
        output.push(json!({"type":"message","id":format!("msg_{}_0",r.id),"role":"assistant","status":state,"content":parts}));
    }
    for (i, ToolCall::Function { id, function }) in m.tool_calls.iter().flatten().enumerate() {
        output.push(json!({"type":"function_call","id":format!("fc_{}_{i}",r.id),"status":state,"call_id":id,"name":function.name,"arguments":function.arguments}));
    }
    validate_items(
        &serde_json::from_value::<Vec<wire::OutputItem>>(json!(output))?,
        true,
    )?;
    envelope(
        &r.id,
        r.created,
        &r.model,
        json!(output),
        Some(finish),
        r.usage.as_ref(),
    )
}
