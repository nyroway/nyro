use anyhow::Result;
use reqwest::header::HeaderMap;
use serde_json::Value;
use std::collections::HashMap;
use std::collections::HashSet;

use crate::protocol::RequestEncoder;
use crate::protocol::ir::request::{
    ContentBlock, MediaSource, Message, MessageContent, Role, ToolChoice, ToolSpec,
};
use crate::protocol::ir::{AiRequest, ToolCall};

pub struct OpenAIEncoder;

impl RequestEncoder for OpenAIEncoder {
    fn encode_request(&self, req: &AiRequest) -> Result<(Value, HeaderMap)> {
        let tools = req.tools.as_deref().unwrap_or(&[]);
        let tools_opt: Option<&[ToolSpec]> = if tools.is_empty() { None } else { Some(tools) };

        let normalized_messages = normalize_messages_for_openai(&req.messages, tools_opt);
        let messages: Vec<Value> = normalized_messages
            .iter()
            .map(encode_message)
            .collect::<Result<Vec<_>>>()?;

        let ingress = &req.meta.vendor.ingress;

        let mut body = serde_json::json!({
            "model": req.model,
            "messages": messages,
            "stream": req.stream.enabled,
        });

        let obj = body.as_object_mut().unwrap();

        if let Some(t) = req.generation.temperature {
            obj.insert("temperature".into(), t.into());
        }
        if let Some(m) = req.generation.max_tokens {
            obj.insert("max_tokens".into(), m.into());
        }
        if let Some(p) = req.generation.top_p {
            obj.insert("top_p".into(), p.into());
        }

        if !tools.is_empty() {
            let tools_val: Vec<Value> = tools
                .iter()
                .map(|t| {
                    let mut f = serde_json::json!({
                        "name": t.name,
                        "parameters": t.parameters,
                    });
                    if let Some(ref desc) = t.description {
                        f.as_object_mut()
                            .unwrap()
                            .insert("description".into(), desc.clone().into());
                    }
                    serde_json::json!({
                        "type": "function",
                        "function": f,
                    })
                })
                .collect();
            obj.insert("tools".into(), Value::Array(tools_val));
        }
        if let Some(ref tc) = req.tool_choice {
            obj.insert("tool_choice".into(), tool_choice_to_value(tc));
        }

        // Always include_usage when streaming.
        if req.stream.enabled {
            let stream_opts = ingress
                .get("stream_options")
                .cloned()
                .unwrap_or_else(|| serde_json::json!({"include_usage": true}));
            obj.insert("stream_options".into(), stream_opts);
        }

        for key in &[
            "parallel_tool_calls",
            "prediction",
            "modalities",
            "audio",
            "response_format",
            "seed",
            "stop",
            "logit_bias",
            "service_tier",
            "reasoning_effort",
            "frequency_penalty",
            "presence_penalty",
            "n",
            "user",
        ] {
            if let Some(v) = ingress.get(*key) {
                obj.entry(key.to_string()).or_insert_with(|| v.clone());
            }
        }

        // Passthrough any remaining unknown extra fields.
        for (k, v) in ingress {
            obj.entry(k.clone()).or_insert_with(|| v.clone());
        }

        Ok((body, HeaderMap::new()))
    }

