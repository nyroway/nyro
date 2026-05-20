//! Google vendor (Gemini direct API).

use async_trait::async_trait;
use serde_json::Value;

use crate::error::GatewayError;
use crate::protocol::ids::ProtocolId;
use crate::protocol::ir::{AiRequest, AiResponse};
use crate::provider::common::pipeline;
use crate::provider::inbound::InboundResponse;
use crate::provider::metadata::{
    AuthMode, CapabilitiesSource, ChannelDef, Label, ProtocolBaseUrl, VendorMetadata,
};
use crate::provider::outbound::OutboundRequest;
use crate::provider::registry::{ExtensionRegistration, VendorRegistration, VendorScope};
use crate::provider::vendor::{ProviderCtx, Vendor};
use crate::provider::vendor_ext::{VendorCtx, VendorExtension};

const METADATA: VendorMetadata = VendorMetadata {
    id: "google",
    label: Label {
        zh: "Google",
        en: "Google",
    },
    icon: "google",
    default_protocol: "google-gemini",
    channels: &[ChannelDef {
        id: "default",
        label: Label {
            zh: "默认",
            en: "Default",
        },
        base_urls: &[
            ProtocolBaseUrl {
                protocol: "openai-compatible",
                base_url: "https://generativelanguage.googleapis.com/v1beta/openai",
            },
            ProtocolBaseUrl {
                protocol: "google-gemini",
                base_url: "https://generativelanguage.googleapis.com",
            },
        ],
        api_key: None,
        models_source: Some("https://generativelanguage.googleapis.com/v1beta/openai/models"),
        capabilities_source: CapabilitiesSource::ModelsDev("google"),
        static_models: &[],
        auth_mode: AuthMode::ApiKey,
        oauth: None,
        runtime: None,
    }],
};

pub struct GoogleVendor;

#[async_trait]
impl Vendor for GoogleVendor {
    fn scope(&self) -> VendorScope {
        VendorScope::Vendor {
            vendor_id: "google",
        }
    }
    fn metadata(&self) -> Option<&'static VendorMetadata> {
        Some(&METADATA)
    }
    fn build_url(&self, ctx: &VendorCtx<'_>, base_url: &str, path: &str) -> String {
        let url = format!("{}{path}", base_url.trim_end_matches('/'));
        if url.contains('?') {
            format!("{url}&key={}", ctx.api_key)
        } else {
            format!("{url}?key={}", ctx.api_key)
        }
    }
    fn vendor_id(&self) -> &'static str {
        "google"
    }
    fn supported_protocols(&self) -> &'static [ProtocolId] {
        use crate::protocol::ids::GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA;
        &[GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA]
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
        GatewayError::upstream_status("google", status, Some(msg))
    }
}

inventory::submit! { VendorRegistration { make: || Box::new(GoogleVendor) } }

/// Family-level fallback for providers with blank/unknown vendor on Google-family protocols.
pub struct GoogleFamilyExt;

impl VendorExtension for GoogleFamilyExt {
    fn scope(&self) -> VendorScope {
        VendorScope::Vendor {
            vendor_id: "google",
        }
    }
    fn metadata(&self) -> Option<&'static VendorMetadata> {
        None
    }
    fn build_url(&self, ctx: &VendorCtx<'_>, base_url: &str, path: &str) -> String {
        let url = format!("{}{path}", base_url.trim_end_matches('/'));
        if url.contains('?') {
            format!("{url}&key={}", ctx.api_key)
        } else {
            format!("{url}?key={}", ctx.api_key)
        }
    }
}

inventory::submit! { ExtensionRegistration { make: || Box::new(GoogleFamilyExt) } }
