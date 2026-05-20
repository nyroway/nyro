//! OpenAI Chat Completions ingress decoder — produces `AiRequest` directly.
//!
//! Protocol-specific fields that don't belong in the IR core are stored in:
//! - `AiRequest.ext = Some(ProtocolExt::OpenAiChat(OpenAIChatExt { … }))` — typed Ext
//! - `AiRequest.meta.vendor.ingress` — backward-compat bag for old encoders (PR-3 will clean up)

use anyhow::Result;
use serde_json::Value;

use crate::protocol::RequestDecoder;
use crate::protocol::ids::OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1;
use crate::protocol::ir::{
    AiRequest, ContentBlock, GenerationConfig, MediaSource, Message, MessageContent, OpenAIChatExt,
    ProtocolExt, ReasoningConfig, ReasoningEffort, ResponseFormat, Role, StreamConfig, ToolCall,
    ToolChoice, ToolSpec,
};

use super::types::*;

pub struct OpenAIDecoder;

impl RequestDecoder for OpenAIDecoder {
    fn decode_request(&self, body: Value) -> Result<AiRequest> {
        let req: OpenAIRequest = serde_json::from_value(body)?;

        let messages = req
            .messages
            .into_iter()
            .map(decode_message)
            .collect::<Result<Vec<_>>>()?;

        let tools = req.tools.as_ref().map(|tools_val| {
            tools_val
                .iter()
                .filter_map(|t| {
                    let func = t.get("function")?;
                    Some(ToolSpec {
                        name: func.get("name")?.as_str()?.to_string(),
                        description: func
                            .get("description")
                            .and_then(|d| d.as_str())
                            .map(String::from),
                        parameters: func
                            .get("parameters")
                            .cloned()
                            .unwrap_or(Value::Object(Default::default())),
                        strict: func.get("strict").and_then(|v| v.as_bool()),
                        cache_control: None,
                        meta: None,
                    })
                })
                .collect()
        });

        let tool_choice = req.tool_choice.map(parse_tool_choice);

        // Prefer max_completion_tokens (o-models), fall back to max_tokens.
        let effective_max_tokens = req.max_completion_tokens.or(req.max_tokens);

        let reasoning = match &req.reasoning_effort {
            Some(s) => ReasoningConfig {
                enabled: true,
                effort: Some(parse_reasoning_effort(s)),
                ..Default::default()
            },
            None => ReasoningConfig::default(),
        };

        let include_usage = req
            .stream_options
            .as_ref()
            .map(|s| s.include_usage)
            .unwrap_or(false);

        // ── ProtocolExt ───────────────────────────────────────────────────────
        let oai_ext = OpenAIChatExt {
            audio: req
                .audio
                .as_ref()
                .and_then(|a| serde_json::to_value(a).ok()),
            logit_bias: req.logit_bias.clone(),
            modalities: req.modalities.clone(),
            n: req.n,
            prediction: req
                .prediction
                .as_ref()
                .and_then(|p| serde_json::to_value(p).ok()),
            stream_options: req
                .stream_options
                .as_ref()
                .and_then(|s| serde_json::to_value(s).ok()),
            ..Default::default()
        };

        // ── Vendor ingress bag — backward compat for old encoders (pre-PR-3) ──
        // Old OpenAI encoder reads these keys from InternalRequest.extra, which
        // is populated from meta.vendor.ingress via compat.rs.
        let mut ingress = req.extra.clone(); // unknown flatten'd fields
        macro_rules! put_opt_value {
            ($key:expr, $val:expr) => {
                if let Some(v) = $val {
                    ingress
                        .entry($key.to_string())
                        .or_insert_with(|| serde_json::to_value(v).unwrap_or(Value::Null));
                }
            };
        }
        macro_rules! put_bool {
            ($key:expr, $val:expr) => {
                if let Some(v) = $val {
                    ingress
                        .entry($key.to_string())
                        .or_insert_with(|| Value::Bool(v));
                }
            };
        }
        macro_rules! put_str {
            ($key:expr, $val:expr) => {
                if let Some(v) = $val {
                    ingress
                        .entry($key.to_string())
                        .or_insert_with(|| Value::String(v.clone()));
                }
            };
        }
        put_opt_value!("stream_options", req.stream_options.as_ref());
        put_bool!("parallel_tool_calls", req.parallel_tool_calls);
        put_opt_value!("prediction", req.prediction.as_ref());
        if let Some(ref v) = req.modalities {
            ingress
                .entry("modalities".to_string())
                .or_insert_with(|| Value::Array(v.iter().cloned().map(Value::String).collect()));
        }
        put_opt_value!("audio", req.audio.as_ref());
        if let Some(ref v) = req.response_format {
            ingress
                .entry("response_format".to_string())
                .or_insert_with(|| serde_json::to_value(v).unwrap_or(Value::Null));
        }
        if let Some(v) = req.seed {
            ingress
                .entry("seed".to_string())
                .or_insert_with(|| Value::from(v));
        }
        if let Some(ref v) = req.stop {
            ingress
                .entry("stop".to_string())
                .or_insert_with(|| match v {
                    StopToken::Single(s) => Value::String(s.clone()),
                    StopToken::Multiple(vs) => {
                        Value::Array(vs.iter().cloned().map(Value::String).collect())
                    }
                });
        }
        if let Some(ref v) = req.logit_bias {
            ingress.entry("logit_bias".to_string()).or_insert_with(|| {
                Value::Object(
                    v.iter()
                        .map(|(k, f)| (k.clone(), Value::from(*f)))
                        .collect(),
                )
            });
        }
        put_str!("service_tier", req.service_tier.as_ref());
        put_str!("reasoning_effort", req.reasoning_effort.as_ref());
        if let Some(v) = req.frequency_penalty {
            ingress
                .entry("frequency_penalty".to_string())
                .or_insert_with(|| Value::from(v));
        }
        if let Some(v) = req.presence_penalty {
            ingress
                .entry("presence_penalty".to_string())
                .or_insert_with(|| Value::from(v));
        }
        if let Some(v) = req.n {
            ingress
                .entry("n".to_string())
                .or_insert_with(|| Value::from(v));
        }
        put_str!("user", req.user.as_ref());

        // Consume stop and response_format AFTER ingress bag is built.
        let stop = req.stop.map(StopToken::into_vec);
        let response_format = req.response_format.map(parse_response_format);

        // ── Build AiRequest ───────────────────────────────────────────────────
        let mut ai_req = AiRequest::new(req.model, messages);
        ai_req.generation = GenerationConfig {
            temperature: req.temperature,
            max_tokens: effective_max_tokens,
            top_p: req.top_p,
            seed: req.seed,
            stop,
            frequency_penalty: req.frequency_penalty,
            presence_penalty: req.presence_penalty,
            ..Default::default()
        };
        ai_req.stream = StreamConfig {
            enabled: req.stream,
            include_usage,
        };
        ai_req.tools = tools;
        ai_req.tool_choice = tool_choice;
        ai_req.parallel_tool_calls = req.parallel_tool_calls;
        ai_req.reasoning = reasoning;
        ai_req.response_format = response_format;
        ai_req.ext = Some(ProtocolExt::OpenAiChat(oai_ext));
        ai_req.meta.source_protocol = Some(OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1);
        ai_req.meta.vendor.ingress = ingress;

        Ok(ai_req)
    }
}

