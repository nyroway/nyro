use serde::{Deserialize, Serialize};
use serde_json::Value;

// ── Primitive helpers ─────────────────────────────────────────────────────────

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct CacheControl {
    #[serde(rename = "type")]
    pub kind: String, // "ephemeral"
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct ThinkingConfig {
    #[serde(rename = "type")]
    pub kind: String, // "enabled" | "disabled"
    #[serde(skip_serializing_if = "Option::is_none")]
    pub budget_tokens: Option<u32>,
}

// ── Top-level request ─────────────────────────────────────────────────────────

#[derive(Debug, Deserialize, Serialize)]
pub struct AnthropicRequest {
    pub model: String,
    pub messages: Vec<AnthropicMessage>,
    pub max_tokens: u32,
    #[serde(default)]
    pub stream: bool,
    pub system: Option<AnthropicSystem>,
    pub temperature: Option<f64>,
    pub top_p: Option<f64>,
    pub top_k: Option<u32>,
    pub tools: Option<Vec<AnthropicToolDef>>,
    pub tool_choice: Option<Value>,

    // PR-10 additions ─────────────────────────────────────────────────────────
    pub thinking: Option<ThinkingConfig>,
    pub context_management: Option<Value>,
    pub container: Option<String>,
    pub service_tier: Option<String>,
    pub metadata: Option<Value>,
    pub stop_sequences: Option<Vec<String>>,
}

// ── System ────────────────────────────────────────────────────────────────────

/// Anthropic `system` field: plain text OR array of typed content blocks
/// (the block form is required when using `cache_control`).
#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(untagged)]
pub enum AnthropicSystem {
    Text(String),
    Blocks(Vec<AnthropicSystemBlock>),
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct AnthropicSystemBlock {
    #[serde(rename = "type")]
    pub kind: String, // currently always "text"
    pub text: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cache_control: Option<CacheControl>,
}

// ── Messages ──────────────────────────────────────────────────────────────────

#[derive(Debug, Deserialize, Serialize)]
pub struct AnthropicMessage {
    pub role: String,
    pub content: AnthropicContent,
}

#[derive(Debug, Deserialize, Serialize)]
#[serde(untagged)]
pub enum AnthropicContent {
    Text(String),
    Blocks(Vec<AnthropicContentBlock>),
}

// ── Content blocks ────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Deserialize, Serialize)]
#[serde(tag = "type")]
pub enum AnthropicContentBlock {
    #[serde(rename = "text")]
    Text {
        text: String,
        #[serde(skip_serializing_if = "Option::is_none")]
        cache_control: Option<CacheControl>,
    },
    #[serde(rename = "thinking")]
    Thinking {
        thinking: String,
        #[serde(default)]
        signature: Option<String>,
        #[serde(skip_serializing_if = "Option::is_none")]
        cache_control: Option<CacheControl>,
    },
    #[serde(rename = "image")]
    Image {
        source: AnthropicImageSource,
        #[serde(skip_serializing_if = "Option::is_none")]
        cache_control: Option<CacheControl>,
    },
    #[serde(rename = "tool_use")]
    ToolUse {
        id: String,
        name: String,
        input: Value,
        #[serde(skip_serializing_if = "Option::is_none")]
        cache_control: Option<CacheControl>,
    },
    #[serde(rename = "tool_result")]
    ToolResult {
        tool_use_id: String,
        content: Option<Value>,
        #[serde(skip_serializing_if = "Option::is_none")]
        cache_control: Option<CacheControl>,
    },
    /// PDF / text / URL document for context (claude-3-5-sonnet+ with document support).
    #[serde(rename = "document")]
    Document {
        source: DocumentSource,
        #[serde(skip_serializing_if = "Option::is_none")]
        title: Option<String>,
        #[serde(skip_serializing_if = "Option::is_none")]
        context: Option<String>,
        #[serde(skip_serializing_if = "Option::is_none")]
        citations: Option<Value>,
        #[serde(skip_serializing_if = "Option::is_none")]
        cache_control: Option<CacheControl>,
    },
    /// Audio input block.
    #[serde(rename = "input_audio")]
    InputAudio {
        source: AudioSource,
        #[serde(skip_serializing_if = "Option::is_none")]
        cache_control: Option<CacheControl>,
    },
}

impl AnthropicContentBlock {
    pub fn cache_control(&self) -> Option<&CacheControl> {
        match self {
            Self::Text { cache_control, .. }
            | Self::Thinking { cache_control, .. }
            | Self::Image { cache_control, .. }
            | Self::ToolUse { cache_control, .. }
            | Self::ToolResult { cache_control, .. }
            | Self::Document { cache_control, .. }
            | Self::InputAudio { cache_control, .. } => cache_control.as_ref(),
        }
    }

    pub fn is_exotic(&self) -> bool {
        matches!(self, Self::Document { .. } | Self::InputAudio { .. })
    }
}

// ── Source types ──────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct AnthropicImageSource {
    #[serde(rename = "type")]
    pub source_type: String, // "base64" | "url"
    #[serde(skip_serializing_if = "Option::is_none")]
    pub media_type: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub url: Option<String>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct DocumentSource {
    #[serde(rename = "type")]
    pub kind: String, // "base64" | "text" | "url" | "content"
    #[serde(skip_serializing_if = "Option::is_none")]
    pub media_type: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub url: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub content: Option<Value>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct AudioSource {
    #[serde(rename = "type")]
    pub kind: String, // "base64"
    pub media_type: String, // "audio/wav" | "audio/mp3" …
    pub data: String,
}

// ── Tools ─────────────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct AnthropicToolDef {
    pub name: String,
    pub description: Option<String>,
    /// For function tools the schema goes here; built-in tools may omit it.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub input_schema: Option<Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cache_control: Option<CacheControl>,
    /// Built-in tool type string e.g. "computer_20241022", "text_editor_20241022".
    #[serde(rename = "type", skip_serializing_if = "Option::is_none")]
    pub tool_type: Option<String>,
}
