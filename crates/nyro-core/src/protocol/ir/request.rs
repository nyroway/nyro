//! `AiRequest` — the unified ingress IR for all supported protocols.
//!
//! Codec decoders (PR-2) produce `AiRequest`; codec encoders (PR-3) and the
//! dispatcher (PR-5) consume it.  Until PR-2 lands, `compat.rs` provides
//! lossless `From` conversions from/to the old `InternalRequest`.

use serde::{Deserialize, Serialize};
use serde_json::Value;

use crate::protocol::ids::ProtocolId;
use crate::protocol::ir::cache::CacheControl;
use crate::protocol::ir::envelope::RawEnvelope;
use crate::protocol::ir::ext::ProtocolExt;
use crate::protocol::ir::vendor_ext::VendorExtensions;

// ── Role ─────────────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Role {
    System,
    User,
    Assistant,
    Tool,
}

// ── Image source ─────────────────────────────────────────────────────────────

/// The data source for an image or audio content block.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum MediaSource {
    /// Inline base64-encoded data.
    Base64 { media_type: String, data: String },
    /// A URL pointing to the media.
    Url(String),
    /// A provider-side file reference.
    FileId {
        file_id: String,
        #[serde(skip_serializing_if = "Option::is_none")]
        detail: Option<String>,
    },
}

// ── Document source ───────────────────────────────────────────────────────────

/// Source for a document content block (Anthropic).
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum DocumentSource {
    Base64Pdf {
        data: String,
    },
    PlainText {
        data: String,
    },
    Url(String),
    /// Content already stored as content blocks.
    Blocks {
        content: Vec<ContentBlock>,
    },
}

// ── Content blocks ────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ContentBlock {
    // ── Text ─────────────────────────────────────────────────────────────────
    Text {
        text: String,
        #[serde(skip_serializing_if = "Option::is_none")]
        cache_control: Option<CacheControl>,
    },

    // ── Multimodal ───────────────────────────────────────────────────────────
    Image {
        source: MediaSource,
        #[serde(skip_serializing_if = "Option::is_none")]
        cache_control: Option<CacheControl>,
    },
    Audio {
        source: MediaSource,
    },
    File {
        source: MediaSource,
        #[serde(skip_serializing_if = "Option::is_none")]
        media_type: Option<String>,
    },

    // ── Reasoning / thinking ─────────────────────────────────────────────────
    /// Extended thinking output (Anthropic `ThinkingBlockParam`, Google `thought=true`).
    Thinking {
        thinking: String,
        #[serde(skip_serializing_if = "Option::is_none")]
        signature: Option<String>,
    },
    /// Redacted thinking block (Anthropic `RedactedThinkingBlockParam`).
    RedactedThinking {
        data: String,
    },

    // ── Tool calls ────────────────────────────────────────────────────────────
    ToolUse {
        id: String,
        name: String,
        input: Value,
        #[serde(skip_serializing_if = "Option::is_none")]
        cache_control: Option<CacheControl>,
    },
    ToolResult {
        tool_use_id: String,
        content: Value,
        #[serde(skip_serializing_if = "Option::is_none")]
        is_error: Option<bool>,
        #[serde(skip_serializing_if = "Option::is_none")]
        cache_control: Option<CacheControl>,
    },

    // ── Server-side tools (Anthropic) ────────────────────────────────────────
    /// A server-executed tool call (Anthropic `ServerToolUseBlockParam`,
    /// Google `Part.toolCall`).
    ServerToolUse {
        id: String,
        /// Tool name (e.g. `"web_search"`, `"code_execution"`).
        name: String,
        input: Value,
        /// Discriminator for the tool type (e.g. `"web_search"`, `"bash"`).
        #[serde(skip_serializing_if = "Option::is_none")]
        server_type: Option<String>,
        #[serde(skip_serializing_if = "Option::is_none")]
        cache_control: Option<CacheControl>,
    },
    /// Result from a server-executed tool.
    ServerToolResult {
        tool_use_id: String,
        content: Value,
        /// Discriminator matching the originating `ServerToolUse.server_type`.
        #[serde(skip_serializing_if = "Option::is_none")]
        server_type: Option<String>,
        #[serde(skip_serializing_if = "Option::is_none")]
        cache_control: Option<CacheControl>,
    },

    // ── Documents / search ────────────────────────────────────────────────────
    /// A document block (Anthropic `DocumentBlockParam`).
    Document {
        source: DocumentSource,
        #[serde(skip_serializing_if = "Option::is_none")]
        title: Option<String>,
        #[serde(skip_serializing_if = "Option::is_none")]
        context: Option<String>,
        #[serde(skip_serializing_if = "Option::is_none")]
        cache_control: Option<CacheControl>,
    },
    /// A search result block (Anthropic `SearchResultBlockParam`).
    SearchResult {
        content: Vec<ContentBlock>,
        source: String,
        title: String,
        #[serde(skip_serializing_if = "Option::is_none")]
        cache_control: Option<CacheControl>,
    },

    // ── Citations ─────────────────────────────────────────────────────────────
    /// A citation block (Anthropic citations, OpenAI Responses annotations).
    Citation {
        cited_text: String,
        source: Value,
    },

    // ── Code execution ───────────────────────────────────────────────────────
    /// Executable code produced by the model (Google `Part.executableCode`).
    ExecutableCode {
        code: String,
        #[serde(skip_serializing_if = "Option::is_none")]
        language: Option<String>,
        #[serde(skip_serializing_if = "Option::is_none")]
        id: Option<String>,
    },
    /// Code execution result (Google `Part.codeExecutionResult`,
    /// Anthropic `CodeExecutionResultBlockParam`).
    CodeExecutionResult {
        return_code: i32,
        stdout: String,
        stderr: String,
        #[serde(skip_serializing_if = "Option::is_none")]
        id: Option<String>,
    },

    // ── Container ────────────────────────────────────────────────────────────
    /// Container file upload (Anthropic `ContainerUploadBlockParam`).
    ContainerUpload {
        file_id: String,
        #[serde(skip_serializing_if = "Option::is_none")]
        cache_control: Option<CacheControl>,
    },

    // ── Refusal ───────────────────────────────────────────────────────────────
    /// Model refusal (OpenAI `content_filter` / Anthropic `stop_reason = "refusal"`).
    Refusal {
        refusal: String,
    },

    // ── Fallback ─────────────────────────────────────────────────────────────
    /// A raw JSON block that the codec does not understand.  Preserved for
    /// pass-through and future extension.
    Unknown {
        raw: Value,
    },
}

