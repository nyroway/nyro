use anyhow::Result;
use serde_json::Value;
use std::collections::HashMap;

use crate::protocol::ir::request::ToolCall;
use crate::protocol::ir::response::ResponseItem;
use crate::protocol::ir::usage::Usage;
use crate::protocol::ir::{AiResponse, AiStreamDelta};
use crate::protocol::*;

// ── Non-streaming response parser ──

pub struct GoogleResponseParser;

impl ResponseDecoder for GoogleResponseParser {
    fn parse_response(&self, resp: Value) -> Result<AiResponse> {
        let candidate = resp
            .get("candidates")
            .and_then(|c| c.as_array())
            .and_then(|a| a.first());

        let content_obj = candidate.and_then(|c| c.get("content"));

        let mut text = String::new();
        let mut tool_calls = Vec::new();
        let mut items = Vec::new();

        if let Some(parts) = content_obj
            .and_then(|c| c.get("parts"))
            .and_then(|p| p.as_array())
        {
            for part in parts {
                if let Some(t) = part.get("text").and_then(|t| t.as_str()) {
                    text.push_str(t);
                    if is_plain_text_part(part) {
                        items.push(ResponseItem::OutputText {
                            text: t.to_string(),
                        });
                    } else {
                        items.push(ResponseItem::Unknown { raw: part.clone() });
                    }
                    continue;
                }

                if let Some(fc) = part.get("functionCall") {
                    let name = fc
                        .get("name")
                        .and_then(|n| n.as_str())
                        .unwrap_or("")
                        .to_string();
                    let args = fc
                        .get("args")
                        .cloned()
                        .unwrap_or(Value::Object(Default::default()));
                    let call_id = format!("call_{}", uuid::Uuid::new_v4().simple());
                    let arguments = args.to_string();
                    tool_calls.push(ToolCall {
                        id: call_id.clone(),
                        name: name.clone(),
                        arguments: arguments.clone(),
                    });
                    if is_plain_function_call_part(part) {
                        items.push(ResponseItem::FunctionCall {
                            call_id,
                            name,
                            arguments,
                        });
                    } else {
                        items.push(ResponseItem::Unknown { raw: part.clone() });
                    }
                    continue;
                }

                items.push(ResponseItem::Unknown { raw: part.clone() });
            }
        }

        let stop_reason = candidate
            .and_then(|c| c.get("finishReason"))
            .and_then(|v| v.as_str())
            .map(|r| match r {
                "STOP" => "stop".to_string(),
                "MAX_TOKENS" => "length".to_string(),
                other => other.to_lowercase(),
            });

        let usage = extract_gemini_usage(&resp);

        let model = resp
            .get("modelVersion")
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();
        let response_id = resp
            .get("responseId")
            .and_then(|v| v.as_str())
            .map(str::to_string)
            .unwrap_or_else(|| format!("gen-{}", uuid::Uuid::new_v4().simple()));

        let mut ai_resp = AiResponse::new(response_id, model);
        ai_resp.content = text;
        ai_resp.tool_calls = tool_calls;
        ai_resp.items = if items.is_empty() { None } else { Some(items) };
        ai_resp.stop_reason = stop_reason;
        ai_resp.usage = usage;
        preserve_google_response_metadata(&mut ai_resp, &resp);
        Ok(ai_resp)
    }
}

// ── Non-streaming response formatter ──

pub struct GoogleResponseFormatter;

impl ResponseEncoder for GoogleResponseFormatter {
    fn format_response(&self, resp: &AiResponse) -> Value {
        let parts = google_parts_from_response(resp);

        let finish_reason = resp.stop_reason.as_deref().map(|r| match r {
            "stop" => "STOP",
            "length" => "MAX_TOKENS",
            "tool_calls" => "STOP",
            other => other,
        });

        let mut out = serde_json::json!({
            "candidates": [{
                "content": {"role": "model", "parts": parts},
                "finishReason": finish_reason,
            }],
            "usageMetadata": google_usage_metadata(resp)
        });
        add_preserved_google_response_metadata(&mut out, resp);
        out
    }
}

// ── Stream parser (upstream Gemini SSE → deltas) ──

pub struct GoogleStreamParser {
    buffer: String,
    first: bool,
}

impl Default for GoogleStreamParser {
    fn default() -> Self {
        Self::new()
    }
}

