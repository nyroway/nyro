//! Zhipu AI vendor (OpenAI-compatible).

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
    id: "zhipuai",
    label: Label {
        zh: "Zhipu AI",
        en: "Zhipu AI",
    },
    icon: "zhipu",
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
                    base_url: "https://open.bigmodel.cn/api/paas/v4",
                },
                ProtocolBaseUrl {
                    protocol: "anthropic-messages",
                    base_url: "https://open.bigmodel.cn/api/anthropic",
                },
            ],
            api_key: None,
            models_source: Some("https://open.bigmodel.cn/api/paas/v4/models"),
            capabilities_source: CapabilitiesSource::ModelsDev("zhipuai"),
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
                    base_url: "https://open.bigmodel.cn/api/coding/paas/v4",
                },
                ProtocolBaseUrl {
                    protocol: "anthropic-messages",
                    base_url: "https://open.bigmodel.cn/api/anthropic",
                },
            ],
            api_key: None,
            models_source: Some("https://open.bigmodel.cn/api/coding/paas/v4/models"),
            capabilities_source: CapabilitiesSource::ModelsDev("zhipuai"),
            static_models: &[],
            auth_mode: AuthMode::ApiKey,
            oauth: None,
            runtime: None,
        },
    ],
};

pub struct ZhipuaiVendor;

#[async_trait]
impl Vendor for ZhipuaiVendor {
    fn scope(&self) -> VendorScope {
        VendorScope::Vendor {
            vendor_id: "zhipuai",
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
        "zhipuai"
    }
    fn supported_protocols(&self) -> &'static [ProtocolId] {
        use crate::protocol::ids::{
            ANTHROPIC_MESSAGES_2023_06_01, OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
        };
        &[
            ANTHROPIC_MESSAGES_2023_06_01,
            OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
        ]
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
        openai_map_error("zhipuai", status, body)
    }
}

inventory::submit! { VendorRegistration { make: || Box::new(ZhipuaiVendor) } }