// ── Message decoding ──────────────────────────────────────────────────────────

fn decode_message(msg: OpenAIMessage) -> Result<Message> {
    let role = match msg.role.as_str() {
        "system" | "developer" => Role::System,
        "user" => Role::User,
        "assistant" => Role::Assistant,
        "tool" => Role::Tool,
        other => anyhow::bail!("unknown role: {other}"),
    };

    let content = match msg.content {
        Some(OpenAIContent::Text(t)) => MessageContent::Text(t),
        Some(OpenAIContent::Parts(parts)) => {
            let blocks = parts
                .into_iter()
                .map(|p| match p {
                    OpenAIContentPart::Text { text } => ContentBlock::Text {
                        text,
                        cache_control: None,
                    },
                    OpenAIContentPart::ImageUrl { image_url } => {
                        let source = if image_url.url.starts_with("data:") {
                            parse_data_url_source(image_url.url)
                        } else {
                            MediaSource::Url(image_url.url)
                        };
                        ContentBlock::Image {
                            source,
                            cache_control: None,
                        }
                    }
                    OpenAIContentPart::InputAudio { input_audio } => ContentBlock::Audio {
                        source: MediaSource::Base64 {
                            media_type: format!("audio/{}", input_audio.format),
                            data: input_audio.data,
                        },
                    },
                })
                .collect();
            MessageContent::Blocks(blocks)
        }
        None => MessageContent::Text(String::new()),
    };

    let tool_calls = msg.tool_calls.map(|tcs| {
        tcs.into_iter()
            .map(|tc| ToolCall {
                id: tc.id,
                name: tc.function.name,
                arguments: tc.function.arguments,
            })
            .collect()
    });

    // Put any per-message extra fields + refusal into meta.
    let mut meta_obj = serde_json::Map::new();
    for (k, v) in msg.extra {
        meta_obj.insert(k, v);
    }
    if let Some(ref r) = msg.refusal {
        meta_obj.insert("refusal".to_string(), Value::String(r.clone()));
    }
    let meta = if meta_obj.is_empty() {
        None
    } else {
        Some(Value::Object(meta_obj))
    };

    Ok(Message {
        role,
        content,
        tool_calls,
        tool_call_id: msg.tool_call_id,
        meta,
    })
}