impl GoogleStreamParser {
    pub fn new() -> Self {
        Self {
            buffer: String::new(),
            first: true,
        }
    }
}

impl StreamResponseDecoder for GoogleStreamParser {
    fn parse_chunk(&mut self, raw: &str) -> Result<Vec<AiStreamDelta>> {
        self.buffer.push_str(raw);
        let mut deltas = Vec::new();

        while let Some(pos) = self.buffer.find("\n\n") {
            let block = self.buffer[..pos].to_string();
            self.buffer = self.buffer[pos + 2..].to_string();

            for line in block.lines() {
                if let Some(data) = line.strip_prefix("data: ") {
                    let data = data.trim();
                    if let Ok(chunk) = serde_json::from_str::<Value>(data) {
                        parse_gemini_chunk(&chunk, &mut deltas, &mut self.first);
                    }
                }
            }
        }

        Ok(deltas)
    }

    fn finish(&mut self) -> Result<Vec<AiStreamDelta>> {
        if self.buffer.trim().is_empty() {
            return Ok(vec![]);
        }
        let remaining = std::mem::take(&mut self.buffer);
        self.parse_chunk(&format!("{remaining}\n\n"))
    }
}

fn parse_gemini_chunk(chunk: &Value, deltas: &mut Vec<AiStreamDelta>, first: &mut bool) {
    if *first {
        *first = false;
        let model = chunk
            .get("modelVersion")
            .and_then(|v| v.as_str())
            .unwrap_or("")
            .to_string();
        deltas.push(AiStreamDelta::MessageStart {
            id: format!("gen-{}", uuid::Uuid::new_v4().simple()),
            model,
        });
    }

    let candidate = chunk
        .get("candidates")
        .and_then(|c| c.as_array())
        .and_then(|a| a.first());

    if let Some(parts) = candidate
        .and_then(|c| c.get("content"))
        .and_then(|c| c.get("parts"))
        .and_then(|p| p.as_array())
    {
        for part in parts {
            if let Some(text) = part.get("text").and_then(|t| t.as_str()) {
                if is_plain_text_part(part) {
                    if !text.is_empty() {
                        deltas.push(AiStreamDelta::TextDelta(text.to_string()));
                    }
                } else {
                    deltas.push(AiStreamDelta::Unknown {
                        raw: part.to_string(),
                    });
                }
                continue;
            }

            if let Some(fc) = part.get("functionCall") {
                if is_plain_function_call_part(part) {
                    let name = fc
                        .get("name")
                        .and_then(|n| n.as_str())
                        .unwrap_or("")
                        .to_string();
                    let id = format!("call_{}", uuid::Uuid::new_v4().simple());
                    deltas.push(AiStreamDelta::ToolCallStart {
                        index: 0,
                        id,
                        name: name.clone(),
                    });
                    let args = fc.get("args").map(|a| a.to_string()).unwrap_or_default();
                    if !args.is_empty() && args != "{}" {
                        deltas.push(AiStreamDelta::ToolCallDelta {
                            index: 0,
                            arguments: args,
                        });
                    }
                } else {
                    deltas.push(AiStreamDelta::Unknown {
                        raw: part.to_string(),
                    });
                }
                continue;
            }

            deltas.push(AiStreamDelta::Unknown {
                raw: part.to_string(),
            });
        }
    }

    let u = extract_gemini_usage(chunk);
    if u.prompt_tokens > 0 || u.completion_tokens > 0 {
        deltas.push(AiStreamDelta::Usage(u));
    }
    if let Some(metadata) = google_stream_metadata(chunk) {
        deltas.push(AiStreamDelta::Unknown {
            raw: serde_json::json!({"__google_response_metadata": metadata}).to_string(),
        });
    }

    if let Some(reason) = candidate
        .and_then(|c| c.get("finishReason"))
        .and_then(|v| v.as_str())
    {
        let normalized = match reason {
            "STOP" => "stop",
            "MAX_TOKENS" => "length",
            other => other,
        };
        deltas.push(AiStreamDelta::Done {
            stop_reason: normalized.to_string(),
        });
    }
}

// ── Stream formatter (deltas → Gemini SSE) ──

pub struct GoogleStreamFormatter {
    usage: Usage,
    model: String,
    tool_names: HashMap<usize, String>,
    tool_arg_buffers: HashMap<usize, String>,
    response_metadata: serde_json::Map<String, Value>,
}

impl Default for GoogleStreamFormatter {
    fn default() -> Self {
        Self::new()
    }
}

