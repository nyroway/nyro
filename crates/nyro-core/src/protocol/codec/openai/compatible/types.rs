//! Wire-format types for the OpenAI Chat Completions API.
//!
//! PR-08 adds the full field set:
//! - `stream_options` (include_usage)
//! - `parallel_tool_calls`
//! - `prediction` (output prediction / cache hinting)
//! - `modalities`
//! - `audio` (audio output config)
//! - `response_format` (text / json_object / json_schema + strict)
//! - `seed`
//! - `stop`
//! - `logit_bias`
//! - `service_tier`
//! - `max_completion_tokens` (alias for `max_tokens` for o-models)
//! - `frequency_penalty`, `presence_penalty`
//! - `n` (number of completions)
//! - `user` (caller identifier for monitoring)

use std::collections::HashMap;

use serde::{Deserialize, Serialize};
use serde_json::Value;

// ── Request ───────────────────────────────────────────────────────────────────

#[derive(Debug, Deserialize, Serialize)]
pub struct OpenAIRequest {
    pub model: String,
    pub messages: Vec<OpenAIMessage>,

    #[serde(default)]
    pub stream: bool,

    // ── Generation parameters ─────────────────────────────────────────────────
    pub temperature: Option<f64>,
    /// For non-o models; `max_completion_tokens` is preferred for o-models.
    pub max_tokens: Option<u32>,
    /// Preferred for o-models; treated identically to `max_tokens` internally.
    pub max_completion_tokens: Option<u32>,
    pub top_p: Option<f64>,
    pub frequency_penalty: Option<f64>,
    pub presence_penalty: Option<f64>,
    pub seed: Option<i64>,
    pub stop: Option<StopToken>,
    pub n: Option<u32>,
    pub logit_bias: Option<HashMap<String, f64>>,

    // ── Tools ─────────────────────────────────────────────────────────────────
    pub tools: Option<Vec<Value>>,
    pub tool_choice: Option<Value>,
    /// Whether the model may issue multiple tool calls in a single turn.
    pub parallel_tool_calls: Option<bool>,

    // ── Streaming ─────────────────────────────────────────────────────────────
    pub stream_options: Option<StreamOptions>,

    // ── Output format ─────────────────────────────────────────────────────────
    pub response_format: Option<ResponseFormatWire>,
    /// Output modalities (e.g. `["text", "audio"]`).
    pub modalities: Option<Vec<String>>,
    /// Audio output configuration (requires `modalities: ["audio"]`).
    pub audio: Option<AudioConfig>,
    /// Predicted output for speculative decoding / cache hinting.
    pub prediction: Option<PredictionConfig>,

    // ── Reasoning (o-models) ──────────────────────────────────────────────────
    pub reasoning_effort: Option<String>,

    // ── Routing / SLA ─────────────────────────────────────────────────────────
    pub service_tier: Option<String>,
    /// Caller identifier surfaced in OpenAI usage dashboard.
    pub user: Option<String>,

    #[serde(flatten)]
    pub extra: HashMap<String, Value>,
}

// ── Stop token ────────────────────────────────────────────────────────────────

#[derive(Debug, Deserialize, Serialize)]
#[serde(untagged)]
pub enum StopToken {
    Single(String),
    Multiple(Vec<String>),
}

impl StopToken {
    pub fn into_vec(self) -> Vec<String> {
        match self {
            Self::Single(s) => vec![s],
            Self::Multiple(v) => v,
        }
    }
}

// ── Stream options ────────────────────────────────────────────────────────────

#[derive(Debug, Default, Deserialize, Serialize)]
pub struct StreamOptions {
    #[serde(default)]
    pub include_usage: bool,
}

// ── Response format ───────────────────────────────────────────────────────────

#[derive(Debug, Deserialize, Serialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ResponseFormatWire {
    Text,
    JsonObject,
    JsonSchema { json_schema: JsonSchemaConfig },
}

#[derive(Debug, Deserialize, Serialize)]
pub struct JsonSchemaConfig {
    pub name: String,
    pub schema: Value,
    pub strict: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
}

// ── Audio ─────────────────────────────────────────────────────────────────────

#[derive(Debug, Deserialize, Serialize)]
pub struct AudioConfig {
    pub voice: Option<String>,
    pub format: Option<String>,
}

// ── Prediction ────────────────────────────────────────────────────────────────

#[derive(Debug, Deserialize, Serialize)]
pub struct PredictionConfig {
    #[serde(rename = "type")]
    pub pred_type: String,
    pub content: Value,
}

// ── Messages ──────────────────────────────────────────────────────────────────

#[derive(Debug, Deserialize, Serialize)]
pub struct OpenAIMessage {
    pub role: String,
    pub content: Option<OpenAIContent>,
    pub tool_calls: Option<Vec<OpenAIToolCall>>,
    pub tool_call_id: Option<String>,
    /// `name` field for system/user messages.
    pub name: Option<String>,
    /// Refusal string (assistant messages).
    pub refusal: Option<String>,
    #[serde(flatten)]
    pub extra: HashMap<String, Value>,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(untagged)]
pub enum OpenAIContent {
    Text(String),
    Parts(Vec<OpenAIContentPart>),
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(tag = "type")]
pub enum OpenAIContentPart {
    #[serde(rename = "text")]
    Text { text: String },
    #[serde(rename = "image_url")]
    ImageUrl { image_url: ImageUrl },
    #[serde(rename = "input_audio")]
    InputAudio { input_audio: InputAudio },
}

#[derive(Debug, Deserialize, Serialize)]
pub struct ImageUrl {
    pub url: String,
    pub detail: Option<String>,
}

#[derive(Debug, Deserialize, Serialize)]
pub struct InputAudio {
    pub data: String,
    pub format: String,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct OpenAIToolCall {
    pub id: String,
    #[serde(rename = "type")]
    pub call_type: Option<String>,
    pub function: OpenAIFunctionCall,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct OpenAIFunctionCall {
    pub name: String,
    pub arguments: String,
}
