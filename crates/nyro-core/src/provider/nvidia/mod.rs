//! NVIDIA vendor (OpenAI-compatible).

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
    id: "nvidia",
    label: Label {
        zh: "NVIDIA",
        en: "NVIDIA",
    },
    icon: "nvidia",
    default_protocol: "openai-compatible",
    channels: &[ChannelDef {
        id: "default",
        label: Label {
            zh: "默认",
            en: "Default",
        },
        base_urls: &[ProtocolBaseUrl {
            protocol: "openai-compatible",
            base_url: "https://integrate.api.nvidia.com/v1",
        }],
        api_key: None,
        models_source: Some("https://integrate.api.nvidia.com/v1/models"),
        capabilities_source: CapabilitiesSource::ModelsDev("nvidia"),
        static_models: &[],
        auth_mode: AuthMode::ApiKey,
        oauth: None,
        runtime: None,
    }],
};

pub struct NvidiaVendor;

#[async_trait]
impl Vendor for NvidiaVendor {
    fn scope(&self) -> VendorScope {
        VendorScope::Vendor {
            vendor_id: "nvidia",
        }
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
        "nvidia"
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
        openai_map_error("nvidia", status, body)
    }
}

inventory::submit! { VendorRegistration { make: || Box::new(NvidiaVendor) } }