impl GoogleStreamFormatter {
    pub fn new() -> Self {
        Self {
            usage: Usage::default(),
            model: String::new(),
            tool_names: HashMap::new(),
            tool_arg_buffers: HashMap::new(),
            response_metadata: serde_json::Map::new(),
        }
    }
}

impl StreamResponseEncoder for GoogleStreamFormatter {
    fn format_deltas(&mut self, deltas: &[AiStreamDelta]) -> Vec<SseEvent> {
        let mut events = Vec::new();

        for delta in deltas {
            match delta {
                AiStreamDelta::MessageStart { model, .. } => {
                    self.model = model.clone();
                }
                AiStreamDelta::ThinkingDelta(text) => {
                    let chunk = serde_json::json!({
                        "candidates": [{
                            "content": {"role": "model", "parts": [{"text": text}]},
                        }],
                        "modelVersion": self.model,
                    });
                    events.push(SseEvent::new(None, chunk.to_string()));
                }
                AiStreamDelta::ThinkingSignature(_) => {}
                AiStreamDelta::TextDelta(text) => {
                    let chunk = serde_json::json!({
                        "candidates": [{
                            "content": {"role": "model", "parts": [{"text": text}]},
                        }],
                        "modelVersion": self.model,
                    });
                    events.push(SseEvent::new(None, chunk.to_string()));
                }
                AiStreamDelta::ToolCallStart { index, id: _, name } => {
                    self.tool_names.insert(*index, name.clone());
                    self.tool_arg_buffers.insert(*index, String::new());
                }
                AiStreamDelta::ToolCallDelta { index, arguments } => {
                    let Some(name) = self.tool_names.get(index).cloned() else {
                        continue;
                    };
                    let buf = self.tool_arg_buffers.entry(*index).or_default();
                    buf.push_str(arguments);
                    let Ok(args) = serde_json::from_str::<Value>(buf) else {
                        continue;
                    };
                    let normalized_args = normalize_tool_args(&name, args);
                    let chunk = serde_json::json!({
                        "candidates": [{
                            "content": {"role": "model", "parts": [{
                                "functionCall": {"name": name, "args": normalized_args}
                            }]},
                        }],
                    });
                    events.push(SseEvent::new(None, chunk.to_string()));
                }
                AiStreamDelta::Usage(u) => {
                    if u.prompt_tokens > 0 {
                        self.usage.prompt_tokens = u.prompt_tokens;
                    }
                    if u.completion_tokens > 0 {
                        self.usage.completion_tokens = u.completion_tokens;
                    }
                }
                AiStreamDelta::Unknown { raw } => {
                    let Ok(value) = serde_json::from_str::<Value>(raw) else {
                        continue;
                    };
                    if let Some(metadata) = value
                        .get("__google_response_metadata")
                        .and_then(Value::as_object)
                    {
                        merge_json_object(&mut self.response_metadata, metadata);
                        continue;
                    }
                    let chunk = serde_json::json!({
                        "candidates": [{
                            "content": {"role": "model", "parts": [value]},
                        }],
                        "modelVersion": self.model,
                    });
                    events.push(SseEvent::new(None, chunk.to_string()));
                }
                AiStreamDelta::Done { stop_reason } => {
                    let gemini_reason = match stop_reason.as_str() {
                        "stop" => "STOP",
                        "length" => "MAX_TOKENS",
                        other => other,
                    };
                    let mut chunk = serde_json::json!({
                        "candidates": [{
                            "content": {"role": "model", "parts": []},
                            "finishReason": gemini_reason,
                        }],
                        "usageMetadata": merge_usage_counts(
                            self.response_metadata
                                .get("usageMetadata")
                                .cloned()
                                .unwrap_or_else(|| google_usage_from_counts(&self.usage)),
                            &AiResponse {
                                usage: self.usage.clone(),
                                ..AiResponse::new("", self.model.clone())
                            },
                        )
                    });
                    add_stream_response_metadata(&mut chunk, &self.response_metadata);
                    events.push(SseEvent::new(None, chunk.to_string()));
                }
                _ => {}
            }
        }

        events
    }

    fn format_done(&mut self) -> Vec<SseEvent> {
        vec![]
    }

    fn usage(&self) -> Usage {
        self.usage.clone()
    }
}