// ── Helpers ───────────────────────────────────────────────────────────────────

fn parse_tool_choice(v: Value) -> ToolChoice {
    match &v {
        Value::String(s) => match s.as_str() {
            "none" => ToolChoice::None,
            "auto" => ToolChoice::Auto,
            "required" => ToolChoice::Required,
            _ => ToolChoice::Raw(v),
        },
        Value::Object(obj) => {
            if obj.get("type").and_then(|t| t.as_str()) == Some("function") {
                if let Some(name) = obj
                    .get("function")
                    .and_then(|f| f.get("name"))
                    .and_then(|n| n.as_str())
                {
                    return ToolChoice::Named {
                        name: name.to_string(),
                    };
                }
            }
            ToolChoice::Raw(v)
        }
        _ => ToolChoice::Raw(v),
    }
}

fn parse_reasoning_effort(s: &str) -> ReasoningEffort {
    match s {
        "low" => ReasoningEffort::Low,
        "high" => ReasoningEffort::High,
        _ => ReasoningEffort::Medium,
    }
}

fn parse_response_format(f: ResponseFormatWire) -> ResponseFormat {
    match f {
        ResponseFormatWire::Text => ResponseFormat::Text,
        ResponseFormatWire::JsonObject => ResponseFormat::JsonObject,
        ResponseFormatWire::JsonSchema { json_schema } => ResponseFormat::JsonSchema {
            name: json_schema.name,
            schema: json_schema.schema,
            strict: json_schema.strict,
        },
    }
}

/// Parse a `data:<media_type>;base64,<data>` URL into a `MediaSource::Base64`.
/// Falls back to `MediaSource::Url` if the format is not recognised.
fn parse_data_url_source(url: String) -> MediaSource {
    if let Some(rest) = url.strip_prefix("data:") {
        if let Some(semi) = rest.find(';') {
            let media_type = rest[..semi].to_string();
            let after = &rest[semi + 1..];
            if let Some(data) = after.strip_prefix("base64,") {
                return MediaSource::Base64 {
                    media_type,
                    data: data.to_string(),
                };
            }
        }
    }
    MediaSource::Url(url)
}