    fn egress_path(&self, _model: &str, _stream: bool) -> String {
        "/v1/chat/completions".to_string()
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

fn normalize_messages_for_openai(messages: &[Message], tools: Option<&[ToolSpec]>) -> Vec<Message> {
    let preprocessed = remap_duplicate_tool_call_ids(messages);

    let mut out: Vec<Message> = Vec::with_capacity(preprocessed.len() + 2);
    let mut seen_tool_call_ids: HashSet<String> = HashSet::new();
    let mut consumed_tool_result_ids: HashSet<String> = HashSet::new();
    let mut generated_seq: usize = 0;
    let fallback_tool_name = tools
        .and_then(|defs| defs.first())
        .map(|d| d.name.clone())
        .unwrap_or_else(|| "tool".to_string());

    for msg in &preprocessed {
        let mut msg = msg.clone();

        if msg.role == Role::Assistant {
            if let Some(tool_calls) = &mut msg.tool_calls {
                for tc in tool_calls.iter_mut() {
                    if tc.id.trim().is_empty() {
                        generated_seq += 1;
                        tc.id = format!("call_enc_{generated_seq}");
                    }
                    if tc.name.trim().is_empty() {
                        tc.name = fallback_tool_name.clone();
                    }
                    seen_tool_call_ids.insert(tc.id.clone());
                }
            }
            out.push(msg);
            continue;
        }

        if msg.role != Role::Tool {
            out.push(msg);
            continue;
        }

        let hinted_id = tool_message_payload(&msg).1;
        let mut resolved_id = msg
            .tool_call_id
            .clone()
            .filter(|v| !v.trim().is_empty())
            .or_else(|| hinted_id.clone().filter(|v| !v.trim().is_empty()));

        if resolved_id.is_none() {
            generated_seq += 1;
            resolved_id = Some(format!("call_enc_{generated_seq}"));
        }
        let mut final_id = resolved_id.expect("tool_call_id should always exist");
        if consumed_tool_result_ids.contains(&final_id) {
            generated_seq += 1;
            final_id = format!("call_enc_{generated_seq}");
        }

        let extracted_call = take_matching_tool_call_from_history(&mut out, &final_id);
        if let Some((tc, source_idx)) = extracted_call {
            trim_trailing_assistant_text_after_index(&mut out, source_idx);
            let source_meta = out[source_idx].meta.clone();
            out.push(Message {
                role: Role::Assistant,
                content: MessageContent::Text(String::new()),
                tool_calls: Some(vec![tc]),
                tool_call_id: None,
                meta: source_meta,
            });
            seen_tool_call_ids.insert(final_id.clone());
        } else {
            let has_adjacent_matching_call = out
                .last()
                .is_some_and(|prev| assistant_has_tool_call_id(prev, &final_id));

            let has_adjacent_matching_call = if has_adjacent_matching_call {
                true
            } else {
                make_matching_call_adjacent(&mut out, &final_id)
            };

            if !has_adjacent_matching_call {
                if seen_tool_call_ids.contains(&final_id) {
                    generated_seq += 1;
                    final_id = format!("call_enc_{generated_seq}");
                }
                let synth_name = hinted_id
                    .as_deref()
                    .filter(|v| !v.trim().is_empty())
                    .map(|_| fallback_tool_name.clone())
                    .unwrap_or_else(|| fallback_tool_name.clone());
                out.push(Message {
                    role: Role::Assistant,
                    content: MessageContent::Text(String::new()),
                    tool_calls: Some(vec![ToolCall {
                        id: final_id.clone(),
                        name: synth_name,
                        arguments: "{}".to_string(),
                    }]),
                    tool_call_id: None,
                    meta: None,
                });
                seen_tool_call_ids.insert(final_id.clone());
            }
        }

        msg.tool_call_id = Some(final_id.clone());
        consumed_tool_result_ids.insert(final_id);
        out.push(msg);
    }

    out = prune_orphan_assistant_tool_calls(out);

    out.retain(|msg| {
        if msg.role != Role::Assistant {
            return true;
        }
        let has_calls = msg.tool_calls.as_ref().is_some_and(|c| !c.is_empty());
        if has_calls {
            return true;
        }
        !msg.content.to_text().trim().is_empty()
    });

    out
}

fn prune_orphan_assistant_tool_calls(messages: Vec<Message>) -> Vec<Message> {
    let referenced_tool_ids: HashSet<String> = messages
        .iter()
        .filter(|m| m.role == Role::Tool)
        .filter_map(|m| m.tool_call_id.clone())
        .filter(|id| !id.trim().is_empty())
        .collect();

    let mut out: Vec<Message> = Vec::with_capacity(messages.len());
    for mut msg in messages {
        if msg.role == Role::Assistant
            && let Some(calls) = msg.tool_calls.take()
        {
            let kept: Vec<ToolCall> = calls
                .into_iter()
                .filter(|tc| referenced_tool_ids.contains(&tc.id))
                .collect();
            if !kept.is_empty() {
                msg.tool_calls = Some(kept);
            }
        }
        out.push(msg);
    }
    out
}

fn assistant_has_tool_call_id(msg: &Message, tool_call_id: &str) -> bool {
    if msg.role != Role::Assistant {
        return false;
    }
    msg.tool_calls.as_ref().is_some_and(|calls| {
        calls
            .iter()
            .any(|tc| !tc.id.trim().is_empty() && tc.id == tool_call_id)
    })
}

fn remap_duplicate_tool_call_ids(messages: &[Message]) -> Vec<Message> {
    let mut out = messages.to_vec();
    let mut seen_counts: HashMap<String, usize> = HashMap::new();
    let mut pending_by_original: HashMap<String, Vec<String>> = HashMap::new();
    let mut generated_seq: usize = 0;

    for msg in &mut out {
        if msg.role == Role::Assistant {
            if let Some(tool_calls) = &mut msg.tool_calls {
                for tc in tool_calls.iter_mut() {
                    let original = if tc.id.trim().is_empty() {
                        generated_seq += 1;
                        format!("call_enc_{generated_seq}")
                    } else {
                        tc.id.clone()
                    };

                    let count = seen_counts.entry(original.clone()).or_insert(0);
                    *count += 1;
                    let unique = if *count == 1 {
                        original.clone()
                    } else {
                        format!("{}_dup{}", original, *count)
                    };
                    tc.id = unique.clone();
                    pending_by_original
                        .entry(original)
                        .or_default()
                        .push(unique);
                }
            }
            continue;
        }

        if msg.role != Role::Tool {
            continue;
        }

        let Some(original_id) = msg
            .tool_call_id
            .as_ref()
            .filter(|v| !v.trim().is_empty())
            .cloned()
        else {
            continue;
        };

        if let Some(stack) = pending_by_original.get_mut(&original_id)
            && let Some(unique_id) = stack.pop()
        {
            msg.tool_call_id = Some(unique_id);
        }
    }

    out
}

fn make_matching_call_adjacent(out: &mut Vec<Message>, tool_call_id: &str) -> bool {
    if out.is_empty() {
        return false;
    }

    loop {
        let Some(last) = out.last() else {
            return false;
        };
        if assistant_has_tool_call_id(last, tool_call_id) {
            return true;
        }

        let drop_candidate = last.role == Role::Assistant
            && last
                .tool_calls
                .as_ref()
                .is_none_or(|calls| calls.is_empty())
            && last
                .tool_call_id
                .as_ref()
                .is_none_or(|id| id.trim().is_empty());
        if drop_candidate {
            let _ = out.pop();
            continue;
        }
        return false;
    }
}

fn take_matching_tool_call_from_history(
    out: &mut [Message],
    tool_call_id: &str,
) -> Option<(ToolCall, usize)> {
    for (idx, msg) in out.iter_mut().enumerate().rev() {
        if msg.role != Role::Assistant {
            continue;
        }
        let Some(calls) = msg.tool_calls.as_mut() else {
            continue;
        };
        if let Some(pos) = calls.iter().position(|tc| tc.id == tool_call_id) {
            let tc = calls.remove(pos);
            if calls.is_empty() {
                msg.tool_calls = None;
            }
            return Some((tc, idx));
        }
    }
    None
}

fn trim_trailing_assistant_text_after_index(out: &mut Vec<Message>, source_idx: usize) {
    while out.len() > source_idx + 1 {
        let Some(last) = out.last() else {
            break;
        };
        let drop_candidate = last.role == Role::Assistant
            && last
                .tool_calls
                .as_ref()
                .is_none_or(|calls| calls.is_empty())
            && last
                .tool_call_id
                .as_ref()
                .is_none_or(|id| id.trim().is_empty());
        if drop_candidate {
            let _ = out.pop();
            continue;
        }
        break;
    }
}

fn encode_message(msg: &Message) -> Result<Value> {
    let role = match msg.role {
        Role::System => "system",
        Role::User => "user",
        Role::Assistant => "assistant",
        Role::Tool => "tool",
    };

    let mut obj = serde_json::json!({ "role": role });
    let map = obj.as_object_mut().unwrap();

    if msg.role == Role::Tool {
        let (tool_content, hinted_tool_call_id) = tool_message_payload(msg);
        map.insert("content".into(), Value::String(tool_content));
        let resolved_tool_call_id = msg
            .tool_call_id
            .clone()
            .filter(|v| !v.trim().is_empty())
            .or_else(|| hinted_tool_call_id.filter(|v| !v.trim().is_empty()));
        if let Some(tool_call_id) = resolved_tool_call_id {
            map.insert("tool_call_id".into(), Value::String(tool_call_id));
        }
        return Ok(obj);
    }

    match &msg.content {
        MessageContent::Text(t) => {
            map.insert("content".into(), Value::String(t.clone()));
        }
        MessageContent::Blocks(blocks) => {
            // Strip Thinking blocks for assistant messages — they are surfaced
            // via the top-level `reasoning_content` field carried in `meta`
            // (see anthropic::messages decoder). Emitting them as plain text
            // would duplicate reasoning into the visible content and break
            // upstreams like Xiaomi Mimo that require strict thinking-mode
            // round-tripping via `reasoning_content`.
            let filter_thinking = msg.role == Role::Assistant;
            let parts: Vec<Value> = blocks
                .iter()
                .filter(|b| {
                    !(filter_thinking
                        && matches!(
                            b,
                            ContentBlock::Thinking { .. } | ContentBlock::RedactedThinking { .. }
                        ))
                })
                .map(|b| encode_content_block_for_openai(b))
                .collect();
            map.insert("content".into(), Value::Array(parts));
        }
    }

    if let Some(ref tcs) = msg.tool_calls {
        let arr: Vec<Value> = tcs
            .iter()
            .map(|tc| {
                serde_json::json!({
                    "id": tc.id,
                    "type": "function",
                    "function": {
                        "name": tc.name,
                        "arguments": tc.arguments,
                    }
                })
            })
            .collect();
        map.insert("tool_calls".into(), Value::Array(arr));
    }
    if let Some(ref tid) = msg.tool_call_id {
        map.insert("tool_call_id".into(), Value::String(tid.clone()));
    }

    // Pass through any extra fields (reasoning_content, etc.)
    if let Some(Value::Object(extra)) = &msg.meta {
        for (k, v) in extra {
            map.entry(k.clone()).or_insert_with(|| v.clone());
        }
    }

    Ok(obj)
}

fn encode_content_block_for_openai(b: &ContentBlock) -> Value {
    match b {
        ContentBlock::Text { text, .. } => {
            serde_json::json!({"type": "text", "text": text})
        }
        ContentBlock::Image { source, .. } => {
            let url = media_source_to_url(source);
            serde_json::json!({
                "type": "image_url",
                "image_url": {"url": url}
            })
        }
        ContentBlock::Audio { source } => {
            let url = media_source_to_url(source);
            serde_json::json!({"type": "input_audio", "input_audio": {"data": url}})
        }
        ContentBlock::File { source } => {
            let url = media_source_to_url(source);
            serde_json::json!({"type": "file", "file": {"url": url}})
        }
        ContentBlock::ToolUse {
            id, name, input, ..
        } => {
            serde_json::json!({
                "type": "function",
                "id": id,
                "function": {"name": name, "arguments": input.to_string()}
            })
        }
        ContentBlock::ToolResult {
            tool_use_id,
            content,
            ..
        } => {
            serde_json::json!({
                "type": "text",
                "text": match content {
                    Value::String(s) => s.clone(),
                    Value::Null => String::new(),
                    other => other.to_string(),
                },
                "tool_call_id": tool_use_id,
            })
        }
        ContentBlock::Thinking { thinking, .. } => {
            // OpenAI does not support thinking blocks; pass as plain text
            serde_json::json!({"type": "text", "text": thinking})
        }
        ContentBlock::RedactedThinking { .. } => {
            serde_json::json!({"type": "text", "text": ""})
        }
        ContentBlock::Unknown { raw } => raw.clone(),
        other => {
            // Other block types (Document, SearchResult, etc.) not supported
            // by OpenAI chat/completions; serialise raw as fallback.
            serde_json::to_value(other).unwrap_or(Value::Null)
        }
    }
}

fn media_source_to_url(source: &MediaSource) -> String {
    match source {
        MediaSource::Base64 { media_type, data } => {
            format!("data:{media_type};base64,{data}")
        }
        MediaSource::Url(url) => url.clone(),
        MediaSource::FileId { file_id, .. } => file_id.clone(),
    }
}

fn tool_message_payload(msg: &Message) -> (String, Option<String>) {
    match &msg.content {
        MessageContent::Text(t) => (t.clone(), None),
        MessageContent::Blocks(blocks) => {
            for block in blocks {
                if let ContentBlock::ToolResult {
                    tool_use_id,
                    content,
                    ..
                } = block
                {
                    let text = match content {
                        Value::String(s) => s.clone(),
                        Value::Null => String::new(),
                        other => other.to_string(),
                    };
                    let hinted_id = if tool_use_id.trim().is_empty() {
                        None
                    } else {
                        Some(tool_use_id.clone())
                    };
                    return (text, hinted_id);
                }
            }
            (msg.content.to_text(), None)
        }
    }
}