fn google_parts_from_response(resp: &AiResponse) -> Vec<Value> {
    if let Some(items) = &resp.items {
        let mut parts = Vec::new();
        for item in items {
            match item {
                ResponseItem::OutputText { text } if !text.is_empty() => {
                    parts.push(serde_json::json!({"text": text}));
                }
                ResponseItem::FunctionCall {
                    name, arguments, ..
                } => {
                    let args: Value = serde_json::from_str(arguments)
                        .unwrap_or(Value::Object(Default::default()));
                    parts.push(serde_json::json!({
                        "functionCall": {"name": name, "args": args}
                    }));
                }
                ResponseItem::Unknown { raw } => {
                    parts.push(raw.clone());
                }
                _ => {}
            }
        }
        if !parts.is_empty() {
            return parts;
        }
    }

    let mut parts = Vec::new();

    if !resp.content.is_empty() {
        parts.push(serde_json::json!({"text": resp.content}));
    }

    for tc in &resp.tool_calls {
        let args: Value =
            serde_json::from_str(&tc.arguments).unwrap_or(Value::Object(Default::default()));
        parts.push(serde_json::json!({
            "functionCall": {"name": tc.name, "args": args}
        }));
    }

    parts
}

fn is_plain_text_part(part: &Value) -> bool {
    part.as_object()
        .is_some_and(|obj| obj.len() == 1 && obj.get("text").is_some_and(Value::is_string))
}

fn is_plain_function_call_part(part: &Value) -> bool {
    part.as_object()
        .is_some_and(|obj| obj.len() == 1 && obj.contains_key("functionCall"))
}

fn preserve_google_response_metadata(resp: &mut AiResponse, raw: &Value) {
    let mut metadata = serde_json::Map::new();
    if let Some(obj) = raw.as_object() {
        for (key, value) in obj {
            if key != "candidates" {
                metadata.insert(key.clone(), value.clone());
            }
        }
    }

    if let Some(candidate) = raw
        .get("candidates")
        .and_then(Value::as_array)
        .and_then(|arr| arr.first())
        .and_then(Value::as_object)
    {
        let mut candidate_extra = serde_json::Map::new();
        for (key, value) in candidate {
            if key != "content" && key != "finishReason" {
                candidate_extra.insert(key.clone(), value.clone());
            }
        }
        if !candidate_extra.is_empty() {
            metadata.insert(
                "__candidate_extra".to_string(),
                Value::Object(candidate_extra),
            );
        }

        if let Some(content) = candidate.get("content").and_then(Value::as_object) {
            let mut content_extra = serde_json::Map::new();
            for (key, value) in content {
                if key != "role" && key != "parts" {
                    content_extra.insert(key.clone(), value.clone());
                }
            }
            if !content_extra.is_empty() {
                metadata.insert("__content_extra".to_string(), Value::Object(content_extra));
            }
        }
    }

    if !metadata.is_empty() {
        resp.vendor.ingress.insert(
            "__google_response_metadata".to_string(),
            Value::Object(metadata),
        );
    }
}

fn add_preserved_google_response_metadata(out: &mut Value, resp: &AiResponse) {
    let Some(metadata) = resp
        .vendor
        .ingress
        .get("__google_response_metadata")
        .and_then(Value::as_object)
    else {
        if !resp.model.is_empty() {
            out.as_object_mut()
                .expect("Gemini response is an object")
                .insert(
                    "modelVersion".to_string(),
                    Value::String(resp.model.clone()),
                );
        }
        if !resp.id.is_empty() {
            out.as_object_mut()
                .expect("Gemini response is an object")
                .insert("responseId".to_string(), Value::String(resp.id.clone()));
        }
        return;
    };

    let obj = out.as_object_mut().expect("Gemini response is an object");
    for (key, value) in metadata {
        if key.starts_with("__") {
            continue;
        }
        if key == "usageMetadata" {
            obj.insert(
                "usageMetadata".to_string(),
                merge_usage_counts(value.clone(), resp),
            );
        } else {
            obj.insert(key.clone(), value.clone());
        }
    }
    if let Some(candidate_extra) = metadata.get("__candidate_extra").and_then(Value::as_object)
        && let Some(candidate) = out
            .get_mut("candidates")
            .and_then(Value::as_array_mut)
            .and_then(|arr| arr.first_mut())
            .and_then(Value::as_object_mut)
    {
        merge_json_object(candidate, candidate_extra);
    }
    if let Some(content_extra) = metadata.get("__content_extra").and_then(Value::as_object)
        && let Some(content) = out
            .get_mut("candidates")
            .and_then(Value::as_array_mut)
            .and_then(|arr| arr.first_mut())
            .and_then(|candidate| candidate.get_mut("content"))
            .and_then(Value::as_object_mut)
    {
        merge_json_object(content, content_extra);
    }
}

