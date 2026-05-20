//! OpenAI vendor — direct API plus the Codex channel (OAuth via ChatGPT).

pub mod codex;

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
    AuthMode, CapabilitiesSource, ChannelDef, Label, OAuthConfig, ProtocolBaseUrl, RuntimeConfig,
    VendorMetadata,
};
use crate::provider::outbound::OutboundRequest;
use crate::provider::registry::{ExtensionRegistration, VendorRegistration, VendorScope};
use crate::provider::vendor::{ProviderCtx, Vendor};
use crate::provider::vendor_ext::{VendorCtx, VendorExtension};

const METADATA: VendorMetadata = VendorMetadata {
    id: "openai",
    label: Label {
        zh: "OpenAI",
        en: "OpenAI",
    },
    icon: "openai",
    default_protocol: "openai-compatible",
    channels: &[
        ChannelDef {
            id: "default",
            label: Label {
                zh: "默认",
                en: "Default",
            },
            base_urls: &[ProtocolBaseUrl {
                protocol: "openai-compatible",
                base_url: "https://api.openai.com/v1",
            }],
            api_key: None,
            models_source: Some("https://api.openai.com/v1/models"),
            capabilities_source: CapabilitiesSource::ModelsDev("openai"),
            static_models: &[],
            auth_mode: AuthMode::ApiKey,
            oauth: None,
            runtime: None,
        },
        ChannelDef {
            id: "codex",
            label: Label {
                zh: "Codex",
                en: "Codex",
            },
            base_urls: &[ProtocolBaseUrl {
                protocol: "openai-responses",
                base_url: "https://chatgpt.com/backend-api/codex",
            }],
            api_key: None,
            models_source: Some("https://chatgpt.com/backend-api/codex/models"),
            capabilities_source: CapabilitiesSource::ModelsDev("openai"),
            static_models: &[],
            auth_mode: AuthMode::OAuth,
            oauth: Some(OAuthConfig {
                auth_base_url: "https://auth.openai.com",
                authorize_url: "https://auth.openai.com/oauth/authorize",
                token_url: "https://auth.openai.com/oauth/token",
                client_id: "app_EMoamEEZ73f0CkXaXp7hrann",
                redirect_uri: "http://localhost:1455/auth/callback",
                scope: "openid profile email offline_access",
            }),
            runtime: Some(RuntimeConfig {
                api_base_url: "https://chatgpt.com/backend-api/codex",
                models_url: "https://chatgpt.com/backend-api/codex/models",
                models_client_version: "0.99.0",
            }),
        },
    ],
};

pub struct OpenAiVendor;

#[async_trait]
impl Vendor for OpenAiVendor {
    fn scope(&self) -> VendorScope {
        VendorScope::Vendor {
            vendor_id: "openai",
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
        "openai"
    }
    fn supported_protocols(&self) -> &'static [ProtocolId] {
        use crate::protocol::ids::{
            OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1, OPENAI_COMPATIBLE_EMBEDDINGS_V1,
            OPENAI_RESPONSES_V1,
        };
        &[
            OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
            OPENAI_RESPONSES_V1,
            OPENAI_COMPATIBLE_EMBEDDINGS_V1,
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
        openai_map_error("openai", status, body)
    }
}

inventory::submit! { VendorRegistration { make: || Box::new(OpenAiVendor) } }

/// Family-level fallback for any provider whose `vendor` field is blank or unknown
/// but whose egress protocol belongs to the OpenAI family.
pub struct OpenAIFamilyExt;

impl VendorExtension for OpenAIFamilyExt {
    fn scope(&self) -> VendorScope {
        VendorScope::Vendor {
            vendor_id: "openai",
        }
    }
    fn metadata(&self) -> Option<&'static VendorMetadata> {
        None
    }
    fn auth_headers(&self, ctx: &VendorCtx<'_>) -> HeaderMap {
        openai_bearer_auth_headers(ctx)
    }
    fn build_url(&self, _ctx: &VendorCtx<'_>, base_url: &str, path: &str) -> String {
        openai_build_url(base_url, path)
    }
}

inventory::submit! { ExtensionRegistration { make: || Box::new(OpenAIFamilyExt) } }
