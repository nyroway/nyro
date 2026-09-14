//! Anthropic Messages conversion and strict, bounded native SSE state machines.
mod cache;
use super::{CodecError, openai};
use crate::ir::*;
use nyro_protocol::{anthropic as wire, framing::Event};
use serde_json::{Value, json};
use std::collections::BTreeSet;
fn bad(s: &str) -> CodecError {
    CodecError(s.into())
}
fn annotated(mut value: Value, control: Option<wire::CacheControl>) -> Value {
    if let Some(control) = control {
        value["anthropic_cache_control"] = json!(control);
    }
    value
}
fn blocks(content: wire::Content) -> Vec<wire::Block> {
    match content {
        wire::Content::Text(text) => vec![wire::Block::Text {
            text,
            cache_control: None,
        }],
        wire::Content::Blocks(b) => b,
    }
}
fn decode_messages(messages: Vec<wire::Message>) -> Result<Vec<Value>, CodecError> {
    let mut out = vec![];
    for m in messages {
        if m.role != "user" && m.role != "assistant" {
            return Err(bad("invalid Messages role"));
        }
        let mut parts = vec![];
        let mut items = vec![];
        let flush_parts = |parts: &mut Vec<Value>, items: &mut Vec<Value>| {
            if !parts.is_empty() {
                items.push(json!({"type":"content","value":std::mem::take(parts)}));
            }
        };
        let flush = |out: &mut Vec<Value>, parts: &mut Vec<Value>, items: &mut Vec<Value>| {
            flush_parts(parts, items);
            if !items.is_empty() {
                out.push(json!({"role":m.role,"items":std::mem::take(items)}));
            }
        };
        let content = blocks(m.content);
        if content.is_empty() {
            return Err(bad("message content must be nonempty"));
        }
        for b in content {
            match b {
                wire::Block::Thinking {
                    thinking,
                    signature,
                } => {
                    if m.role != "assistant" || signature.is_empty() {
                        return Err(bad("thinking requires assistant content and a signature"));
                    }
                    parts.push(json!({"type":"anthropic_thinking","thinking":thinking,"signature":signature}));
                }
                wire::Block::RedactedThinking { data } => {
                    if m.role != "assistant" || data.is_empty() {
                        return Err(bad("redacted thinking requires assistant content and data"));
                    }
                    parts.push(json!({"type":"anthropic_redacted_thinking","data":data}));
                }
                wire::Block::Text {
                    text,
                    cache_control,
                } => {
                    parts.push(annotated(json!({"type":"text","text":text}), cache_control));
                }
                wire::Block::Image {
                    source,
                    cache_control,
                } => {
                    if m.role != "user" {
                        return Err(bad("images require a user message"));
                    }
                    let url = match source {
                        wire::ImageSource::Base64 { media_type, data } => {
                            format!("data:{media_type};base64,{data}")
                        }
                        wire::ImageSource::Url { url } => {
                            if url.starts_with("data:") {
                                return Err(bad("URL image source requires HTTP(S)"));
                            }
                            url
                        }
                    };
                    parts.push(annotated(
                        json!({"type":"image_url","image_url":{"url":url}}),
                        cache_control,
                    ));
                }
                wire::Block::ToolUse {
                    id,
                    name,
                    input,
                    cache_control,
                } => {
                    if m.role != "assistant"
                        || !input.is_object()
                        || id.is_empty()
                        || name.is_empty()
                    {
                        return Err(bad("invalid tool use"));
                    }
                    flush_parts(&mut parts, &mut items);
                    items.push(json!({"type":"tool_call","value":annotated(json!({"type":"function","id":id,"function":{"name":name,"arguments":input.to_string()}}), cache_control)}));
                }
                wire::Block::ToolResult {
                    tool_use_id,
                    cache_control,
                    content,
                    is_error,
                } => {
                    if m.role != "user" {
                        return Err(bad("unsupported tool result role"));
                    }
                    flush(&mut out, &mut parts, &mut items);
                    let content = match content {
                        None => json!([]),
                        Some(wire::Content::Text(text)) => json!(text),
                        Some(wire::Content::Blocks(blocks)) => json!(
                            blocks
                                .into_iter()
                                .map(|block| {
                                    match block {
                                        wire::Block::Text {
                                            text,
                                            cache_control,
                                        } => Ok(annotated(
                                            json!({"type":"text","text":text}),
                                            cache_control,
                                        )),
                                        _ => Err(bad("unsupported tool result content")),
                                    }
                                })
                                .collect::<Result<Vec<_>, _>>()?
                        ),
                    };
                    out.push(annotated(json!({"role":"tool","tool_call_id":tool_use_id,"items":[{"type":"content","value":content}],"tool_error":is_error.unwrap_or(false)}), cache_control));
                }
            }
        }
        flush(&mut out, &mut parts, &mut items);
    }
    Ok(out)
}
pub fn decode_chat(value: Value) -> Result<ChatRequest, CodecError> {
    let r: wire::Request = serde_json::from_value(value)?;
    if r.messages.is_empty() {
        return Err(bad("messages must be nonempty"));
    }
    if r.tools.as_ref().is_some_and(|tools| {
        tools
            .iter()
            .any(|t| t.name.is_empty() || !t.input_schema.is_object())
    }) {
        return Err(bad("invalid tool definition"));
    }
    if r.temperature.is_some_and(|v| !(0.0..=1.0).contains(&v)) {
        return Err(bad("temperature out of range"));
    }
    let mut messages = vec![];
    if let Some(system) = r.system {
        let content = match system {
            wire::Content::Text(text) => json!(text),
            wire::Content::Blocks(blocks) => json!(
                blocks
                    .into_iter()
                    .map(|b| match b {
                        wire::Block::Text {
                            text,
                            cache_control,
                        } => Ok(annotated(json!({"type":"text","text":text}), cache_control)),
                        _ => Err(bad("system content must be text")),
                    })
                    .collect::<Result<Vec<_>, _>>()?
            ),
        };
        messages.push(json!({"role":"system","items":[{"type":"content","value":content}]}));
    }
    messages.extend(decode_messages(r.messages)?);
    let tools = r.tools.map(|tools| {
        tools
            .into_iter()
            .map(|t| Tool::Function {
                function: nyro_protocol::openai::chat::FunctionDefinition {
                    name: t.name,
                    description: t.description,
                    parameters: Some(t.input_schema),
                    strict: None,
                },
                anthropic_cache_control: t.cache_control,
            })
            .collect()
    });
    let mut tool_choice = None;
    let mut parallel_tool_calls = None;
    if let Some(t) = r.tool_choice {
        let choice = match t.r#type.as_str() {
            "auto" | "none" if t.name.is_none() => json!(t.r#type),
            "any" if t.name.is_none() => json!("required"),
            "tool" => {
                json!({"type":"function","function":{"name":t.name.ok_or_else(||bad("tool choice needs name"))?}})
            }
            _ => return Err(bad("unsupported tool choice")),
        };
        tool_choice = Some(serde_json::from_value(choice)?);
        parallel_tool_calls = t.disable_parallel_tool_use.map(|disable| !disable);
    }
    let request = ChatRequest {
        responses_reasoning: None,
        responses_include_encrypted: false,
        gemini_thinking: None,
        anthropic_thinking: r.thinking,
        model: r.model,
        stream: r.stream,
        messages: serde_json::from_value(json!(messages))?,
        anthropic_cache_control: r.cache_control,
        generation: Generation {
            max_tokens: Some(r.max_tokens),
            temperature: r.temperature,
            top_p: r.top_p,
            stop: r
                .stop_sequences
                .map(nyro_protocol::openai::chat::Stop::Multiple),
            ..Default::default()
        },
        openai: Box::new(OpenAiOptions {
            tools,
            tool_choice,
            parallel_tool_calls,
            ..Default::default()
        }),
    };
    validate_thinking(&request)?;
    openai::validate_chat(&request)?;
    Ok(request)
}