fn google_usage_metadata(resp: &AiResponse) -> Value {
    let preserved = resp
        .vendor
        .ingress
        .get("__google_response_metadata")
        .and_then(|m| m.get("usageMetadata"));
    if let Some(usage) = preserved {
        return merge_usage_counts(usage.clone(), resp);
    }

    serde_json::json!({
        "promptTokenCount": resp.usage.prompt_tokens,
        "candidatesTokenCount": resp.usage.completion_tokens,
        "totalTokenCount": resp.usage.prompt_tokens + resp.usage.completion_tokens,
    })
}

fn merge_usage_counts(mut usage: Value, resp: &AiResponse) -> Value {
    let Some(obj) = usage.as_object_mut() else {
        return google_usage_metadata_fallback(resp);
    };
    obj.entry("promptTokenCount".to_string())
        .or_insert_with(|| serde_json::json!(resp.usage.prompt_tokens));
    obj.entry("candidatesTokenCount".to_string())
        .or_insert_with(|| serde_json::json!(resp.usage.completion_tokens));
    obj.entry("totalTokenCount".to_string()).or_insert_with(|| {
        serde_json::json!(resp.usage.prompt_tokens + resp.usage.completion_tokens)
    });
    usage
}

fn google_usage_metadata_fallback(resp: &AiResponse) -> Value {
    google_usage_from_counts(&resp.usage)
}

fn google_usage_from_counts(usage: &Usage) -> Value {
    serde_json::json!({
        "promptTokenCount": usage.prompt_tokens,
        "candidatesTokenCount": usage.completion_tokens,
        "totalTokenCount": usage.prompt_tokens + usage.completion_tokens,
    })
}

fn merge_json_object(
    target: &mut serde_json::Map<String, Value>,
    source: &serde_json::Map<String, Value>,
) {
    for (key, value) in source {
        target.entry(key.clone()).or_insert_with(|| value.clone());
    }
}

fn google_stream_metadata(chunk: &Value) -> Option<Value> {
    let mut metadata = serde_json::Map::new();
    if let Some(obj) = chunk.as_object() {
        for (key, value) in obj {
            if key != "candidates" {
                metadata.insert(key.clone(), value.clone());
            }
        }
    }
    if metadata.is_empty() {
        None
    } else {
        Some(Value::Object(metadata))
    }
}

fn add_stream_response_metadata(out: &mut Value, metadata: &serde_json::Map<String, Value>) {
    let obj = out
        .as_object_mut()
        .expect("Gemini stream chunk is an object");
    for (key, value) in metadata {
        if key == "usageMetadata" || key == "candidates" {
            continue;
        }
        obj.entry(key.clone()).or_insert_with(|| value.clone());
    }
}

fn extract_gemini_usage(v: &Value) -> Usage {
    let usage = v
        .get("usageMetadata")
        .or_else(|| v.get("usage_metadata"))
        .or_else(|| v.get("usage"));
    let Some(u) = usage else {
        return Usage::default();
    };

    let input = first_u64(
        u,
        &[
            "promptTokenCount",
            "prompt_tokens",
            "inputTokenCount",
            "input_tokens",
        ],
    )
    .unwrap_or(0);
    let output = first_u64(
        u,
        &[
            "candidatesTokenCount",
            "completion_tokens",
            "outputTokenCount",
            "output_tokens",
        ],
    )
    .unwrap_or(0);

    Usage {
        prompt_tokens: input as u32,
        completion_tokens: output as u32,
        ..Usage::default()
    }
}

fn first_u64(obj: &Value, keys: &[&str]) -> Option<u64> {
    keys.iter()
        .find_map(|k| obj.get(*k).and_then(|v| v.as_u64()))
}

