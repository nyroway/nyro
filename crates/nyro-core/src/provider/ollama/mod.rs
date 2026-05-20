//! Ollama vendor. The `pre_request` hook probes `/api/show` and
//! strips tool definitions when the model does not support tools.

mod capabilities;

use async_trait::async_trait;
use reqwest::header::HeaderMap;
use serde_json::Value;

use crate::Gateway;
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
    id: "ollama",
    label: Label {
        zh: "Ollama",
        en: "Ollama",
    },
    icon: "ollama",
    default_protocol: "openai-compatible",
    channels: &[ChannelDef {
        id: "default",
        label: Label {
            zh: "默认",
            en: "Default",
        },
        base_urls: &[
            ProtocolBaseUrl {
                protocol: "openai-compatible",
                base_url: "http://127.0.0.1:11434/v1",
            },
            ProtocolBaseUrl {
                protocol: "anthropic-messages",
                base_url: "http://127.0.0.1:11434",
            },
        ],
        api_key: Some("sk-ollama"),
        models_source: Some("http://127.0.0.1:11434/v1/models"),
        capabilities_source: CapabilitiesSource::Http("http://127.0.0.1:11434/api/show"),
        static_models: &[],
        auth_mode: AuthMode::ApiKey,
        oauth: None,
        runtime: None,
    }],
};

pub struct OllamaVendor;

#[async_trait]
impl Vendor for OllamaVendor {
    fn scope(&self) -> VendorScope {
        VendorScope::Vendor {
            vendor_id: "ollama",
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

    async fn pre_request(
        &self,
        ctx: &VendorCtx<'_>,
        req: &mut AiRequest,
        gw: &Gateway,
    ) -> anyhow::Result<()> {
        if req.tools.is_none() && req.tool_choice.is_none() {
            return Ok(());
        }
        let model = ctx.actual_model;
        let caps = match capabilities::get_ollama_capabilities(gw, ctx.provider, model).await {
            Ok(v) => v,
            Err(e) => {
                tracing::warn!(
                    "failed to fetch capabilities for model {model}, skipping tools check: {e}"
                );
                return Ok(());
            }
        };
        let supports_tools = caps.iter().any(|c| c == "tools");
        if !supports_tools {
            tracing::warn!(
                "tools stripped for model {model} (tools not supported, capabilities: {caps:?})"
            );
            req.tools = None;
            req.tool_choice = None;
            req.meta.vendor.ingress.remove("tools");
            req.meta.vendor.ingress.remove("tool_choice");
        }
        Ok(())
    }

    fn vendor_id(&self) -> &'static str {
        "ollama"
    }
    fn supported_protocols(&self) -> &'static [ProtocolId] {
        use crate::protocol::ids::OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1;
        &[OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1]
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
        openai_map_error("ollama", status, body)
    }
}

inventory::submit! { VendorRegistration { make: || Box::new(OllamaVendor) } }
