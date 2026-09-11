//! Stateless Responses wire shapes. Unsupported item kinds are rejected by serde.
use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::collections::BTreeMap;

#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Request {
    pub model: String,
    pub input: Input,
    pub instructions: Option<String>,
    pub stream: Option<bool>,
    pub max_output_tokens: Option<u32>,
    pub temperature: Option<f64>,
    pub top_p: Option<f64>,
    pub tools: Option<Vec<FunctionTool>>,
    pub tool_choice: Option<ToolChoice>,
    pub parallel_tool_calls: Option<bool>,
    pub text: Option<TextConfig>,
    pub store: Option<bool>,
    pub background: Option<bool>,
    pub truncation: Option<String>,
    pub include: Option<Vec<String>>,
    pub stream_options: Option<StreamOptions>,
    pub metadata: Option<BTreeMap<String, String>>,
    pub service_tier: Option<String>,
    pub user: Option<String>,
    pub safety_identifier: Option<String>,
    pub prompt_cache_key: Option<String>,
    pub prompt_cache_retention: Option<super::PromptCacheRetention>,
    pub prompt_cache_options: Option<super::PromptCacheOptions>,
}
#[derive(Debug, Clone, Deserialize)]
#[serde(untagged)]
pub enum Input {
    Text(String),
    Items(Vec<InputItem>),
}
#[derive(Debug, Clone, Deserialize)]
#[serde(untagged)]
pub enum InputItem {
    Message(InputMessage),
    Item(HistoryItem),
}
#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct InputMessage {
    pub r#type: Option<String>,
    pub id: Option<String>,
    pub role: String,
    pub content: InputContent,
    pub status: Option<String>,
}
#[derive(Debug, Clone, Deserialize)]
#[serde(untagged)]
pub enum InputContent {
    Text(String),
    Parts(Vec<InputPart>),
}
#[derive(Debug, Clone, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case", deny_unknown_fields)]
pub enum InputPart {
    InputImage {
        image_url: String,
        detail: Option<String>,
        #[serde(default, deserialize_with = "super::present")]
        prompt_cache_breakpoint: Option<super::PromptCacheBreakpoint>,
    },
    InputText {
        text: String,
        #[serde(default, deserialize_with = "super::present")]
        prompt_cache_breakpoint: Option<super::PromptCacheBreakpoint>,
    },
    OutputText {
        text: String,
        #[serde(default)]
        annotations: Vec<Value>,
        #[serde(default)]
        logprobs: Vec<Value>,
    },
    Refusal {
        refusal: String,
    },
}
#[derive(Debug, Clone, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case", deny_unknown_fields)]
pub enum HistoryItem {
    FunctionCall {
        id: Option<String>,
        call_id: String,
        name: String,
        arguments: String,
        status: Option<String>,
    },
    FunctionCallOutput {
        id: Option<String>,
        call_id: String,
        output: FunctionOutput,
        status: Option<String>,
    },
}
#[derive(Debug, Clone, Deserialize)]
#[serde(untagged)]
pub enum FunctionOutput {
    Text(String),
    Parts(Vec<FunctionOutputPart>),
}
#[derive(Debug, Clone, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case", deny_unknown_fields)]
pub enum FunctionOutputPart {
    InputText {
        text: String,
        #[serde(default, deserialize_with = "super::present")]
        prompt_cache_breakpoint: Option<super::PromptCacheBreakpoint>,
    },
}
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(tag = "type", rename_all = "snake_case", deny_unknown_fields)]
pub enum FunctionTool {
    Function {
        name: String,
        #[serde(skip_serializing_if = "Option::is_none")]
        description: Option<String>,
        #[serde(skip_serializing_if = "Option::is_none")]
        parameters: Option<Value>,
        #[serde(skip_serializing_if = "Option::is_none")]
        strict: Option<bool>,
    },
}
#[derive(Debug, Clone, Deserialize)]
#[serde(untagged)]
pub enum ToolChoice {
    Mode(super::chat::ToolMode),
    Named(NamedTool),
}
#[derive(Debug, Clone, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case", deny_unknown_fields)]
pub enum NamedTool {
    Function { name: String },
}
#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TextConfig {
    pub format: TextFormat,
}
#[derive(Debug, Clone, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case", deny_unknown_fields)]
pub enum TextFormat {
    Text,
    JsonObject,
    JsonSchema {
        name: String,
        schema: Value,
        strict: Option<bool>,
        description: Option<String>,
    },
}
#[derive(Debug, Clone, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct StreamOptions {
    pub include_obfuscation: Option<bool>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct Response {
    pub id: String,
    pub object: String,
    pub created_at: u64,
    pub model: String,
    pub status: String,
    pub output: Vec<OutputItem>,
    pub usage: Option<Usage>,
    pub error: Option<Value>,
    pub incomplete_details: Option<IncompleteDetails>,
    // Request echoes are validated/normalized by the codec, never forwarded as opaque IR.
    #[serde(flatten)]
    pub extra: BTreeMap<String, Value>,
}
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct IncompleteDetails {
    pub reason: String,
}
#[derive(Debug, Clone, PartialEq, Deserialize, Serialize)]
#[serde(tag = "type", rename_all = "snake_case", deny_unknown_fields)]
pub enum OutputItem {
    Message {
        id: String,
        role: String,
        status: String,
        content: Vec<OutputPart>,
    },
    FunctionCall {
        id: String,
        call_id: String,
        name: String,
        arguments: String,
        status: String,
    },
}
impl OutputItem {
    pub fn id(&self) -> &str {
        match self {
            Self::Message { id, .. } | Self::FunctionCall { id, .. } => id,
        }
    }
    pub fn status(&self) -> &str {
        match self {
            Self::Message { status, .. } | Self::FunctionCall { status, .. } => status,
        }
    }
}
#[derive(Debug, Clone, PartialEq, Deserialize, Serialize)]
#[serde(tag = "type", rename_all = "snake_case", deny_unknown_fields)]
pub enum OutputPart {
    OutputText {
        text: String,
        #[serde(default)]
        annotations: Vec<Value>,
        #[serde(default, skip_serializing_if = "Vec::is_empty")]
        logprobs: Vec<Value>,
    },
    Refusal {
        refusal: String,
    },
}
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Usage {
    pub input_tokens: u64,
    pub output_tokens: u64,
    pub total_tokens: u64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub input_tokens_details: Option<InputTokensDetails>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub output_tokens_details: Option<OutputTokensDetails>,
}
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct InputTokensDetails {
    pub cached_tokens: u64,
    #[serde(
        default,
        deserialize_with = "super::present",
        skip_serializing_if = "Option::is_none"
    )]
    pub cache_write_tokens: Option<u64>,
}
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct OutputTokensDetails {
    pub reasoning_tokens: u64,
}

/// Typed payload leaves; event-specific required fields and lifecycle are validated by the codec.
#[derive(Debug, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct StreamEvent {
    pub r#type: String,
    pub sequence_number: u64,
    pub response: Option<Response>,
    pub output_index: Option<usize>,
    pub content_index: Option<usize>,
    pub item_id: Option<String>,
    pub item: Option<OutputItem>,
    pub part: Option<OutputPart>,
    pub delta: Option<String>,
    pub text: Option<String>,
    pub refusal: Option<String>,
    pub arguments: Option<String>,
    pub name: Option<String>,
    pub obfuscation: Option<String>,
    #[serde(default)]
    pub logprobs: Vec<Value>,
}
