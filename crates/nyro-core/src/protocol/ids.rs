//! Three-layer protocol identity: `Protocol` (suite) + `ProtocolEndpoint` (specific API endpoint).
//!
//! Canonical string form: `{protocol}/{name}/{version}`.
//!
//! - `protocol`: closed enum of protocol suites (`openai-compatible` / `openai-responses` / `anthropic-messages` / `google-gemini`).
//! - `name`: wire-format endpoint name (`chat-completions` / `responses` / `messages` / `generate-content` / `embeddings`).
//! - `version`: schema version as the vendor labels it (`v1`, `2023-06-01`, `v1beta`).
//!
//! `ProtocolEndpoint` is `Copy` and stores `&'static str` slices — values must be const.
//! Runtime parsing of arbitrary strings into a `ProtocolEndpoint` is the responsibility of
//! `ProtocolRegistry::resolve_alias`, which returns one of the registered const ids.

use std::fmt;
use std::str::FromStr;

/// Top-level protocol suite (wire-format family).
///
/// A `Protocol` groups one or more `ProtocolEndpoint`s that share the same
/// request/response wire format. It is orthogonal to `Vendor` — multiple vendors
/// (e.g. OpenAI, Moonshot, DeepSeek) may implement the same `Protocol`.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub enum Protocol {
    /// OpenAI Chat Completions-compatible protocol (`/v1/chat/completions`, `/v1/embeddings`).
    OpenAICompatible,
    /// OpenAI Responses API protocol (`/v1/responses`).
    OpenAIResponses,
    /// Anthropic Messages protocol (`/v1/messages`).
    AnthropicMessages,
    /// Google Generative AI (Gemini) protocol.
    GoogleGemini,
}

impl Protocol {
    pub const fn as_str(&self) -> &'static str {
        match self {
            Self::OpenAICompatible => "openai-compatible",
            Self::OpenAIResponses => "openai-responses",
            Self::AnthropicMessages => "anthropic-messages",
            Self::GoogleGemini => "google-gemini",
        }
    }

    pub const fn display_name(&self) -> &'static str {
        match self {
            Self::OpenAICompatible => "OpenAI Compatible",
            Self::OpenAIResponses => "OpenAI Responses",
            Self::AnthropicMessages => "Anthropic Messages",
            Self::GoogleGemini => "Google Gemini",
        }
    }
}

impl fmt::Display for Protocol {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

impl FromStr for Protocol {
    type Err = anyhow::Error;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        match s {
            "openai-compatible" | "openai-compat" | "openai" => Ok(Self::OpenAICompatible),
            "openai-responses" | "openai-resps" | "responses" => Ok(Self::OpenAIResponses),
            "anthropic-messages" | "anthropic-msgs" | "anthropic" | "claude" => {
                Ok(Self::AnthropicMessages)
            }
            "google-gemini" | "google-genai" | "google-generative-ai" | "gemini" | "google" => {
                Ok(Self::GoogleGemini)
            }
            other => anyhow::bail!("unknown protocol: {other}"),
        }
    }
}

/// Specific API endpoint within a `Protocol`.
///
/// Canonical display: `{protocol}/{name}/{version}` (e.g. `openai-compatible/chat-completions/v1`).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub struct ProtocolEndpoint {
    pub protocol: Protocol,
    /// Endpoint name (kebab-case, matches the final path segment of the ingress route).
    pub name: &'static str,
    /// Wire-format version string as the vendor labels it.
    pub version: &'static str,
}

impl ProtocolEndpoint {
    pub const fn new(protocol: Protocol, name: &'static str, version: &'static str) -> Self {
        Self {
            protocol,
            name,
            version,
        }
    }

    /// Look up the registered handler for this endpoint.
    ///
    /// Panics only if no `inventory::submit!` registration exists — a
    /// build-time invariant covered by `tests/protocol_registry.rs`.
    pub fn handler(self) -> &'static std::sync::Arc<dyn super::traits::EndpointHandler> {
        super::registry::ProtocolRegistry::global()
            .get(&self)
            .unwrap_or_else(|| panic!("EndpointHandler for {self} not registered"))
    }
}

impl fmt::Display for ProtocolEndpoint {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}/{}/{}", self.protocol, self.name, self.version)
    }
}

// ── Canonical const `ProtocolEndpoint` values ────────────────────────────────

pub const OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1: ProtocolEndpoint =
    ProtocolEndpoint::new(Protocol::OpenAICompatible, "chat-completions", "v1");

pub const OPENAI_COMPATIBLE_EMBEDDINGS_V1: ProtocolEndpoint =
    ProtocolEndpoint::new(Protocol::OpenAICompatible, "embeddings", "v1");

pub const OPENAI_RESPONSES_V1: ProtocolEndpoint =
    ProtocolEndpoint::new(Protocol::OpenAIResponses, "responses", "v1");

pub const ANTHROPIC_MESSAGES_2023_06_01: ProtocolEndpoint =
    ProtocolEndpoint::new(Protocol::AnthropicMessages, "messages", "2023-06-01");

pub const GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA: ProtocolEndpoint =
    ProtocolEndpoint::new(Protocol::GoogleGemini, "generate-content", "v1beta");

// ── Backward-compat type alias ────────────────────────────────────────────────

/// Backward-compat alias — prefer `ProtocolEndpoint`.
pub type ProtocolId = ProtocolEndpoint;

// ── Static capability types ───────────────────────────────────────────────────

/// Vendor field policy: what happens when the codec encounters a field
/// that the provider may or may not support.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum VendorFieldPolicy {
    /// The provider is known to support this field.
    Supported,
    /// The provider does not support this field; it MUST be dropped silently.
    Drop,
    /// Unknown — check at runtime via vendor extension.
    Unknown,
}

