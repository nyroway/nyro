//! Z.ai vendor (OpenAI-compatible).

use async_trait::async_trait;
use reqwest::header::HeaderMap;
use serde_json::Value;

use crate::error::GatewayError;
use crate::protocol::ids::ProtocolId;
use crate::protocol::ir::{AiRequest, AiResponse};
use crate::provider::common::openai::{
    openai_bearer_auth_headers, openai_build_url, openai_map_error,
};
use crate::provider::common::pipeline;
use crate::provider::inbound::InboundResponse;
use crate::provider::metadata::{
    AuthMode, CapabilitiesSource, ChannelDef, Label, ProtocolBaseUrl, VendorMetadata,
};
use crate::provider::outbound::OutboundRequest;
use crate::provider::registry::{VendorRegistration, VendorScope};
use crate::provider::vendor::{ProviderCtx, Vendor};
use crate::provider::vendor_ext::VendorCtx;

const METADATA: VendorMetadata = VendorMetadata {
    id: "zai",
    label: Label {
        zh: "Z.ai",
        en: "Z.ai",
    },
    icon: "zai",
    default_protocol: "openai-compatible",
    channels: &[
        ChannelDef {
            id: "default",
            label: Label {
                zh: "默认",
                en: "Default",
            },
            base_urls: &[
                ProtocolBaseUrl {
                    protocol: "openai-compatible",
                    base_url: "https://api.z.ai/api/paas/v4",
                },
                ProtocolBaseUrl {
                    protocol: "anthropic-messages",
                    base_url: "https://api.z.ai/api/anthropic",
                },
            ],
            api_key: None,
            models_source: Some("https://api.z.ai/api/paas/v4/models"),
            capabilities_source: CapabilitiesSource::ModelsDev("zai"),
            static_models: &[],
            auth_mode: AuthMode::ApiKey,
            oauth: None,
            runtime: None,
        },
        ChannelDef {
            id: "coding",
            label: Label {
                zh: "Coding Plan",
                en: "Coding Plan",
            },
            base_urls: &[
                ProtocolBaseUrl {
                    protocol: "openai-compatible",
                    base_url: "https://api.z.ai/api/coding/paas/v4",
                },
                ProtocolBaseUrl {
                    protocol: "anthropic-messages",
                    base_url: "https://api.z.ai/api/anthropic",
                },
            ],
            api_key: None,
            models_source: Some("https://api.z.ai/api/coding/paas/v4/models"),
            capabilities_source: CapabilitiesSource::ModelsDev("zai"),
            static_models: &[],
            auth_mode: AuthMode::ApiKey,
            oauth: None,
            runtime: None,
        },
    ],
};

pub struct ZaiVendor;

#[async_trait]
impl Vendor for ZaiVendor {
    fn scope(&self) -> VendorScope {
        VendorScope::Vendor { vendor_id: "zai" }
    }
    fn metadata(&self) -> Option<&'static VendorMetadata> {
        Some(&METADATA)
    }
    fn auth_headers(&self, ctx: &VendorCtx<'_>) -> HeaderMap {
        openai_bearer_auth_headers(ctx)
    }
    fn build_url(&self, _ctx: &VendorCtx<'_>, base_url: &str, path: &str) -> String {
        openai_build_url(base_url, path)
    }
    fn vendor_id(&self) -> &'static str {
        "zai"
    }
    fn supported_protocols(&self) -> &'static [ProtocolId] {
        use crate::protocol::ids::OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1;
        &[OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1]
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
        openai_map_error("zai", status, body)
    }
}

inventory::submit! { VendorRegistration { make: || Box::new(ZaiVendor) } }
