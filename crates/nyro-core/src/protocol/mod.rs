//! Protocol layer.
//!
//! # Three-layer identity
//!
//! Canonical form: `{protocol}/{name}/{version}`.
//!
//! - `protocol`: closed `Protocol` enum (`openai-compatible` / `openai-responses` / `anthropic-messages` / `google-gemini`).
//! - `name`: wire-format endpoint name (`chat-completions`, `responses`, `messages`, `generate-content`).
//! - `version`: schema version as the vendor labels it (`v1`, `2023-06-01`, `v1beta`).
//!
//! See [`ids`], [`traits`], [`registry`], and [`codec`] for the model.
//!
//! ## Codec layout
//!
//! Each `codec/<protocol>/` directory co-locates the wire codecs **and** the
//! thin `EndpointHandler` registration shell for every endpoint:
//!
//! - `codec/openai_compatible/chat_completions.rs` — `OpenAIChatCompletionsV1`
//! - `codec/openai_compatible/embeddings.rs` — `OpenAIEmbeddingsV1`
//! - `codec/openai_responses/responses.rs` — `OpenAIResponsesV1`
//! - `codec/anthropic_messages/messages.rs` — `AnthropicMessages2023`
//! - `codec/google_generative/generate_content.rs` — `GoogleGenerateContentV1Beta`
//!
//! Shared semantic utilities live in `codec/reasoning.rs` and
//! `codec/tool_correlation.rs`.
//!
//! ## Alias table
//!
//! See [`registry::ProtocolRegistry`] for three-tier resolution of endpoint aliases
//! and [`registry::ProtocolRegistry::parse_protocol`] for Protocol-level resolution.

pub mod codec;

pub mod ids;
pub mod ir;
pub mod registry;
pub mod traits;

use reqwest::header::HeaderMap;

use crate::db::models::Provider;
use crate::protocol::ids::{OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1, ProtocolEndpoint};

// ── Client → Gateway ──

pub trait RequestDecoder {
    fn decode_request(&self, body: serde_json::Value) -> anyhow::Result<ir::AiRequest>;
}

// ── Gateway → Provider ──

pub trait RequestEncoder {
    fn encode_request(&self, req: &ir::AiRequest)
    -> anyhow::Result<(serde_json::Value, HeaderMap)>;

    fn egress_path(&self, model: &str, stream: bool) -> String;
}

// ── Provider response → internal ──

pub trait ResponseDecoder: Send {
    fn parse_response(&self, resp: serde_json::Value) -> anyhow::Result<ir::AiResponse>;
}

// ── Internal → client response ──

pub trait ResponseEncoder: Send {
    fn format_response(&self, resp: &ir::AiResponse) -> serde_json::Value;
}

// ── Streaming: provider → internal deltas ──

pub trait StreamResponseDecoder: Send {
    fn parse_chunk(&mut self, raw: &str) -> anyhow::Result<Vec<ir::AiStreamDelta>>;
    fn finish(&mut self) -> anyhow::Result<Vec<ir::AiStreamDelta>>;
}

// ── Streaming: internal deltas → client SSE ──

pub trait StreamResponseEncoder: Send {
    fn format_deltas(&mut self, deltas: &[ir::AiStreamDelta]) -> Vec<SseEvent>;
    fn format_done(&mut self) -> Vec<SseEvent>;
    fn usage(&self) -> ir::Usage;
}

// ── SSE helper ──

#[derive(Debug, Clone)]
pub struct SseEvent {
    pub event: Option<String>,
    pub data: String,
}

impl SseEvent {
    pub fn new(event: Option<&str>, data: impl Into<String>) -> Self {
        Self {
            event: event.map(|e| e.to_string()),
            data: data.into(),
        }
    }

    pub fn to_sse_string(&self) -> String {
        let mut s = String::new();
        if let Some(ref event) = self.event {
            s.push_str(&format!("event: {event}\n"));
        }
        s.push_str(&format!("data: {}\n\n", self.data));
        s
    }
}

// ── Provider protocol negotiation ──

/// Declared protocol capabilities of a single provider, built from the DB row.
#[derive(Debug, Clone)]
pub struct ProviderProtocols {
    pub default: ProtocolEndpoint,
    pub base_url: String,
}

#[derive(Debug, Clone)]
pub struct ResolvedEgress {
    pub protocol: ProtocolEndpoint,
    pub base_url: String,
    pub needs_conversion: bool,
}

impl ProviderProtocols {
    /// Best-effort string → [`ProtocolEndpoint`] resolver.
    pub fn parse_protocol_key(s: &str) -> Option<ProtocolEndpoint> {
        let reg = registry::ProtocolRegistry::global();
        reg.resolve_alias(s).or_else(|| {
            let protocol = reg.parse_protocol(s)?;
            reg.list_by_protocol(protocol)
                .first()
                .map(|handler| handler.id())
        })
    }

    /// Build from a provider DB row.
    pub fn from_provider(provider: &Provider) -> Self {
        let default = Self::parse_protocol_key(provider.protocol.trim())
            .unwrap_or(OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1);

        Self {
            default,
            base_url: provider.base_url.trim().to_string(),
        }
    }

    /// Returns `true` if the provider declares support for `protocol`.
    pub fn supports(&self, protocol: ProtocolEndpoint) -> bool {
        self.default.protocol == protocol.protocol
    }

    /// Deterministic two-tier egress resolution:
    ///
    /// 1. **Same protocol suite** — use the ingress endpoint and provider base URL.
    /// 2. **Provider default** — last resort with conversion.
    pub fn resolve_egress(&self, ingress: ProtocolEndpoint) -> ResolvedEgress {
        if self.supports(ingress) {
            return ResolvedEgress {
                protocol: ingress,
                base_url: self.base_url.clone(),
                needs_conversion: false,
            };
        }

        ResolvedEgress {
            protocol: self.default,
            base_url: self.base_url.clone(),
            needs_conversion: true,
        }
    }
}