/// Stream capabilities for this endpoint.
#[derive(Debug, Clone, Copy)]
pub struct StreamCaps {
    /// Endpoint can produce SSE streaming responses.
    pub server_sent_events: bool,
    /// The `usage` object is present in the final stream chunk.
    pub usage_in_stream: bool,
    /// Provider requires the body to contain `"stream": true` to stream.
    pub requires_stream_flag: bool,
}

impl StreamCaps {
    pub const DEFAULT: Self = Self {
        server_sent_events: true,
        usage_in_stream: false,
        requires_stream_flag: true,
    };
}

/// Extended static capabilities of an `EndpointHandler`.
///
/// Describes what a specific `ProtocolEndpoint` can do.  The `lossy_default_reject`
/// flag is consumed by `negotiator::negotiate` to decide whether to reject or
/// accept lossy cross-protocol transforms.
#[derive(Debug, Clone, Copy)]
pub struct EndpointCapabilities {
    // ── Original fields (PR-01 through PR-06) ────────────────────────────────
    pub streaming: bool,
    pub tools: bool,
    pub reasoning: bool,
    pub embeddings: bool,
    /// Force the upstream call to be made in streaming mode regardless of the
    /// client's `stream` flag. Currently only true for OpenAI Responses API.
    pub force_upstream_stream: bool,
    /// The encoder writes the actual model name into the request body rather
    /// than the URL path. Currently only true for Google Generate.
    pub override_model_in_body: bool,
    /// Ingress routes this handler claims, as `(method, path)` tuples.
    /// Used by `ProtocolRegistry::find_by_ingress_route` for declarative routing.
    pub ingress_routes: &'static [(&'static str, &'static str)],

    // ── PR-07 additions ───────────────────────────────────────────────────────
    /// Whether multimodal (vision) input is accepted.
    pub multimodal: bool,
    /// Whether the provider accepts structured output / JSON-mode requests.
    pub structured_output: bool,
    /// Whether the provider supports named function tools.
    pub function_calling: bool,
    /// Whether the provider supports parallel tool calls.
    pub parallel_tool_calls: bool,
    /// Whether the provider exposes extended reasoning / thinking.
    pub extended_reasoning: bool,
    /// Whether the provider honours the `seed` parameter for determinism.
    pub deterministic_seed: bool,
    /// Stream capabilities for this endpoint.
    pub stream: StreamCaps,
    /// Default policy for unrecognised vendor fields in the egress body.
    pub unknown_field_policy: VendorFieldPolicy,
    /// When `true`, a request requiring a lossy cross-protocol transform is
    /// rejected with `GatewayError::ProtocolLossyRejected` unless the
    /// route has `allow_lossy = true`.  When `false`, the lossy transform is
    /// accepted silently.
    pub lossy_default_reject: bool,
}

/// Backward-compat alias — prefer `EndpointCapabilities`.
pub type ProtocolCapabilities = EndpointCapabilities;

impl EndpointCapabilities {
    pub const EMPTY: Self = Self {
        streaming: false,
        tools: false,
        reasoning: false,
        embeddings: false,
        force_upstream_stream: false,
        override_model_in_body: false,
        ingress_routes: &[],
        multimodal: false,
        structured_output: false,
        function_calling: false,
        parallel_tool_calls: false,
        extended_reasoning: false,
        deterministic_seed: false,
        stream: StreamCaps::DEFAULT,
        unknown_field_policy: VendorFieldPolicy::Drop,
        lossy_default_reject: true,
    };

    /// The standard set of capabilities for a typical chat-completions endpoint.
    pub const CHAT_STANDARD: Self = Self {
        streaming: true,
        tools: true,
        reasoning: false,
        embeddings: false,
        force_upstream_stream: false,
        override_model_in_body: false,
        ingress_routes: &[],
        multimodal: true,
        structured_output: true,
        function_calling: true,
        parallel_tool_calls: true,
        extended_reasoning: false,
        deterministic_seed: true,
        stream: StreamCaps {
            server_sent_events: true,
            usage_in_stream: true,
            requires_stream_flag: true,
        },
        unknown_field_policy: VendorFieldPolicy::Drop,
        lossy_default_reject: true,
    };
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn display_canonical_form() {
        assert_eq!(
            OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1.to_string(),
            "openai-compatible/chat-completions/v1"
        );
        assert_eq!(
            OPENAI_RESPONSES_V1.to_string(),
            "openai-responses/responses/v1"
        );
        assert_eq!(
            ANTHROPIC_MESSAGES_2023_06_01.to_string(),
            "anthropic-messages/messages/2023-06-01"
        );
        assert_eq!(
            GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA.to_string(),
            "google-gemini/generate-content/v1beta"
        );
        assert_eq!(
            OPENAI_COMPATIBLE_EMBEDDINGS_V1.to_string(),
            "openai-compatible/embeddings/v1"
        );
    }

    #[test]
    fn protocol_round_trip() {
        for p in [
            Protocol::OpenAICompatible,
            Protocol::OpenAIResponses,
            Protocol::AnthropicMessages,
            Protocol::GoogleGemini,
        ] {
            assert_eq!(p.as_str().parse::<Protocol>().unwrap(), p);
        }
    }

    #[test]
    fn protocol_endpoint_is_copy_and_hashable() {
        use std::collections::HashSet;
        let id = OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1;
        let copied = id;
        let mut set = HashSet::new();
        set.insert(id);
        set.insert(copied);
        assert_eq!(set.len(), 1);
    }
}