fn validate_thinking(r: &ChatRequest) -> Result<(), CodecError> {
    if matches!(r.anthropic_thinking, Some(wire::ThinkingConfig::Enabled { budget_tokens, .. }) if budget_tokens < 1024)
    {
        return Err(bad("thinking budget must be at least 1024 tokens"));
    }
    for m in &r.messages {
        for parts in m.items.iter().filter_map(|item| match item.as_content() {
            Some(Content::Parts(parts)) => Some(parts),
            _ => None,
        }) {
            for part in parts {
                match part {
                    ContentPart::AnthropicThinking { signature, .. }
                        if m.role != Role::Assistant || signature.is_empty() =>
                    {
                        return Err(bad("thinking requires assistant role and a signature"));
                    }
                    ContentPart::AnthropicRedactedThinking { data }
                        if m.role != Role::Assistant || data.is_empty() =>
                    {
                        return Err(bad("redacted thinking requires assistant role and data"));
                    }
                    _ => {}
                }
            }
        }
    }
    Ok(())
}

fn content_parts(c: Option<&Content>, images: bool) -> Result<Vec<Value>, CodecError> {
    match c {
        None => Ok(vec![]),
        Some(Content::Text(t)) => Ok(vec![json!({"type":"text","text":t})]),
        Some(Content::Parts(p)) => p
            .iter()
            .map(|p| match p {
                ContentPart::AnthropicThinking {
                    thinking,
                    signature,
                } if !signature.is_empty() => {
                    Ok(json!({"type":"thinking","thinking":thinking,"signature":signature}))
                }
                ContentPart::AnthropicRedactedThinking { data } if !data.is_empty() => {
                    Ok(json!({"type":"redacted_thinking","data":data}))
                }
                ContentPart::Text {
                    anthropic_cache_control,
                    text,
                    prompt_cache_breakpoint: None,
                } => {
                    let mut part = json!({"type":"text","text":text});
                    if let Some(control) = anthropic_cache_control {
                        part["cache_control"] = json!(control);
                    }
                    Ok(part)
                }
                ContentPart::ImageUrl {
                    anthropic_cache_control,
                    image_url,
                    prompt_cache_breakpoint: None,
                } if images => {
                    super::image::default_detail(image_url)?;
                    let source = match super::image::source(image_url)? {
                        super::image::Source::Url(url) => json!({"type":"url","url":url}),
                        super::image::Source::Base64 { mime, data } => {
                            json!({"type":"base64","media_type":mime,"data":data})
                        }
                    };
                    {
                        let mut part = json!({"type":"image","source":source});
                        if let Some(control) = anthropic_cache_control {
                            part["cache_control"] = json!(control);
                        }
                        Ok(part)
                    }
                }
                _ => Err(bad("unsupported non-text content")),
            })
            .collect(),
    }
}
fn call_blocks<'a>(calls: impl Iterator<Item = &'a ToolCall>) -> Result<Vec<Value>, CodecError> {
    calls
        .map(|t| {
            let ToolCall::Function {
                gemini,
                id,
                function,
                anthropic_cache_control,
            } = t;
            if gemini.is_some() {
                return Err(bad("Anthropic cannot represent Gemini call signatures"));
            }
            let input: Value = serde_json::from_str(&function.arguments)?;
            if !input.is_object() || id.is_empty() || function.name.is_empty() {
                return Err(bad("invalid tool call"));
            }
            let mut part = json!({"type":"tool_use","id":id,"name":function.name,"input":input});
            if let Some(control) = anthropic_cache_control {
                part["cache_control"] = json!(control);
            }
            Ok(part)
        })
        .collect()
}
fn item_blocks(items: &[MessageItem], images: bool) -> Result<Vec<Value>, CodecError> {
    let mut blocks = Vec::new();
    for item in items {
        if let Some(content) = item.as_content() {
            blocks.extend(content_parts(Some(content), images)?);
        } else if let Some(call) = item.as_tool_call() {
            blocks.extend(call_blocks(std::iter::once(call))?);
        }
    }
    Ok(blocks)
}

