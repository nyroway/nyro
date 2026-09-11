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
fn text(content: wire::Content) -> Result<String, CodecError> {
    match content {
        wire::Content::Text(s) => Ok(s),
        wire::Content::Blocks(blocks) => {
            let mut out = String::new();
            for b in blocks {
                if let wire::Block::Text { text } = b {
                    out.push_str(&text)
                } else {
                    return Err(bad("expected text content"));
                }
            }
            Ok(out)
        }
    }
}
fn blocks(content: wire::Content) -> Vec<wire::Block> {
    match content {
        wire::Content::Text(text) => vec![wire::Block::Text { text }],
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
        let mut calls = vec![];
        let flush = |out: &mut Vec<Value>, parts: &mut Vec<Value>, calls: &mut Vec<Value>| {
            if !parts.is_empty() || !calls.is_empty() {
                let mut v = json!({"role":m.role});
                if !parts.is_empty() {
                    v["content"] = json!(std::mem::take(parts));
                }
                if !calls.is_empty() {
                    v["tool_calls"] = json!(std::mem::take(calls));
                }
                out.push(v);
            }
        };
        let content = blocks(m.content);
        if content.is_empty() {
            return Err(bad("message content must be nonempty"));
        }
        for b in content {
            match b {
                wire::Block::Text { text } => {
                    if !calls.is_empty() {
                        return Err(bad("text after tool calls cannot be represented"));
                    }
                    parts.push(json!({"type":"text","text":text}));
                }
                wire::Block::Image { source } => {
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
                    parts.push(json!({"type":"image_url","image_url":{"url":url}}));
                }
                wire::Block::ToolUse { id, name, input } => {
                    if m.role != "assistant"
                        || !input.is_object()
                        || id.is_empty()
                        || name.is_empty()
                    {
                        return Err(bad("invalid tool use"));
                    }
                    calls.push(json!({"type":"function","id":id,"function":{"name":name,"arguments":input.to_string()}}));
                }
                wire::Block::ToolResult {
                    tool_use_id,
                    content,
                    is_error,
                } => {
                    if m.role != "user" {
                        return Err(bad("unsupported tool result role"));
                    }
                    flush(&mut out, &mut parts, &mut calls);
                    let content = match content {
                        None => json!([]),
                        Some(wire::Content::Text(text)) => json!(text),
                        Some(wire::Content::Blocks(blocks)) => json!(
                            blocks
                                .into_iter()
                                .map(|block| {
                                    match block {
                                        wire::Block::Text { text } => {
                                            Ok(json!({"type":"text","text":text}))
                                        }
                                        _ => Err(bad("unsupported tool result content")),
                                    }
                                })
                                .collect::<Result<Vec<_>, _>>()?
                        ),
                    };
                    out.push(json!({"role":"tool","tool_call_id":tool_use_id,"content":content,"tool_error":is_error.unwrap_or(false)}));
                }
            }
        }
        flush(&mut out, &mut parts, &mut calls);
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
    if let Some(s) = r.system {
        messages.push(json!({"role":"system","content":text(s)?}));
    }
    messages.extend(decode_messages(r.messages)?);
    // Wire decoding still goes through the established OpenAI-shaped leaves;
    // carry the typed error status separately from that protocol's wire fields.
    let errors: Vec<_> = messages
        .iter_mut()
        .map(|m| {
            m.as_object_mut()
                .unwrap()
                .remove("tool_error")
                .and_then(|v| v.as_bool())
                .unwrap_or(false)
        })
        .collect();
    let mut v = json!({"model":r.model,"messages":messages,"max_tokens":r.max_tokens});
    if let Some(x) = r.stream {
        v["stream"] = json!(x)
    }
    if let Some(x) = r.temperature {
        v["temperature"] = json!(x)
    }
    if let Some(x) = r.top_p {
        v["top_p"] = json!(x)
    }
    if let Some(x) = r.stop_sequences {
        v["stop"] = json!(x)
    }
    if let Some(ts) = r.tools {
        v["tools"] = json!(
            ts.into_iter()
                .map(|t| {
                    let mut f = json!({"name":t.name,"parameters":t.input_schema});
                    if let Some(d) = t.description {
                        f["description"] = json!(d)
                    }
                    json!({"type":"function","function":f})
                })
                .collect::<Vec<_>>()
        )
    }
    if let Some(t) = r.tool_choice {
        v["tool_choice"] = match t.r#type.as_str() {
            "auto" | "none" if t.name.is_none() => json!(t.r#type),
            "any" if t.name.is_none() => json!("required"),
            "tool" => {
                json!({"type":"function","function":{"name":t.name.ok_or_else(||bad("tool choice needs name"))?}})
            }
            _ => return Err(bad("unsupported tool choice")),
        };
        if let Some(disable) = t.disable_parallel_tool_use {
            v["parallel_tool_calls"] = json!(!disable)
        }
    }
    let mut request = openai::decode_chat(v)?;
    for (message, error) in request.messages.iter_mut().zip(errors) {
        message.tool_error = error;
    }
    Ok(request)
}
fn content_parts(c: &Option<Content>, images: bool) -> Result<Vec<Value>, CodecError> {
    match c {
        None => Ok(vec![]),
        Some(Content::Text(t)) => Ok(vec![json!({"type":"text","text":t})]),
        Some(Content::Parts(p)) => p
            .iter()
            .map(|p| match p {
                ContentPart::Text {
                    text,
                    prompt_cache_breakpoint: None,
                } => Ok(json!({"type":"text","text":text})),
                ContentPart::ImageUrl {
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
                    Ok(json!({"type":"image","source":source}))
                }
                _ => Err(bad("unsupported non-text content")),
            })
            .collect(),
    }
}
fn call_blocks(calls: &Option<Vec<ToolCall>>) -> Result<Vec<Value>, CodecError> {
    calls
        .iter()
        .flatten()
        .map(|t| {
            let ToolCall::Function { id, function } = t;
            let input: Value = serde_json::from_str(&function.arguments)?;
            if !input.is_object() || id.is_empty() || function.name.is_empty() {
                return Err(bad("invalid tool call"));
            }
            Ok(json!({"type":"tool_use","id":id,"name":function.name,"input":input}))
        })
        .collect()
}
pub fn encode_chat(r: &ChatRequest) -> Result<Value, CodecError> {
    let mut portable = r.clone();
    for message in &mut portable.messages {
        if message.tool_error && message.role != Role::Tool {
            return Err(bad("tool_error requires a tool result"));
        }
        message.tool_error = false;
    }
    let source = openai::encode_chat(&portable)?;
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
        let mut content = content_parts(&m.content, m.role == Role::User)?;
        content.extend(call_blocks(&m.tool_calls)?);
        match m.role {
            Role::System | Role::Developer => {
                if !messages.is_empty() || m.tool_calls.is_some() || m.tool_call_id.is_some() {
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
                for ToolCall::Function { id, .. } in m.tool_calls.iter().flatten() {
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
        for t in ts.as_array().unwrap() {
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
            content: wire::Content::Blocks(r.content),
        }])?
    };
    let mut content = vec![];
    let mut tools = vec![];
    for m in messages {
        if let Some(p) = m.get("content") {
            content.extend(p.as_array().unwrap().clone())
        }
        if let Some(t) = m.get("tool_calls") {
            tools.extend(t.as_array().unwrap().clone())
        }
    }
    let mut message = json!({"role":"assistant","content":content});
    if !tools.is_empty() {
        message["tool_calls"] = json!(tools)
    }
    let reason = stop_in(
        r.stop_reason
            .as_deref()
            .ok_or_else(|| bad("missing stop reason"))?,
    )?;
    let mut response = openai::decode_chat_response(
        json!({"id":r.id,"object":"chat.completion","created":0,"model":r.model,"choices":[{"index":0,"message":message,"finish_reason":reason}]}),
    )?;
    response.usage = Some(cache::decode(&r.usage)?);
    Ok(response)
}
pub fn encode_chat_response(r: &ChatResponse) -> Result<Value, CodecError> {
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
    let mut content = content_parts(&c.message.content, false)?;
    content.extend(call_blocks(&c.message.tool_calls)?);
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
    tool: Option<(u32, String)>,
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
                let mut delta = Delta::default();
                let tool = match content_block {
                    wire::Block::Text { text } => {
                        if self.tools != 0 {
                            return Err(bad("text after tool calls cannot be represented"));
                        }
                        delta.content = Some(text);
                        None
                    }
                    wire::Block::ToolUse { id, name, input } => {
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
                        delta.tool_calls = Some(vec![ToolCallDelta {
                            index: i,
                            id: Some(id),
                            r#type: Some(FunctionType::Function),
                            function: Some(FunctionDelta {
                                name: Some(name),
                                arguments: None,
                            }),
                        }]);
                        Some((i, String::new()))
                    }
                    _ => return Err(bad("unsupported streamed block")),
                };
                self.active = Some(ActiveBlock { index, tool });
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
                let delta = match (delta, &mut active.tool) {
                    (wire::Delta::TextDelta { text }, None) => Delta {
                        content: Some(text),
                        ..Default::default()
                    },
                    (wire::Delta::InputJsonDelta { partial_json }, Some((i, args))) => {
                        if args.len().saturating_add(partial_json.len()) > self.limit {
                            return Err(bad("tool JSON exceeds limit"));
                        }
                        args.push_str(&partial_json);
                        Delta {
                            tool_calls: Some(vec![ToolCallDelta {
                                index: *i,
                                id: None,
                                r#type: None,
                                function: Some(FunctionDelta {
                                    name: None,
                                    arguments: Some(partial_json),
                                }),
                            }]),
                            ..Default::default()
                        }
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
                if let Some((i, args)) = a.tool {
                    if args.is_empty() {
                        out.push(self.chunk(
                            Delta {
                                tool_calls: Some(vec![ToolCallDelta {
                                    index: i,
                                    id: None,
                                    r#type: None,
                                    function: Some(FunctionDelta {
                                        name: None,
                                        arguments: Some("{}".into()),
                                    }),
                                }]),
                                ..Default::default()
                            },
                            None,
                            false,
                        )?)
                    } else {
                        let v: Value = serde_json::from_str(&args)?;
                        if !v.is_object() {
                            return Err(bad("tool JSON must be object"));
                        }
                    }
                }
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
    id: String,
    name: String,
    args: String,
}
pub struct StreamEncoder {
    model: String,
    limit: usize,
    bytes: usize,
    started: bool,
    ended: bool,
    finished: Option<String>,
    text_open: bool,
    tools: std::collections::BTreeMap<u32, PendingTool>,
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
            tools: Default::default(),
            usage: None,
        }
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
                            || c.delta.refusal.is_some()
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
                    let d = &choice.delta;
                    if self.finished.is_some()
                        && (d.content.is_some()
                            || d.tool_calls.is_some()
                            || choice.finish_reason.is_some())
                    {
                        return Err(bad("content after finish reason"));
                    }
                    if let Some(text) = &d.content {
                        if text.len() > self.limit || !self.tools.is_empty() {
                            return Err(bad("text exceeds limit or follows buffered tools"));
                        }
                        if !self.text_open {
                            output.push_str(&frame(
                                "content_block_start",
                                json!({"index":0,"content_block":{"type":"text","text":""}}),
                            ));
                            self.text_open = true;
                        }
                        output.push_str(&frame(
                            "content_block_delta",
                            json!({"index":0,"delta":{"type":"text_delta","text":text}}),
                        ));
                    }
                    for t in d.tool_calls.iter().flatten() {
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
                                );
                        if self.bytes.saturating_add(additional) > self.limit
                            || (!self.tools.contains_key(&t.index)
                                && self.tools.len() >= self.limit / 32)
                        {
                            return Err(bad("tool accumulation exceeds limit"));
                        }
                        let item = self.tools.entry(t.index).or_default();
                        if let Some(id) = &t.id {
                            if !item.id.is_empty() {
                                return Err(bad("repeated tool id"));
                            }
                            self.bytes = self.bytes.saturating_add(id.len());
                            item.id.push_str(id)
                        }
                        if let Some(f) = &t.function {
                            if let Some(n) = &f.name {
                                self.bytes = self.bytes.saturating_add(n.len());
                                item.name.push_str(n)
                            }
                            if let Some(a) = &f.arguments {
                                self.bytes = self.bytes.saturating_add(a.len());
                                item.args.push_str(a)
                            }
                        }
                    }
                    if let Some(reason) = &choice.finish_reason {
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
                    return Err(bad("done before message"));
                }
                let reason = self
                    .finished
                    .as_ref()
                    .ok_or_else(|| bad("missing finish reason"))?;
                let mut out = String::new();
                let mut index = 0;
                if self.text_open {
                    out.push_str(&frame("content_block_stop", json!({"index":index})));
                    index += 1;
                }
                for (expected, (i, t)) in self.tools.iter().enumerate() {
                    if *i as usize != expected || t.id.is_empty() || t.name.is_empty() {
                        return Err(bad("incomplete tool stream"));
                    }
                    let args: Value = serde_json::from_str(&t.args)?;
                    if !args.is_object() {
                        return Err(bad("tool JSON must be object"));
                    }
                    out.push_str(&frame("content_block_start",json!({"index":index,"content_block":{"type":"tool_use","id":t.id,"name":t.name,"input":{}}})));
                    out.push_str(&frame("content_block_delta",json!({"index":index,"delta":{"type":"input_json_delta","partial_json":t.args}})));
                    out.push_str(&frame("content_block_stop", json!({"index":index})));
                    index += 1;
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