fn normalize_tool_args(tool_name: &str, mut args: Value) -> Value {
    let Some(obj) = args.as_object_mut() else {
        return args;
    };

    if let Some(v) = obj.get("exclude_patterns").cloned() {
        obj.insert(
            "exclude_patterns".to_string(),
            normalize_stringified_string_array(v),
        );
    }
    if let Some(v) = obj.remove("exclude_pattern") {
        let normalized = match v {
            Value::String(s) => Value::Array(vec![Value::String(s)]),
            other => normalize_stringified_string_array(other),
        };
        obj.entry("exclude_patterns".to_string())
            .or_insert(normalized);
    }

    match tool_name {
        "glob" => {
            if let Some(v) = obj.remove("include_pattern") {
                obj.entry("pattern".to_string()).or_insert(v);
            }
            if let Some(v) = obj.remove("path") {
                obj.entry("root_dir".to_string()).or_insert(v);
            }
            if let Some(v) = obj.remove("search_root") {
                obj.entry("root_dir".to_string()).or_insert(v);
            }
        }
        "list_directory" => {
            if let Some(v) = obj.remove("path") {
                obj.entry("dir_path".to_string()).or_insert(v);
            }
        }
        _ => {}
    }

    args
}

fn normalize_stringified_string_array(v: Value) -> Value {
    match v {
        Value::String(s) => {
            let parsed = serde_json::from_str::<Value>(&s).ok();
            if let Some(Value::Array(arr)) = parsed {
                let only_strings = arr.iter().all(|item| item.is_string());
                if only_strings {
                    return Value::Array(arr);
                }
            }
            Value::String(s)
        }
        Value::Array(arr) => Value::Array(arr),
        other => other,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::{
        ResponseDecoder, ResponseEncoder, StreamResponseDecoder, StreamResponseEncoder,
    };

    #[test]
    fn parse_and_format_response_preserves_inline_data_part() {
        let upstream = serde_json::json!({
            "candidates": [{
                "content": {
                    "role": "model",
                    "parts": [{
                        "inlineData": {
                            "mimeType": "image/png",
                            "data": "iVBORw0KGgoAAAANSUhEUgA"
                        }
                    }]
                },
                "finishReason": "STOP"
            }],
            "usageMetadata": {
                "promptTokenCount": 24,
                "candidatesTokenCount": 1120,
                "totalTokenCount": 1144
            },
            "modelVersion": "gemini-3.1-flash-image-preview"
        });

        let parsed = GoogleResponseParser.parse_response(upstream).unwrap();
        let formatted = GoogleResponseFormatter.format_response(&parsed);

        assert_eq!(
            formatted["candidates"][0]["content"]["parts"][0]["inlineData"]["mimeType"],
            "image/png"
        );
        assert_eq!(
            formatted["candidates"][0]["content"]["parts"][0]["inlineData"]["data"],
            "iVBORw0KGgoAAAANSUhEUgA"
        );
        assert_eq!(formatted["usageMetadata"]["promptTokenCount"], 24);
        assert_eq!(formatted["usageMetadata"]["candidatesTokenCount"], 1120);
    }

    #[test]
    fn parse_and_format_response_preserves_future_parts_and_metadata() {
        let upstream = serde_json::json!({
            "candidates": [{
                "content": {
                    "role": "model",
                    "futureContentField": {"keep": true},
                    "parts": [
                        {"text": "hello", "futureTextField": 7},
                        {"futurePart": {"foo": "bar"}}
                    ]
                },
                "finishReason": "STOP",
                "futureCandidateField": {"rank": 1}
            }],
            "usageMetadata": {
                "promptTokenCount": 24,
                "candidatesTokenCount": 1120,
                "totalTokenCount": 1144,
                "trafficType": "ON_DEMAND",
                "promptTokensDetails": [{"modality": "TEXT", "tokenCount": 24}],
                "candidatesTokensDetails": [{"modality": "IMAGE", "tokenCount": 1120}]
            },
            "modelVersion": "gemini-3.1-flash-image-preview",
            "responseId": "resp-future",
            "futureTopLevelField": {"trace": "abc"}
        });

        let parsed = GoogleResponseParser.parse_response(upstream).unwrap();
        let formatted = GoogleResponseFormatter.format_response(&parsed);

        assert_eq!(
            formatted["candidates"][0]["content"]["parts"][0]["futureTextField"],
            7
        );
        assert_eq!(
            formatted["candidates"][0]["content"]["parts"][1]["futurePart"]["foo"],
            "bar"
        );
        assert_eq!(
            formatted["candidates"][0]["content"]["futureContentField"]["keep"],
            true
        );
        assert_eq!(
            formatted["candidates"][0]["futureCandidateField"]["rank"],
            1
        );
        assert_eq!(formatted["futureTopLevelField"]["trace"], "abc");
        assert_eq!(formatted["usageMetadata"]["trafficType"], "ON_DEMAND");
        assert_eq!(
            formatted["usageMetadata"]["candidatesTokensDetails"][0]["modality"],
            "IMAGE"
        );
    }

    #[test]
    fn stream_parser_and_formatter_preserve_inline_data_part() {
        let raw = concat!(
            "data: {",
            "\"candidates\":[{",
            "\"content\":{\"role\":\"model\",\"parts\":[{",
            "\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"iVBORw0KGgoAAAANSUhEUgAABY+yvQxDX\"}",
            "}]},",
            "\"finishReason\":\"STOP\"}],",
            "\"usageMetadata\":{\"promptTokenCount\":24,\"candidatesTokenCount\":1120,\"totalTokenCount\":1144},",
            "\"modelVersion\":\"gemini-3.1-flash-image-preview\"",
            "}\n\n"
        );

        let mut parser = GoogleStreamParser::new();
        let deltas = parser.parse_chunk(raw).unwrap();
        let mut formatter = GoogleStreamFormatter::new();
        let events = formatter.format_deltas(&deltas);

        let image_event = events
            .iter()
            .map(|event| serde_json::from_str::<Value>(&event.data).unwrap())
            .find(|value| {
                value["candidates"][0]["content"]["parts"]
                    .as_array()
                    .is_some_and(|parts| parts.iter().any(|part| part.get("inlineData").is_some()))
            })
            .expect("expected an SSE event containing the inlineData part");

        assert_eq!(
            image_event["candidates"][0]["content"]["parts"][0]["inlineData"]["mimeType"],
            "image/png"
        );
        assert_eq!(
            image_event["candidates"][0]["content"]["parts"][0]["inlineData"]["data"],
            "iVBORw0KGgoAAAANSUhEUgAABY+yvQxDX"
        );

        let usage_event = events
            .iter()
            .map(|event| serde_json::from_str::<Value>(&event.data).unwrap())
            .find(|value| value.get("usageMetadata").is_some())
            .expect("expected terminal usage event");
        assert_eq!(usage_event["usageMetadata"]["promptTokenCount"], 24);
        assert_eq!(usage_event["usageMetadata"]["candidatesTokenCount"], 1120);
    }

    #[test]
    fn stream_parser_and_formatter_preserve_future_part_and_usage_details() {
        let chunk = serde_json::json!({
            "candidates": [{
                "content": {
                    "role": "model",
                    "parts": [{"futurePart": {"foo": "bar"}}]
                },
                "finishReason": "STOP"
            }],
            "usageMetadata": {
                "promptTokenCount": 24,
                "candidatesTokenCount": 1120,
                "totalTokenCount": 1144,
                "trafficType": "ON_DEMAND",
                "candidatesTokensDetails": [{"modality": "IMAGE", "tokenCount": 1120}]
            },
            "modelVersion": "gemini-3.1-flash-image-preview",
            "responseId": "stream-future"
        });
        let raw = format!("data: {chunk}\n\n");

        let mut parser = GoogleStreamParser::new();
        let deltas = parser.parse_chunk(&raw).unwrap();
        let mut formatter = GoogleStreamFormatter::new();
        let events = formatter.format_deltas(&deltas);

        let future_part_event = events
            .iter()
            .map(|event| serde_json::from_str::<Value>(&event.data).unwrap())
            .find(|value| {
                value["candidates"][0]["content"]["parts"]
                    .as_array()
                    .is_some_and(|parts| parts.iter().any(|part| part.get("futurePart").is_some()))
            })
            .expect("expected an SSE event containing the future part");
        assert_eq!(
            future_part_event["candidates"][0]["content"]["parts"][0]["futurePart"]["foo"],
            "bar"
        );

        let usage_event = events
            .iter()
            .map(|event| serde_json::from_str::<Value>(&event.data).unwrap())
            .find(|value| value.get("usageMetadata").is_some())
            .expect("expected terminal usage event");
        assert_eq!(usage_event["usageMetadata"]["trafficType"], "ON_DEMAND");
        assert_eq!(
            usage_event["usageMetadata"]["candidatesTokensDetails"][0]["modality"],
            "IMAGE"
        );
        assert_eq!(usage_event["responseId"], "stream-future");
    }
}