impl ContentBlock {
    pub fn as_text(&self) -> Option<&str> {
        if let Self::Text { text, .. } = self {
            Some(text)
        } else {
            None
        }
    }

    pub fn is_tool_use(&self) -> bool {
        matches!(self, Self::ToolUse { .. } | Self::ServerToolUse { .. })
    }

    pub fn is_tool_result(&self) -> bool {
        matches!(
            self,
            Self::ToolResult { .. } | Self::ServerToolResult { .. }
        )
    }
}

/// Message content — either a plain string or a typed block list.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(untagged)]
pub enum MessageContent {
    Text(String),
    Blocks(Vec<ContentBlock>),
}

impl MessageContent {
    pub fn to_text(&self) -> String {
        match self {
            Self::Text(t) => t.clone(),
            Self::Blocks(bs) => bs
                .iter()
                .filter_map(|b| b.as_text())
                .collect::<Vec<_>>()
                .join(""),
        }
    }
}

// ── Message ───────────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Message {
    pub role: Role,
    pub content: MessageContent,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub tool_calls: Option<Vec<ToolCall>>,
    /// The `tool_call_id` this result answers.  Required for `Role::Tool` messages.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub tool_call_id: Option<String>,
    /// Provider-specific extras for this individual message (e.g. Anthropic
    /// `cache_control` on `system` array items).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub meta: Option<Value>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ToolCall {
    pub id: String,
    pub name: String,
    pub arguments: String,
}

// ── Tool spec ─────────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ToolSpec {
    pub name: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub description: Option<String>,
    /// JSON Schema for the tool's input parameters.
    pub parameters: Value,
    /// Whether to enforce strict JSON Schema validation (OpenAI + Anthropic).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub strict: Option<bool>,
    /// Per-tool cache breakpoint (Anthropic).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub cache_control: Option<CacheControl>,
    /// Vendor-specific extra fields not covered by the IR.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub meta: Option<Value>,
}

/// `tool_choice` — how the model selects tools.
#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ToolChoice {
    /// Model decides whether to call a tool.
    Auto,
    /// Model must not call any tool.
    None,
    /// Model must call at least one tool.
    Required,
    /// Force a specific tool by name.
    Named { name: String },
    /// Pass-through raw value for protocol-specific options.
    Raw(Value),
}

// ── Generation config ─────────────────────────────────────────────────────────

/// Core generation parameters shared across all supported protocols.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct GenerationConfig {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub temperature: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub max_tokens: Option<u32>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub top_p: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub seed: Option<i64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub stop: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub presence_penalty: Option<f64>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub frequency_penalty: Option<f64>,
}

// ── Reasoning config ──────────────────────────────────────────────────────────

/// Effort level for reasoning / thinking models.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ReasoningEffort {
    None,
    Minimal,
    Low,
    Medium,
    High,
    Xhigh,
    /// Budget in tokens (Anthropic `budget_tokens`).
    Budget(u32),
}