pub fn encode_chat(r: &ChatRequest) -> Result<Value, CodecError> {
    openai::validate_chat(r)?;
    validate_thinking(r)?;
    let mut portable = r.clone();
    portable.anthropic_thinking = None;
    portable.anthropic_cache_control = None;
    for Tool::Function {
        anthropic_cache_control,
        ..
    } in portable.openai.tools.iter_mut().flatten()
    {
        *anthropic_cache_control = None;
    }

    for message in &mut portable.messages {
        if message.tool_error && message.role != Role::Tool {
            return Err(bad("tool_error requires a tool result"));
        }
        message.tool_error = false;
        message.anthropic_cache_control = None;
        for parts in message
            .items
            .iter_mut()
            .filter_map(|item| match item.as_content_mut() {
                Some(Content::Parts(parts)) => Some(parts),
                _ => None,
            })
        {
            parts.retain(|part| {
                !matches!(
                    part,
                    ContentPart::AnthropicThinking { .. }
                        | ContentPart::AnthropicRedactedThinking { .. }
                )
            });
            for part in parts {
                match part {
                    ContentPart::Text {
                        anthropic_cache_control,
                        ..
                    }
                    | ContentPart::ImageUrl {
                        anthropic_cache_control,
                        ..
                    } => *anthropic_cache_control = None,
                    _ => {}
                }
            }
        }
        for ToolCall::Function {
            anthropic_cache_control,
            ..
        } in message.tool_calls_mut()
        {
            *anthropic_cache_control = None;
        }
    }
    let source = openai::encode_portable_options(&portable)?;
    if r.openai
        .stream_options
        .as_ref()
        .is_some_and(|o| o.include_obfuscation == Some(true))
    {
        return Err(bad("Anthropic does not support stream obfuscation"));
    }
    let allowed = [
        "model",
        "messages",
        "stream",
        "temperature",
        "max_tokens",
        "max_completion_tokens",
        "top_p",
        "stop",
        "n",
        "tools",
        "tool_choice",
        "parallel_tool_calls",
        "stream_options",
    ];
    if source
        .as_object()
        .unwrap()
        .keys()
        .any(|k| !allowed.contains(&k.as_str()))
        || r.generation.n.is_some_and(|n| n != 1)
    {
        return Err(bad("unsupported Anthropic request option"));
    }
    let g = &r.generation;
    if g.max_tokens.is_some() && g.max_completion_tokens.is_some() {
        return Err(bad("ambiguous token limits"));
    }
    let max = g
        .max_tokens
        .or(g.max_completion_tokens)
        .ok_or_else(|| bad("Anthropic requires max_tokens"))?;
    if g.temperature.is_some_and(|v| !(0.0..=1.0).contains(&v)) {
        return Err(bad("Anthropic temperature out of range"));
    }
    let mut messages: Vec<Value> = vec![];
    let mut system = vec![];
    let mut pending = BTreeSet::new();
    let mut results = vec![];
    let mut after_results = false;
    for m in &r.messages {
        if m.role != Role::Tool && !pending.is_empty() {
            return Err(bad(
                "tool results must immediately follow all calls in a batch",
            ));
        }
        if m.name.is_some() || m.refusal.is_some() || m.audio.is_some() {
            return Err(bad("unsupported message option"));
        }
        let content = item_blocks(&m.items, m.role == Role::User)?;
        match m.role {
            Role::System | Role::Developer => {
                if !messages.is_empty()
                    || m.tool_calls().next().is_some()
                    || m.tool_call_id.is_some()
                {
                    return Err(bad("system instructions must precede conversation"));
                }
                system.extend(content)
            }
            Role::Tool => {
                let id = m
                    .tool_call_id
                    .as_ref()
                    .ok_or_else(|| bad("tool result needs id"))?;
                if !pending.remove(id.as_str()) {
                    return Err(bad("unknown or duplicate tool result id"));
                }
                let mut result = json!({"type":"tool_result","tool_use_id":id,"content":content});
                if let Some(control) = m.anthropic_cache_control {
                    result["cache_control"] = json!(control);
                }
                if m.tool_error {
                    result["is_error"] = json!(true);
                }
                results.push(result);
                if pending.is_empty() {
                    messages.push(json!({"role":"user","content":std::mem::take(&mut results)}));
                    after_results = true;
                }
            }
            Role::User | Role::Assistant => {
                if m.tool_call_id.is_some() {
                    return Err(bad("unexpected tool_call_id"));
                }
                for ToolCall::Function { id, .. } in m.tool_calls() {
                    if !pending.insert(id.as_str()) {
                        return Err(bad("duplicate tool call id in a batch"));
                    }
                }
                // Keep accompanying user text after the complete result batch.
                if m.role == Role::User && after_results {
                    messages.last_mut().unwrap()["content"]
                        .as_array_mut()
                        .unwrap()
                        .extend(content);
                } else {
                    messages.push(json!({"role":if m.role==Role::User{"user"}else{"assistant"},"content":content}));
                }
                after_results = false;
            }
        }
    }
    if !pending.is_empty() {
        return Err(bad("missing tool results"));
    }
    if messages.is_empty() {
        return Err(bad("conversation is empty"));
    }
    let mut v = json!({"model":r.model,"messages":messages,"max_tokens":max});
    if let Some(thinking) = r.anthropic_thinking {
        v["thinking"] = json!(thinking);
    }
    if let Some(control) = r.anthropic_cache_control {
        v["cache_control"] = json!(control);
    }
    if !system.is_empty() {
        v["system"] = json!(system)
    }
    for k in ["stream", "temperature", "top_p"] {
        if let Some(x) = source.get(k) {
            v[k] = x.clone()
        }
    }
    if let Some(s) = source.get("stop") {
        v["stop_sequences"] = if s.is_string() { json!([s]) } else { s.clone() }
    }
    if let Some(ts) = source.get("tools") {
        let mut out = vec![];
        for (
            t,
            Tool::Function {
                anthropic_cache_control,
                ..
            },
        ) in ts
            .as_array()
            .unwrap()
            .iter()
            .zip(r.openai.tools.iter().flatten())
        {
            let f = &t["function"];
            if f["name"].as_str().is_none_or(str::is_empty)
                || f.get("parameters").is_some_and(|p| !p.is_object())
            {
                return Err(bad("invalid tool definition"));
            }
            if f.get("strict").and_then(Value::as_bool) == Some(true) {
                return Err(bad("strict tools unsupported"));
            }
            let mut item = json!({"name":f["name"],"input_schema":f.get("parameters").cloned().unwrap_or(json!({"type":"object"}))});
            if let Some(d) = f.get("description") {
                item["description"] = d.clone()
            }
            if let Some(control) = anthropic_cache_control {
                item["cache_control"] = json!(control);
            }
            out.push(item)
        }
        v["tools"] = json!(out)
    }
    if source.get("tool_choice").is_some() || r.openai.parallel_tool_calls.is_some() {
        let mut t = match source.get("tool_choice") {
            None => json!({"type":"auto"}),
            Some(Value::String(s)) => json!({"type":if s=="required"{"any"}else{s}}),
            Some(t) => json!({"type":"tool","name":t["function"]["name"]}),
        };
        if let Some(p) = r.openai.parallel_tool_calls {
            t["disable_parallel_tool_use"] = json!(!p)
        }
        v["tool_choice"] = t
    }
    let _: wire::Request = serde_json::from_value(v.clone())?;
    Ok(v)
}
fn stop_in(s: &str) -> Result<&str, CodecError> {
    match s {
        "end_turn" => Ok("stop"),
        "max_tokens" => Ok("length"),
        "tool_use" => Ok("tool_calls"),
        _ => Err(bad("unsupported Anthropic stop reason")),
    }
}
fn stop_out(s: &str) -> Result<&str, CodecError> {
    match s {
        "stop" => Ok("end_turn"),
        "length" => Ok("max_tokens"),
        "tool_calls" => Ok("tool_use"),
        _ => Err(bad("unsupported finish reason")),
    }
}
pub fn decode_chat_response(v: Value) -> Result<ChatResponse, CodecError> {
    let r: wire::Response = serde_json::from_value(v)?;
    if r.r#type != "message"
        || r.role != "assistant"
        || r.id.is_empty()
        || r.model.trim().is_empty()
    {
        return Err(bad("invalid response type or role"));
    }
    if r.stop_sequence.is_some() {
        return Err(bad("matched stop sequence cannot be represented"));
    }
    let messages = if r.content.is_empty() {
        vec![]
    } else {
        decode_messages(vec![wire::Message {
            role: r.role,
            content: wire::Content::Blocks(serde_json::from_value(serde_json::to_value(
                r.content,
            )?)?),
        }])?
    };
    let mut items: Vec<Value> = messages
        .into_iter()
        .flat_map(|m| m["items"].as_array().unwrap().clone())
        .collect();
    if items.first().is_none_or(|item| item["type"] != "content") {
        items.insert(0, json!({"type":"content","value":[]}));
    }
    let message = json!({"role":"assistant","items":items});
    let reason = stop_in(
        r.stop_reason
            .as_deref()
            .ok_or_else(|| bad("missing stop reason"))?,
    )?;
    let mut response: ChatResponse = serde_json::from_value(
        json!({"id":r.id,"object":"chat.completion","created":0,"model":r.model,"choices":[{"index":0,"message":message,"finish_reason":reason}]}),
    )?;
    response.usage = Some(cache::decode(&r.usage)?);
    Ok(response)
}
pub fn encode_chat_response(r: &ChatResponse) -> Result<Value, CodecError> {
    openai::validate_output_breakpoints(r)?;
    if r.system_fingerprint.is_some() || r.service_tier.is_some() {
        return Err(bad("unsupported response metadata"));
    }
    if r.choices.len() != 1 {
        return Err(bad("Anthropic requires one choice"));
    }
    let c = &r.choices[0];
    if c.index != 0
        || c.logprobs.is_some()
        || c.message.role != Role::Assistant
        || c.message.refusal.is_some()
        || c.message.audio.is_some()
    {
        return Err(bad("unsupported response fields"));
    }
    let content = item_blocks(&c.message.items, false)?;
    let u = r
        .usage
        .as_ref()
        .ok_or_else(|| bad("Anthropic requires usage"))?;
    let wire_usage = cache::encode(u)?;
    Ok(
        json!({"id":r.id,"type":"message","role":"assistant","model":r.model,"content":content,"stop_reason":stop_out(c.finish_reason.as_deref().ok_or_else(||bad("missing finish reason"))?)?,"stop_sequence":null,"usage":wire_usage}),
    )
}
const DEFAULT_LIMIT: usize = 1024 * 1024;
struct ActiveBlock {
    index: u32,
    content: ActiveContent,
}
enum ActiveContent {
    Text,
    Tool(u32, String),
    Thinking(ThinkingState),
}
/// Validate bounded thinking blocks without retaining summaries or interpreting signatures.
enum ThinkingState {
    Visible { bytes: usize, signed: bool },
    Redacted,
}
impl ThinkingState {
    fn start(delta: &AnthropicThinkingDelta, limit: usize) -> Result<Self, CodecError> {
        match delta {
            AnthropicThinkingDelta::Start => Ok(Self::Visible {
                bytes: 0,
                signed: false,
            }),
            AnthropicThinkingDelta::Redacted { data }
                if !data.is_empty() && data.len() <= limit =>
            {
                Ok(Self::Redacted)
            }
            _ => Err(bad("invalid thinking block start")),
        }
    }
    fn advance(&mut self, delta: &AnthropicThinkingDelta, limit: usize) -> Result<(), CodecError> {
        match (self, delta) {
            (
                Self::Visible {
                    bytes,
                    signed: false,
                },
                AnthropicThinkingDelta::Thinking { thinking },
            ) => {
                *bytes = bytes.saturating_add(thinking.len());
                if *bytes > limit {
                    return Err(bad("thinking block exceeds limit"));
                }
            }
            (Self::Visible { bytes, signed }, AnthropicThinkingDelta::Signature { signature })
                if !*signed && !signature.is_empty() =>
            {
                *bytes = bytes.saturating_add(signature.len());
                if *bytes > limit {
                    return Err(bad("thinking block exceeds limit"));
                }
                *signed = true;
            }
            (Self::Visible { signed: true, .. } | Self::Redacted, AnthropicThinkingDelta::Stop) => {
            }
            _ => return Err(bad("out of order or incomplete thinking block")),
        }
        Ok(())
    }
}
fn block_delta(index: u32, delta: PartDelta) -> Delta {
    Delta {
        role: None,
        events: vec![PositionedDelta {
            position: StreamPosition {
                item: StreamItem::Ordered(index),
                part: 0,
            },
            delta,
        }],
    }
}
fn thinking_delta(index: u32, delta: AnthropicThinkingDelta) -> Delta {
    block_delta(index, PartDelta::AnthropicThinking(delta))
}
pub struct StreamDecoder {
    limit: usize,
    started: bool,
    ended: bool,
    terminal: bool,
    id: String,
    model: String,
    usage: wire::Usage,
    active: Option<ActiveBlock>,
    next: u32,
    tools: u32,
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
            limit: max_bytes,
            started: false,
            ended: false,
            terminal: false,
            id: String::new(),
            model: String::new(),
            usage: wire::Usage::default(),
            active: None,
            next: 0,
            tools: 0,
        }
    }
    fn chunk(
        &self,
        delta: Delta,
        finish_reason: Option<String>,
        with_usage: bool,
    ) -> Result<ChatEvent, CodecError> {
        Ok(ChatEvent::Chunk(Box::new(ChatChunk {
            id: self.id.clone(),
            object: "chat.completion.chunk".into(),
            created: 0,
            model: self.model.clone(),
            choices: vec![StreamChoice {
                index: 0,
                delta,
                finish_reason,
                logprobs: None,
            }],
            usage: if with_usage {
                Some(cache::decode(&self.usage)?)
            } else {
                None
            },
            system_fingerprint: None,
            service_tier: None,
            obfuscation: None,
        })))
    }
    pub fn push(&mut self, event: &Event) -> Result<Vec<ChatEvent>, CodecError> {
        if self.ended || event.data.len() > self.limit {
            return Err(bad("event after end or exceeds limit"));
        }
        let value: Value = serde_json::from_str(&event.data)?;
        if event.event.as_deref() != value["type"].as_str() {
            return Err(bad("SSE event name does not match type"));
        }
        let e: wire::StreamEvent = serde_json::from_value(value)?;
        let mut out = vec![];
        match e {
            wire::StreamEvent::Ping => {}
            wire::StreamEvent::Error { .. } => return Err(bad("upstream Anthropic error")),
            wire::StreamEvent::MessageStart { message } => {
                if self.started
                    || message.r#type != "message"
                    || message.id.is_empty()
                    || message.model.trim().is_empty()
                    || message.role != "assistant"
                    || !message.content.is_empty()
                    || message.stop_reason.is_some()
                    || message.stop_sequence.is_some()
                {
                    return Err(bad("invalid message_start"));
                }
                self.started = true;
                self.id = message.id;
                self.model = message.model;
                self.usage = message.usage;
                out.push(self.chunk(
                    Delta {
                        role: Some(Role::Assistant),
                        ..Default::default()
                    },
                    None,
                    true,
                )?);
            }
            wire::StreamEvent::ContentBlockStart {
                index,
                content_block,
            } => {
                if !self.started || self.terminal || self.active.is_some() || index != self.next {
                    return Err(bad("out of order content block"));
                }
                self.next = self
                    .next
                    .checked_add(1)
                    .ok_or_else(|| bad("block index overflow"))?;
                let mut delta;
                let content = match content_block {
                    wire::OutputBlock::Text { text } => {
                        delta = block_delta(index, PartDelta::Text(text));
                        ActiveContent::Text
                    }
                    wire::OutputBlock::Thinking {
                        thinking,
                        signature,
                    } => {
                        if !thinking.is_empty() || !signature.is_empty() {
                            return Err(bad("thinking stream requires empty initial block"));
                        }
                        let start = AnthropicThinkingDelta::Start;
                        let state = ThinkingState::start(&start, self.limit)?;
                        delta = thinking_delta(index, start);
                        ActiveContent::Thinking(state)
                    }
                    wire::OutputBlock::RedactedThinking { data } => {
                        let start = AnthropicThinkingDelta::Redacted { data };
                        let state = ThinkingState::start(&start, self.limit)?;
                        delta = thinking_delta(index, start);
                        ActiveContent::Thinking(state)
                    }
                    wire::OutputBlock::ToolUse { id, name, input } => {
                        if id.is_empty()
                            || name.is_empty()
                            || input.as_object().is_none_or(|o| !o.is_empty())
                        {
                            return Err(bad("tool stream requires empty initial input"));
                        }
                        let i = self.tools;
                        self.tools = self
                            .tools
                            .checked_add(1)
                            .ok_or_else(|| bad("tool index overflow"))?;
                        delta = block_delta(
                            index,
                            PartDelta::ToolCall(ToolCallDelta {
                                gemini: None,
                                index: i,
                                id: Some(id),
                                r#type: Some(FunctionType::Function),
                                function: Some(FunctionDelta {
                                    name: Some(name),
                                    arguments: None,
                                }),
                            }),
                        );
                        ActiveContent::Tool(i, String::new())
                    }
                };
                let kind = match content {
                    ActiveContent::Text => Some(StreamPartKind::Text),
                    ActiveContent::Tool(..) => Some(StreamPartKind::ToolCall),
                    ActiveContent::Thinking(_) => None,
                };
                if let Some(kind) = kind {
                    delta.events.insert(
                        0,
                        PositionedDelta {
                            position: StreamPosition {
                                item: StreamItem::Ordered(index),
                                part: 0,
                            },
                            delta: PartDelta::Start(kind),
                        },
                    );
                }
                self.active = Some(ActiveBlock { index, content });
                out.push(self.chunk(delta, None, false)?);
            }
            wire::StreamEvent::ContentBlockDelta { index, delta } => {
                let active = self
                    .active
                    .as_mut()
                    .ok_or_else(|| bad("delta without block"))?;
                if active.index != index {
                    return Err(bad("delta index mismatch"));
                }
                let delta = match (delta, &mut active.content) {
                    (wire::Delta::TextDelta { text }, ActiveContent::Text) => {
                        block_delta(index, PartDelta::Text(text))
                    }
                    (
                        wire::Delta::InputJsonDelta { partial_json },
                        ActiveContent::Tool(i, args),
                    ) => {
                        if args.len().saturating_add(partial_json.len()) > self.limit {
                            return Err(bad("tool JSON exceeds limit"));
                        }
                        args.push_str(&partial_json);
                        block_delta(
                            index,
                            PartDelta::ToolCall(ToolCallDelta {
                                gemini: None,
                                index: *i,
                                id: None,
                                r#type: None,
                                function: Some(FunctionDelta {
                                    name: None,
                                    arguments: Some(partial_json),
                                }),
                            }),
                        )
                    }
                    (wire::Delta::ThinkingDelta { thinking }, ActiveContent::Thinking(state)) => {
                        let delta = AnthropicThinkingDelta::Thinking { thinking };
                        state.advance(&delta, self.limit)?;
                        thinking_delta(index, delta)
                    }
                    (wire::Delta::SignatureDelta { signature }, ActiveContent::Thinking(state)) => {
                        let delta = AnthropicThinkingDelta::Signature { signature };
                        state.advance(&delta, self.limit)?;
                        thinking_delta(index, delta)
                    }
                    _ => return Err(bad("delta type mismatch")),
                };
                out.push(self.chunk(delta, None, false)?);
            }
            wire::StreamEvent::ContentBlockStop { index } => {
                let a = self
                    .active
                    .take()
                    .ok_or_else(|| bad("stop without block"))?;
                if index != a.index {
                    return Err(bad("stop index mismatch"));
                }
                let mut delta = block_delta(index, PartDelta::End);
                match a.content {
                    ActiveContent::Tool(i, args) => {
                        if args.is_empty() {
                            delta.events.insert(
                                0,
                                PositionedDelta {
                                    position: StreamPosition {
                                        item: StreamItem::Ordered(index),
                                        part: 0,
                                    },
                                    delta: PartDelta::ToolCall(ToolCallDelta {
                                        gemini: None,
                                        index: i,
                                        id: None,
                                        r#type: None,
                                        function: Some(FunctionDelta {
                                            name: None,
                                            arguments: Some("{}".into()),
                                        }),
                                    }),
                                },
                            );
                        } else {
                            let v: Value = serde_json::from_str(&args)?;
                            if !v.is_object() {
                                return Err(bad("tool JSON must be object"));
                            }
                        }
                    }
                    ActiveContent::Thinking(mut state) => {
                        state.advance(&AnthropicThinkingDelta::Stop, self.limit)?;
                        delta = thinking_delta(index, AnthropicThinkingDelta::Stop);
                    }
                    ActiveContent::Text => {}
                }
                out.push(self.chunk(delta, None, false)?);
            }
            wire::StreamEvent::MessageDelta { delta, usage } => {
                if delta.stop_sequence.is_some() {
                    return Err(bad("matched stop sequence cannot be represented"));
                }
                if !self.started || self.active.is_some() || self.terminal {
                    return Err(bad("out of order message_delta"));
                }
                let reason = stop_in(
                    delta
                        .stop_reason
                        .as_deref()
                        .ok_or_else(|| bad("missing terminal reason"))?,
                )?;
                let mut next = self.usage.clone();
                next.output_tokens = usage.output_tokens;
                if let Some(input) = usage.input_tokens {
                    next.input_tokens = input;
                }
                if usage.cache_read_input_tokens.is_some() {
                    next.cache_read_input_tokens = usage.cache_read_input_tokens;
                }
                if usage.cache_creation_input_tokens.is_some() {
                    next.cache_creation_input_tokens = usage.cache_creation_input_tokens;
                }
                if usage.cache_creation.is_some() {
                    next.cache_creation = usage.cache_creation;
                }
                cache::decode(&next)?;
                cache::progress(&self.usage, &next)?;
                self.usage = next;
                self.terminal = true;
                out.push(self.chunk(Delta::default(), Some(reason.into()), true)?);
            }
            wire::StreamEvent::MessageStop => {
                if !self.terminal || self.active.is_some() {
                    return Err(bad("premature message_stop"));
                }
                self.ended = true;
                out.push(ChatEvent::Done);
            }
        }
        Ok(out)
    }
    pub fn finish(&mut self) -> Result<Vec<ChatEvent>, CodecError> {
        if self.ended {
            Ok(vec![])
        } else {
            Err(bad("truncated Anthropic stream"))
        }
    }
}
fn frame(kind: &str, mut value: Value) -> String {
    value["type"] = json!(kind);
    format!("event: {kind}\ndata: {value}\n\n")
}
#[derive(Default)]
struct PendingTool {
    position: Option<StreamPosition>,
    id: String,
    name: String,
    args: String,
    emitted: bool,
}
struct OrdinaryPart {
    kind: Option<StreamPartKind>,
    tool: Option<u32>,
    ended: bool,
    thinking: Option<ThinkingState>,
    pending: Vec<PartDelta>,
    buffered_bytes: usize,
}
struct ResponseContainer {
    function: bool,
    ended: bool,
    id: String,
    last_part: Option<u32>,
}
pub struct StreamEncoder {
    model: String,
    limit: usize,
    bytes: usize,
    started: bool,
    ended: bool,
    finished: Option<String>,
    text_open: bool,
    text_position: Option<StreamPosition>,
    last_content_position: Option<StreamPosition>,
    next_index: u32,
    tools: std::collections::BTreeMap<u32, PendingTool>,
    parts: std::collections::BTreeMap<StreamPosition, OrdinaryPart>,
    part_order: std::collections::VecDeque<StreamPosition>,
    containers: std::collections::BTreeMap<StreamItem, ResponseContainer>,
    incomplete_container: bool,
    explicit: bool,
    usage: Option<Usage>,
}
impl StreamEncoder {
    pub fn new(public_model: String) -> Self {
        Self::with_limit(public_model, DEFAULT_LIMIT)
    }
    pub fn with_limit(public_model: String, max_bytes: usize) -> Self {
        Self {
            model: public_model,
            limit: max_bytes,
            bytes: 0,
            started: false,
            ended: false,
            finished: None,
            text_open: false,
            text_position: None,
            last_content_position: None,
            next_index: 0,
            tools: Default::default(),
            parts: Default::default(),
            part_order: Default::default(),
            containers: Default::default(),
            incomplete_container: false,
            explicit: false,
            usage: None,
        }
    }
    fn close_text(&mut self, output: &mut String) -> Result<(), CodecError> {
        if self.text_open {
            output.push_str(&frame(
                "content_block_stop",
                json!({"index":self.next_index}),
            ));
            self.next_index = self
                .next_index
                .checked_add(1)
                .ok_or_else(|| bad("block index overflow"))?;
            self.text_open = false;
            self.text_position = None;
        }
        Ok(())
    }
    fn start_content(&mut self, position: StreamPosition) -> Result<(), CodecError> {
        if self
            .last_content_position
            .is_some_and(|last| match (last.item, position.item) {
                (StreamItem::Ordered(a), StreamItem::Ordered(b)) => {
                    (b, position.part) <= (a, last.part)
                }
                _ => last == position,
            })
        {
            return Err(bad("reused or out of order content position"));
        }
        self.last_content_position = Some(position);
        Ok(())
    }
    fn emit_tool(&mut self, tool_index: u32, output: &mut String) -> Result<(), CodecError> {
        let tool = self
            .tools
            .get_mut(&tool_index)
            .ok_or_else(|| bad("missing tool payload"))?;
        if tool.emitted || tool.id.is_empty() || tool.name.is_empty() {
            return Err(bad("incomplete or repeated tool block"));
        }
        if !serde_json::from_str::<Value>(&tool.args)?.is_object() {
            return Err(bad("tool JSON must be object"));
        }
        output.push_str(&frame("content_block_start",json!({"index":self.next_index,"content_block":{"type":"tool_use","id":tool.id,"name":tool.name,"input":{}}})));
        output.push_str(&frame("content_block_delta",json!({"index":self.next_index,"delta":{"type":"input_json_delta","partial_json":tool.args}})));
        output.push_str(&frame(
            "content_block_stop",
            json!({"index":self.next_index}),
        ));
        tool.emitted = true;
        self.next_index = self
            .next_index
            .checked_add(1)
            .ok_or_else(|| bad("block index overflow"))?;
        Ok(())
    }
    fn pending_implicit_tools(&self) -> bool {
        self.tools
            .values()
            .any(|tool| !tool.emitted && tool.position.is_none_or(|p| !self.parts.contains_key(&p)))
    }
    fn queue_output(
        &mut self,
        position: StreamPosition,
        delta: &PartDelta,
        output: &mut String,
    ) -> Result<(), CodecError> {
        let blocked = self.part_order.front() != Some(&position) || self.container_blocks(position);
        let bytes = if blocked {
            serde_json::to_vec(delta)?
                .len()
                .saturating_add(std::mem::size_of::<PartDelta>())
        } else {
            0
        };
        if self.bytes.saturating_add(bytes) > self.limit {
            return Err(bad("blocked stream content exceeds limit"));
        }
        self.bytes += bytes;
        let part = self
            .parts
            .get_mut(&position)
            .ok_or_else(|| bad("payload without part"))?;
        part.buffered_bytes = part.buffered_bytes.saturating_add(bytes);
        part.pending.push(delta.clone());
        self.flush_parts(output)
    }
    fn flush_parts(&mut self, output: &mut String) -> Result<(), CodecError> {
        while let Some(position) = self.part_order.front().copied() {
            if self.container_blocks(position) {
                break;
            }
            let part = self.parts.get_mut(&position).unwrap();
            if part.kind == Some(StreamPartKind::ToolCall) {
                if !part.ended {
                    break;
                }
                let index = part.tool.ok_or_else(|| bad("tool ended without payload"))?;
                self.emit_tool(index, output)?;
            } else {
                let pending = std::mem::take(&mut part.pending);
                self.bytes -= part.buffered_bytes;
                part.buffered_bytes = 0;
                for delta in pending {
                    match delta {
                        PartDelta::Start(StreamPartKind::Text) => {
                            self.close_text(output)?;
                            output.push_str(&frame("content_block_start",json!({"index":self.next_index,"content_block":{"type":"text","text":""}})));
                            self.text_open = true;
                            self.text_position = Some(position);
                        }
                        PartDelta::Text(text) => output.push_str(&frame("content_block_delta",json!({"index":self.next_index,"delta":{"type":"text_delta","text":text}}))),
                        PartDelta::End => self.close_text(output)?,
                        PartDelta::AnthropicThinking(delta) => match delta {
                            AnthropicThinkingDelta::Start | AnthropicThinkingDelta::Redacted { .. } => {
                                self.close_text(output)?;
                                let block = match delta { AnthropicThinkingDelta::Redacted { data } => json!({"type":"redacted_thinking","data":data}), _ => json!({"type":"thinking","thinking":"","signature":""}) };
                                output.push_str(&frame("content_block_start",json!({"index":self.next_index,"content_block":block})));
                            }
                            AnthropicThinkingDelta::Thinking { thinking } => output.push_str(&frame("content_block_delta",json!({"index":self.next_index,"delta":{"type":"thinking_delta","thinking":thinking}}))),
                            AnthropicThinkingDelta::Signature { signature } => output.push_str(&frame("content_block_delta",json!({"index":self.next_index,"delta":{"type":"signature_delta","signature":signature}}))),
                            AnthropicThinkingDelta::Stop => {
                                output.push_str(&frame("content_block_stop",json!({"index":self.next_index})));
                                self.next_index = self.next_index.checked_add(1).ok_or_else(|| bad("block index overflow"))?;
                            }
                        },
                        _ => return Err(bad("unsupported buffered stream part")),
                    }
                }
                if !self.parts[&position].ended {
                    break;
                }
            }
            self.parts.remove(&position);
            self.part_order.pop_front();
            self.bytes -= std::mem::size_of::<(StreamPosition, OrdinaryPart)>()
                + std::mem::size_of::<StreamPosition>()
                + 32;
        }
        Ok(())
    }
    fn container_blocks(&self, position: StreamPosition) -> bool {
        self.containers
            .range(..position.item)
            .any(|(_, container)| !container.ended)
    }
    fn start_part(
        &mut self,
        position: StreamPosition,
        kind: Option<StreamPartKind>,
        thinking: Option<ThinkingState>,
    ) -> Result<(), CodecError> {
        if self.pending_implicit_tools() {
            return Err(bad("explicit block after unfinished implicit tools"));
        }
        if let Some(container) = self.containers.get_mut(&position.item) {
            if container
                .last_part
                .is_some_and(|part| part >= position.part)
            {
                return Err(bad("reused or out of order container part"));
            }
            container.last_part = Some(position.part);
        } else {
            self.start_content(position)?;
        }
        let bytes = std::mem::size_of::<(StreamPosition, OrdinaryPart)>()
            + std::mem::size_of::<StreamPosition>()
            + 32;
        if self.bytes.saturating_add(bytes) > self.limit {
            return Err(bad("stream part state exceeds limit"));
        }
        self.bytes += bytes;
        self.parts.insert(
            position,
            OrdinaryPart {
                kind,
                tool: None,
                ended: false,
                thinking,
                pending: Vec::new(),
                buffered_bytes: 0,
            },
        );
        let index = self.part_order.partition_point(|part| *part < position);
        self.part_order.insert(index, position);
        Ok(())
    }
    fn lifecycle(
        &mut self,
        position: StreamPosition,
        kind: Option<StreamPartKind>,
        output: &mut String,
    ) -> Result<(), CodecError> {
        if let Some(kind) = kind {
            if kind == StreamPartKind::Refusal {
                return Err(bad("Anthropic does not support refusal blocks"));
            }
            self.explicit = true;
            self.start_part(position, Some(kind), None)?;
            if kind == StreamPartKind::Text {
                self.queue_output(position, &PartDelta::Start(kind), output)?;
            } else if self.part_order.front() == Some(&position) {
                self.close_text(output)?;
            }
        } else {
            let part = self
                .parts
                .get_mut(&position)
                .ok_or_else(|| bad("end without part"))?;
            if part.ended || part.kind.is_none() {
                return Err(bad("repeated or mismatched part end"));
            }
            part.ended = true;
            if part.kind == Some(StreamPartKind::Text) {
                self.queue_output(position, &PartDelta::End, output)?;
            } else {
                self.flush_parts(output)?;
            }
        }
        Ok(())
    }
    fn thinking_part(
        &mut self,
        position: StreamPosition,
        delta: &AnthropicThinkingDelta,
        output: &mut String,
    ) -> Result<(), CodecError> {
        match delta {
            AnthropicThinkingDelta::Start | AnthropicThinkingDelta::Redacted { .. } => {
                let state = ThinkingState::start(delta, self.limit)?;
                self.start_part(position, None, Some(state))?;
            }
            _ => {
                let part = self
                    .parts
                    .get_mut(&position)
                    .ok_or_else(|| bad("thinking delta without block"))?;
                if part.ended {
                    return Err(bad("thinking after block end"));
                }
                part.thinking
                    .as_mut()
                    .ok_or_else(|| bad("thinking delta type mismatch"))?
                    .advance(delta, self.limit)?;
                part.ended = matches!(delta, AnthropicThinkingDelta::Stop);
            }
        }
        self.queue_output(
            position,
            &PartDelta::AnthropicThinking(delta.clone()),
            output,
        )
    }
    fn container(&mut self, event: &PositionedDelta) -> Result<(), CodecError> {
        match &event.delta {
            PartDelta::ResponsesItemStart(start) => {
                let (function, id) = match start {
                    ResponsesItemStart::Message { id } => (false, id),
                    ResponsesItemStart::FunctionCall { id } => (true, id),
                };
                if id.is_empty()
                    || self
                        .containers
                        .keys()
                        .any(|item| *item >= event.position.item)
                    || self.containers.values().any(|c| c.id == *id)
                {
                    return Err(bad("invalid or reused Responses item"));
                }
                let additional = id
                    .len()
                    .saturating_add(std::mem::size_of::<(StreamItem, ResponseContainer)>() + 32);
                if self.bytes.saturating_add(additional) > self.limit {
                    return Err(bad("container state exceeds limit"));
                }
                self.bytes += additional;
                self.containers.insert(
                    event.position.item,
                    ResponseContainer {
                        function,
                        ended: false,
                        id: id.clone(),
                        last_part: None,
                    },
                );
            }
            PartDelta::ResponsesItemEnd(status) => {
                let c = self
                    .containers
                    .get_mut(&event.position.item)
                    .ok_or_else(|| bad("item end without start"))?;
                if c.ended
                    || self
                        .parts
                        .iter()
                        .any(|(p, part)| p.item == event.position.item && !part.ended)
                    || *status == nyro_protocol::openai::responses::ItemStatus::InProgress
                {
                    return Err(bad("unfinished or repeated Responses item end"));
                }
                c.ended = true;
                self.incomplete_container |=
                    *status == nyro_protocol::openai::responses::ItemStatus::Incomplete;
            }
            _ => {}
        }
        Ok(())
    }
    fn part(&mut self, event: &PositionedDelta, output: &mut String) -> Result<(), CodecError> {
        super::validate_position(event)?;
        let position = event.position;
        if matches!(
            event.delta,
            PartDelta::ResponsesItemStart(_) | PartDelta::ResponsesItemEnd(_)
        ) {
            self.container(event)?;
            return self.flush_parts(output);
        }
        if !self.containers.is_empty() {
            let container = self
                .containers
                .get(&position.item)
                .ok_or_else(|| bad("part outside Responses item"))?;
            let function = match &event.delta {
                PartDelta::Start(kind) => *kind == StreamPartKind::ToolCall,
                PartDelta::End => self
                    .parts
                    .get(&position)
                    .is_some_and(|p| p.kind == Some(StreamPartKind::ToolCall)),
                PartDelta::ToolCall(_) => true,
                _ => false,
            };
            if container.ended || container.function != function {
                return Err(bad("Responses item payload mismatch"));
            }
        }
        if matches!(event.delta, PartDelta::Text(_) | PartDelta::ToolCall(_)) {
            if let Some(part) = self.parts.get(&position) {
                let kind = if matches!(event.delta, PartDelta::Text(_)) {
                    StreamPartKind::Text
                } else {
                    StreamPartKind::ToolCall
                };
                if part.ended || part.kind != Some(kind) {
                    return Err(bad("part payload mismatch or after end"));
                }
            } else if !self.parts.is_empty()
                || (self.explicit && matches!(position.item, StreamItem::Ordered(_)))
            {
                return Err(bad("payload without matching part start"));
            }
        }
        match &event.delta {
            PartDelta::Start(kind) => self.lifecycle(position, Some(*kind), output)?,
            PartDelta::End => self.lifecycle(position, None, output)?,
            PartDelta::AnthropicThinking(delta) => self.thinking_part(position, delta, output)?,
            PartDelta::Text(text) => {
                if self.parts.contains_key(&position) {
                    if text.len() > self.limit {
                        return Err(bad("text exceeds limit"));
                    }
                    return self.queue_output(position, &event.delta, output);
                }
                if !self.parts.is_empty()
                    || text.len() > self.limit
                    || self.pending_implicit_tools()
                {
                    return Err(bad(
                        "invalid text within thinking, after tools, or over limit",
                    ));
                }
                if self.text_position != Some(position) {
                    self.start_content(position)?;
                    self.close_text(output)?;
                }
                if !self.text_open {
                    output.push_str(&frame(
                        "content_block_start",
                        json!({"index":self.next_index,"content_block":{"type":"text","text":""}}),
                    ));
                    self.text_open = true;
                    self.text_position = Some(position);
                }
                output.push_str(&frame(
                    "content_block_delta",
                    json!({"index":self.next_index,"delta":{"type":"text_delta","text":text}}),
                ));
            }
            PartDelta::ToolCall(t) => {
                if t.gemini.is_some() {
                    return Err(bad("unsupported tool metadata or tool within thinking"));
                }
                if !self.parts.contains_key(&position)
                    && self.last_content_position.is_some_and(|last| {
                        match (last.item, position.item) {
                            (StreamItem::Ordered(content), StreamItem::Ordered(tool)) => {
                                tool <= content
                            }
                            _ => last == position,
                        }
                    })
                    || self
                        .tools
                        .iter()
                        .any(|(index, tool)| *index != t.index && tool.position == Some(position))
                {
                    return Err(bad("tool position belongs to another item"));
                }
                let additional =
                    t.id.as_ref()
                        .map_or(0, String::len)
                        .saturating_add(
                            t.function
                                .as_ref()
                                .and_then(|f| f.name.as_ref())
                                .map_or(0, String::len),
                        )
                        .saturating_add(
                            t.function
                                .as_ref()
                                .and_then(|f| f.arguments.as_ref())
                                .map_or(0, String::len),
                        )
                        .saturating_add(if self.tools.contains_key(&t.index) {
                            0
                        } else {
                            std::mem::size_of::<StreamPosition>()
                        });
                if self.bytes.saturating_add(additional) > self.limit
                    || (!self.tools.contains_key(&t.index) && self.tools.len() >= self.limit / 32)
                {
                    return Err(bad("tool accumulation exceeds limit"));
                }
                let item = self.tools.entry(t.index).or_default();
                if item.emitted || item.position.is_some_and(|p| p != position) {
                    return Err(bad("tool delta position mismatch"));
                }
                item.position = Some(position);
                if let Some(part) = self.parts.get_mut(&position) {
                    if part.tool.is_some_and(|index| index != t.index) {
                        return Err(bad("multiple tool calls in one part"));
                    }
                    part.tool = Some(t.index);
                }
                self.bytes = self.bytes.saturating_add(additional);
                if let Some(id) = &t.id {
                    if !item.id.is_empty() {
                        return Err(bad("repeated tool id"));
                    }
                    item.id.push_str(id);
                }
                if let Some(f) = &t.function {
                    if let Some(n) = &f.name {
                        item.name.push_str(n);
                    }
                    if let Some(a) = &f.arguments {
                        item.args.push_str(a);
                    }
                }
            }
            _ => return Err(bad("unsupported Anthropic stream part")),
        }
        Ok(())
    }
    pub fn push(&mut self, event: &ChatEvent) -> Result<String, CodecError> {
        if self.ended {
            return Err(bad("event after stream end"));
        }
        match event {
            ChatEvent::Chunk(c) => {
                if c.id.len().saturating_add(self.model.len()) > self.limit {
                    return Err(bad("stream identity exceeds limit"));
                }
                if c.system_fingerprint.is_some()
                    || c.service_tier.is_some()
                    || c.obfuscation.is_some()
                {
                    return Err(bad("unsupported stream metadata"));
                }
                if c.choices.len() > 1
                    || c.choices.first().is_some_and(|c| {
                        c.index != 0
                            || c.logprobs.is_some()
                            || c.delta.role.as_ref().is_some_and(|r| *r != Role::Assistant)
                    })
                {
                    return Err(bad("unsupported stream choice"));
                }
                if let Some(u) = &c.usage {
                    let next = cache::encode(u)?;
                    if let Some(previous) = &self.usage {
                        cache::progress(&cache::encode(previous)?, &next)?;
                    }
                }
                // Bound each batch before constructing frames; text is not retained across chunks.
                for choice in &c.choices {
                    let events = &choice.delta.events;
                    let text_bytes = events.iter().fold(0usize, |bytes, event| {
                        bytes.saturating_add(match &event.delta {
                            PartDelta::Text(text) => text.len(),
                            _ => 0,
                        })
                    });
                    if text_bytes > self.limit
                        || (events.len() > 1
                            && events
                                .len()
                                .saturating_mul(std::mem::size_of::<PositionedDelta>())
                                > self.limit)
                    {
                        return Err(bad("stream event batch exceeds limit"));
                    }
                }
                let mut output = String::new();
                if !self.started {
                    self.started = true;
                    let mut initial = c
                        .usage
                        .as_ref()
                        .map(cache::encode)
                        .transpose()?
                        .unwrap_or_default();
                    initial.output_tokens = 0;
                    output = frame(
                        "message_start",
                        json!({"message":{"id":c.id,"type":"message","role":"assistant","model":self.model,"content":[],"stop_reason":null,"stop_sequence":null,"usage":initial}}),
                    );
                }
                if let Some(u) = &c.usage {
                    self.usage = Some(u.clone())
                }
                for choice in &c.choices {
                    if self.finished.is_some()
                        && (!choice.delta.events.is_empty() || choice.finish_reason.is_some())
                    {
                        return Err(bad("content after finish reason"));
                    }
                    // Keep the existing separate thinking-event contract during the IR migration.
                    if choice
                        .delta
                        .events
                        .iter()
                        .any(|event| matches!(event.delta, PartDelta::AnthropicThinking(_)))
                        && (choice.delta.events.len() != 1 || choice.finish_reason.is_some())
                    {
                        return Err(bad("thinking delta must be separate"));
                    }
                    for event in &choice.delta.events {
                        self.part(event, &mut output)?;
                    }
                    if let Some(reason) = &choice.finish_reason {
                        if !self.parts.is_empty() || self.containers.values().any(|c| !c.ended) {
                            return Err(bad("finish within unfinished block or item"));
                        }
                        if self.incomplete_container && reason != "length" {
                            return Err(bad("incomplete item requires incomplete finish"));
                        }
                        self.finished = Some(stop_out(reason)?.into())
                    }
                }
                if self.bytes > self.limit || self.tools.len() > self.limit / 32 {
                    return Err(bad("stream accumulation exceeds limit"));
                }
                Ok(output)
            }
            ChatEvent::Done => {
                if !self.started {
                    return Err(bad("done before message or within thinking block"));
                }
                if !self.parts.is_empty() || self.containers.values().any(|c| !c.ended) {
                    return Err(bad("done within unfinished part or item"));
                }
                let reason = self
                    .finished
                    .clone()
                    .ok_or_else(|| bad("missing finish reason"))?;
                let mut out = String::new();
                self.close_text(&mut out)?;
                let indices: Vec<_> = self.tools.keys().copied().collect();
                for (expected, index) in indices.into_iter().enumerate() {
                    if index as usize != expected {
                        return Err(bad("incomplete tool stream"));
                    }
                    if !self.tools[&index].emitted {
                        self.emit_tool(index, &mut out)?;
                    }
                }
                let u = self
                    .usage
                    .as_ref()
                    .ok_or_else(|| bad("missing stream usage"))?;
                out.push_str(&frame("message_delta",json!({"delta":{"stop_reason":reason,"stop_sequence":null},"usage":cache::encode(u)?})));
                out.push_str(&frame("message_stop", json!({})));
                self.ended = true;
                Ok(out)
            }
        }
    }
}
