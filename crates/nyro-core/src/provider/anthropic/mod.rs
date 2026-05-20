//! Anthropic vendor (direct API).

pub mod claude_code;

use async_trait::async_trait;
use reqwest::header::{HeaderMap, HeaderValue};
use serde_json::Value;

use crate::error::GatewayError;
use crate::protocol::ids::ProtocolId;
use crate::protocol::ir::{AiRequest, AiResponse};
use crate::provider::common::pipeline;
use crate::provider::inbound::InboundResponse;
use crate::provider::metadata::{
    AuthMode, CapabilitiesSource, ChannelDef, Label, OAuthConfig, ProtocolBaseUrl, VendorMetadata,
};
use crate::provider::outbound::OutboundRequest;
use crate::provider::registry::{ExtensionRegistration, VendorRegistration, VendorScope};
use crate::provider::vendor::{ProviderCtx, Vendor};
use crate::provider::vendor_ext::{VendorCtx, VendorExtension};

const METADATA: VendorMetadata = VendorMetadata {
    id: "anthropic",
    label: Label {
        zh: "Anthropic",
        en: "Anthropic",
    },
    icon: "anthropic",
    default_protocol: "anthropic-messages",
    channels: &[
        ChannelDef {
            id: "default",
            label: Label {
                zh: "默认",
                en: "Default",
            },
            base_urls: &[ProtocolBaseUrl {
                protocol: "anthropic-messages",
                base_url: "https://api.anthropic.com",
            }],
            api_key: None,
            models_source: Some("https://api.anthropic.com/v1/models"),
            capabilities_source: CapabilitiesSource::ModelsDev("anthropic"),
            static_models: &[],
            auth_mode: AuthMode::ApiKey,
            oauth: None,
            runtime: None,
        },
        ChannelDef {
            id: "claude-code",
            label: Label {
                zh: "Claude Code",
                en: "Claude Code",
            },
            base_urls: &[ProtocolBaseUrl {
                protocol: "anthropic-messages",
                base_url: "https://api.anthropic.com",
            }],
            api_key: None,
            models_source: Some("ai://models.dev/anthropic"),
            capabilities_source: CapabilitiesSource::ModelsDev("anthropic"),
            static_models: &[
                "claude-opus-4-6",
                "claude-sonnet-4-6",
                "claude-opus-4-5-20251101",
                "claude-sonnet-4-5-20250929",
                "claude-sonnet-4-20250514",
                "claude-opus-4-1-20250805",
                "claude-opus-4-20250514",
                "claude-haiku-4-5-20251001",
                "claude-3-5-haiku-20241022",
            ],
            auth_mode: AuthMode::OAuth,
            oauth: Some(OAuthConfig {
                auth_base_url: "https://claude.ai",
                authorize_url: "https://claude.ai/oauth/authorize",
                token_url: "https://console.anthropic.com/v1/oauth/token",
                client_id: "9d1c250a-e61b-44d9-88ed-5944d1962f5e",
                redirect_uri: "https://platform.claude.com/oauth/code/callback",
                scope: "user:inference user:profile",
            }),
            runtime: None,
        },
    ],
};

pub struct AnthropicVendor;

#[async_trait]
impl Vendor for AnthropicVendor {
    fn scope(&self) -> VendorScope {
        VendorScope::Vendor {
            vendor_id: "anthropic",
        }
    }
    fn metadata(&self) -> Option<&'static VendorMetadata> {
        Some(&METADATA)
    }
    fn auth_headers(&self, ctx: &VendorCtx<'_>) -> HeaderMap {
        let mut h = HeaderMap::new();
        if let Ok(v) = HeaderValue::from_str(ctx.api_key) {
            h.insert("x-api-key", v);
        }
        h.insert("anthropic-version", HeaderValue::from_static("2023-06-01"));
        h
    }
    fn vendor_id(&self) -> &'static str {
        "anthropic"
    }
    fn supported_protocols(&self) -> &'static [ProtocolId] {
        use crate::protocol::ids::ANTHROPIC_MESSAGES_2023_06_01;
        &[ANTHROPIC_MESSAGES_2023_06_01]
    }
    fn declared_request_mutations(&self) -> bool {
        false
    }
    fn declared_response_mutations(&self) -> bool {
        false
    }
    async fn build_request(
        &self,
        req: &mut AiRequest,
        ctx: &ProviderCtx<'_>,
    ) -> Result<OutboundRequest, GatewayError> {
        pipeline::build_request(self, req, ctx).await
    }
    async fn parse_response(
        &self,
        resp: InboundResponse,
        ctx: &ProviderCtx<'_>,
    ) -> Result<AiResponse, GatewayError> {
        pipeline::parse_response(self, resp, ctx).await
    }
    fn map_error(&self, status: u16, body: Value) -> GatewayError {
        let msg = body
            .get("error")
            .and_then(|e| e.get("message"))
            .and_then(|m| m.as_str())
            .map(|s| s.to_string())
            .unwrap_or_else(|| format!("upstream HTTP {status}"));
        GatewayError::upstream_status("anthropic", status, Some(msg))
    }
}

inventory::submit! { VendorRegistration { make: || Box::new(AnthropicVendor) } }

/// Family-level fallback for providers with blank/unknown vendor on Anthropic-family protocols.
pub struct AnthropicFamilyExt;

impl VendorExtension for AnthropicFamilyExt {
    fn scope(&self) -> VendorScope {
        VendorScope::Vendor {
            vendor_id: "anthropic",
        }
    }
    fn metadata(&self) -> Option<&'static VendorMetadata> {
        None
    }
    fn auth_headers(&self, ctx: &VendorCtx<'_>) -> HeaderMap {
        let mut h = HeaderMap::new();
        if let Ok(v) = HeaderValue::from_str(ctx.api_key) {
            h.insert("x-api-key", v);
        }
        h.insert("anthropic-version", HeaderValue::from_static("2023-06-01"));
        h
    }
}

inventory::submit! { ExtensionRegistration { make: || Box::new(AnthropicFamilyExt) } }