/// Reasoning / extended-thinking configuration.
///
/// Normalized from:
/// - OpenAI `reasoning.effort` + `reasoning.summary`
/// - Anthropic `thinking: { type: "enabled", budget_tokens, display }`
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ReasoningConfig {
    /// Whether extended reasoning / thinking is requested.
    pub enabled: bool,
    /// Token budget for thinking (Anthropic `budget_tokens`).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub budget_tokens: Option<u32>,
    /// Effort level (OpenAI `reasoning.effort`).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub effort: Option<ReasoningEffort>,
    /// Display mode for thinking content (Anthropic `display: "summarized" | "omitted"`).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub display: Option<String>,
}

// ── Response format ───────────────────────────────────────────────────────────

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ResponseFormat {
    Text,
    JsonObject,
    JsonSchema {
        name: String,
        schema: Value,
        #[serde(skip_serializing_if = "Option::is_none")]
        strict: Option<bool>,
    },
}

// ── Stream config ─────────────────────────────────────────────────────────────

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct StreamConfig {
    pub enabled: bool,
    /// Whether the provider should include token usage in the final stream chunk.
    pub include_usage: bool,
}

// ── Safety settings ───────────────────────────────────────────────────────────

/// Google SafetySettings — important enough to have a first-class home in the IR.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SafetySettings {
    pub category: String,
    pub threshold: String,
}

// ── Request metadata ──────────────────────────────────────────────────────────

#[derive(Debug, Clone, Default)]
pub struct RequestMetadata {
    /// The protocol the client spoke.
    pub source_protocol: Option<ProtocolId>,
    /// Raw envelope preserved for pass-through / audit.
    pub raw: Option<RawEnvelope>,
    /// Three-segment vendor extension bag.
    pub vendor: VendorExtensions,
}

// ── AiRequest ─────────────────────────────────────────────────────────────────

/// Unified ingress IR consumed by all codec encoders and the dispatcher.
///
/// Fields are annotated with the FIELD_HOMING.md category that they belong to:
/// `[IR]` = core, `[OAIChat]` = OpenAIChatExt, etc.
#[derive(Debug, Clone)]
pub struct AiRequest {
    // ── Core ──────────────────────────────────────────────────────────────────
    /// [IR] The model identifier as received from the client.
    pub model: String,
    /// [IR] Conversation history.
    pub messages: Vec<Message>,
    /// [IR] System prompt (extracted from a leading `system` message or the
    /// top-level `system` field in Anthropic Messages API).
    pub system: Option<String>,

    // ── Generation ────────────────────────────────────────────────────────────
    /// [IR] Core generation parameters.
    pub generation: GenerationConfig,

    // ── Streaming ─────────────────────────────────────────────────────────────
    /// [IR] Streaming configuration.
    pub stream: StreamConfig,

    // ── Tools ─────────────────────────────────────────────────────────────────
    /// [IR] User-defined tool specifications.
    pub tools: Option<Vec<ToolSpec>>,
    /// [IR] Tool selection mode.
    pub tool_choice: Option<ToolChoice>,
    /// [IR] Whether the provider should call tools in parallel.
    pub parallel_tool_calls: Option<bool>,
    /// [IR] Disable parallel tool use (Anthropic `disable_parallel_tool_use`,
    /// equivalent to `parallel_tool_calls = false` for OpenAI).
    pub disable_parallel_tool_calls: Option<bool>,

    // ── Reasoning ─────────────────────────────────────────────────────────────
    /// [IR] Reasoning / extended-thinking configuration.
    pub reasoning: ReasoningConfig,

    // ── Output format ─────────────────────────────────────────────────────────
    /// [IR] Response format constraint.
    pub response_format: Option<ResponseFormat>,

    // ── Safety ────────────────────────────────────────────────────────────────
    /// [IR] Google SafetySettings (ignored by other encoders).
    pub safety_settings: Option<Vec<SafetySettings>>,

    // ── Protocol extensions ───────────────────────────────────────────────────
    /// Protocol-domain Ext carrying fields specific to the source protocol.
    /// Populated by the ingress decoder (PR-2); consumed by the egress encoder (PR-3).
    pub ext: Option<ProtocolExt>,

    // ── Metadata / vendor bag ─────────────────────────────────────────────────
    pub meta: RequestMetadata,
}

impl AiRequest {
    /// Convenience constructor with minimal required fields.
    pub fn new(model: impl Into<String>, messages: Vec<Message>) -> Self {
        Self {
            model: model.into(),
            messages,
            system: None,
            generation: GenerationConfig::default(),
            stream: StreamConfig::default(),
            tools: None,
            tool_choice: None,
            parallel_tool_calls: None,
            disable_parallel_tool_calls: None,
            reasoning: ReasoningConfig::default(),
            response_format: None,
            safety_settings: None,
            ext: None,
            meta: RequestMetadata::default(),
        }
    }

    /// Return the modalities from `OpenAIChatExt` if present.
    pub fn modalities(&self) -> Option<&Vec<String>> {
        if let Some(ProtocolExt::OpenAiChat(ref e)) = self.ext {
            e.modalities.as_ref()
        } else {
            None
        }
    }
}
