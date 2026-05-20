//! Google Vertex AI provider.
//!
//! Vertex uses Google service-account OAuth rather than Gemini API keys.  The
//! provider stores the service-account JSON in the existing provider `api_key`
//! field, exchanges it for a short-lived Google access token, and sends
//! `Authorization: Bearer <token>` to Vertex AI.

use std::sync::{Arc, OnceLock};

use anyhow::{Context, anyhow};
use async_trait::async_trait;
use base64::Engine;
use gcp_auth::{CustomServiceAccount, TokenProvider};
use reqwest::header::{AUTHORIZATION, HeaderMap, HeaderValue};
use serde_json::Value;
use sha2::{Digest, Sha256};

use crate::error::GatewayError;
use crate::protocol::ids::{
    GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA, OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
    OPENAI_COMPATIBLE_EMBEDDINGS_V1, Protocol, ProtocolId,
};
use crate::protocol::ir::{AiRequest, AiResponse};
use crate::provider::common::{openai::openai_map_error, pipeline};
use crate::provider::inbound::InboundResponse;
use crate::provider::metadata::{
    AuthMode, CapabilitiesSource, ChannelDef, Label, ProtocolBaseUrl, VendorMetadata,
};
use crate::provider::outbound::OutboundRequest;
use crate::provider::registry::{VendorRegistration, VendorScope};
use crate::provider::vendor::{ProviderCtx, Vendor};
use crate::provider::vendor_ext::VendorCtx;

const GOOGLE_CLOUD_PLATFORM_SCOPE: &str = "https://www.googleapis.com/auth/cloud-platform";
const PROJECT_PLACEHOLDERS: &[&str] = &["{project}", "{project_id}", "${PROJECT_ID}"];

const METADATA: VendorMetadata = VendorMetadata {
    id: "vertexai",
    label: Label {
        zh: "Vertex AI",
        en: "Vertex AI",
    },
    icon: "googlecloud",
    default_protocol: "google-gemini",
    channels: &[
        ChannelDef {
            id: "native",
            label: Label {
                zh: "原生 Gemini",
                en: "Native Gemini",
            },
            base_urls: &[ProtocolBaseUrl {
                protocol: "google-gemini",
                base_url: "https://aiplatform.googleapis.com/v1/projects/{project}/locations/global",
            }],
            api_key: None,
            models_source: None,
            capabilities_source: CapabilitiesSource::ModelsDev("google"),
            static_models: &[
                "gemini-2.5-pro",
                "gemini-2.5-flash",
                "gemini-2.0-flash-001",
                "gemini-1.5-pro-002",
                "gemini-1.5-flash-002",
            ],
            auth_mode: AuthMode::ApiKey,
            oauth: None,
            runtime: None,
        },
        ChannelDef {
            id: "openai",
            label: Label {
                zh: "OpenAI 兼容",
                en: "OpenAI Compatible",
            },
            base_urls: &[ProtocolBaseUrl {
                protocol: "openai-compatible",
                base_url: "https://aiplatform.googleapis.com/v1/projects/{project}/locations/global/endpoints/openapi",
            }],
            api_key: None,
            models_source: None,
            capabilities_source: CapabilitiesSource::ModelsDev("google"),
            static_models: &[
                "google/gemini-2.5-pro",
                "google/gemini-2.5-flash",
                "google/gemini-2.0-flash-001",
            ],
            auth_mode: AuthMode::ApiKey,
            oauth: None,
            runtime: None,
        },
    ],
};

pub struct VertexVendor;

#[async_trait]
impl Vendor for VertexVendor {
    fn scope(&self) -> VendorScope {
        VendorScope::Vendor {
            vendor_id: "vertexai",
        }
    }

    fn metadata(&self) -> Option<&'static VendorMetadata> {
        Some(&METADATA)
    }

    fn build_url(&self, ctx: &VendorCtx<'_>, base_url: &str, path: &str) -> String {
        vertex_build_url(ctx, base_url, path)
    }

    fn auth_headers(&self, ctx: &VendorCtx<'_>) -> HeaderMap {
        let mut headers = HeaderMap::new();
        let token = ctx.api_key.trim();
        if token.is_empty() || looks_like_service_account_json(token) {
            return headers;
        }
        if let Ok(value) = HeaderValue::from_str(&format!("Bearer {token}")) {
            headers.insert(AUTHORIZATION, value);
        }
        headers
    }

    fn vendor_id(&self) -> &'static str {
        "vertexai"
    }

    fn supported_protocols(&self) -> &'static [ProtocolId] {
        &[
            GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA,
            OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
            OPENAI_COMPATIBLE_EMBEDDINGS_V1,
        ]
    }

    async fn build_request(
        &self,
        req: &mut AiRequest,
        ctx: &ProviderCtx<'_>,
    ) -> Result<OutboundRequest, GatewayError> {
        let mut outbound = pipeline::build_request(self, req, ctx).await?;
        let token = vertex_access_token(ctx.api_key).await.map_err(|source| {
            GatewayError::provider_unavailable(
                "vertexai",
                format!("failed to fetch Vertex access token: {source}"),
            )
        })?;
        let value = HeaderValue::from_str(&format!("Bearer {token}")).map_err(|source| {
            GatewayError::Internal {
                source: anyhow!(source).context("build Vertex authorization header"),
            }
        })?;
        outbound.headers.insert(AUTHORIZATION, value);
        Ok(outbound)
    }

    async fn parse_response(
        &self,
        resp: InboundResponse,
        ctx: &ProviderCtx<'_>,
    ) -> Result<AiResponse, GatewayError> {
        pipeline::parse_response(self, resp, ctx).await
    }

    fn map_error(&self, status: u16, body: Value) -> GatewayError {
        match body
            .get("error")
            .and_then(|e| e.get("message"))
            .and_then(Value::as_str)
        {
            Some(message) => {
                GatewayError::upstream_status("vertexai", status, Some(message.to_string()))
            }
            None => openai_map_error("vertexai", status, body),
        }
    }
}

inventory::submit! { VendorRegistration { make: || Box::new(VertexVendor) } }

pub fn is_vertex_vendor(provider: &crate::db::models::Provider) -> bool {
    [provider.vendor.as_deref(), provider.preset_key.as_deref()]
        .into_iter()
        .flatten()
        .map(str::trim)
        .any(|value| value.eq_ignore_ascii_case("vertexai"))
}

pub async fn vertex_access_token(secret_or_token: &str) -> anyhow::Result<String> {
    let trimmed = secret_or_token.trim();
    if trimmed.is_empty() {
        anyhow::bail!("Vertex service account JSON is empty");
    }
    if !looks_like_service_account_json(trimmed) {
        return Ok(trimmed.to_string());
    }

    let service_account = cached_service_account(trimmed)?;
    let scopes = [GOOGLE_CLOUD_PLATFORM_SCOPE];
    let token = service_account
        .token(&scopes)
        .await
        .context("fetch google access token with service account")?;
    Ok(token.as_str().to_string())
}

pub fn expand_vertex_base_url(base_url: &str, service_account_json: &str) -> String {
    let mut out = base_url.trim_end_matches('/').to_string();
    if let Some(project_id) = project_id_from_service_account_json(service_account_json) {
        for placeholder in PROJECT_PLACEHOLDERS {
            out = out.replace(placeholder, &project_id);
        }
    }
    out
}

fn vertex_build_url(ctx: &VendorCtx<'_>, base_url: &str, path: &str) -> String {
    let base = expand_vertex_base_url(base_url, ctx.api_key);
    match ctx.protocol_id.protocol {
        Protocol::GoogleGemini => vertex_google_generate_url(&base, path),
        Protocol::OpenAICompatible => vertex_openai_url(&base, path),
        _ => format!("{}{}", base.trim_end_matches('/'), path),
    }
}

fn vertex_google_generate_url(base_url: &str, path: &str) -> String {
    let (path_no_query, query) = path
        .split_once('?')
        .map_or((path, None), |(p, q)| (p, Some(q)));
    let Some(model_and_action) = path_no_query.split_once("/models/").map(|(_, rest)| rest) else {
        return format!("{}{}", base_url.trim_end_matches('/'), path);
    };
    let mut url = format!(
        "{}/publishers/google/models/{}",
        base_url.trim_end_matches('/'),
        model_and_action
    );
    if let Some(query) = query.filter(|q| !q.is_empty()) {
        url.push('?');
        url.push_str(query);
    }
    url
}

fn vertex_openai_url(base_url: &str, path: &str) -> String {
    let adjusted = path.strip_prefix("/v1/").map_or(path, |rest| {
        // Preserve the leading slash expected by the final formatter.
        rest.strip_prefix('/').unwrap_or(rest)
    });
    let adjusted = if adjusted.starts_with('/') {
        adjusted.to_string()
    } else {
        format!("/{adjusted}")
    };
    format!("{}{}", base_url.trim_end_matches('/'), adjusted)
}

fn looks_like_service_account_json(value: &str) -> bool {
    value.starts_with('{')
        && serde_json::from_str::<Value>(value)
            .ok()
            .is_some_and(|json| {
                json.get("client_email").and_then(Value::as_str).is_some()
                    && json.get("private_key").and_then(Value::as_str).is_some()
            })
}

fn project_id_from_service_account_json(value: &str) -> Option<String> {
    let project_id = serde_json::from_str::<Value>(value)
        .ok()?
        .get("project_id")?
        .as_str()?
        .trim()
        .to_string();
    (!project_id.is_empty()).then_some(project_id)
}

fn cached_service_account(service_account_json: &str) -> anyhow::Result<Arc<CustomServiceAccount>> {
    static CACHE: OnceLock<dashmap::DashMap<String, Arc<CustomServiceAccount>>> = OnceLock::new();
    let key = secret_hash(service_account_json);
    let cache = CACHE.get_or_init(dashmap::DashMap::new);
    if let Some(existing) = cache.get(&key) {
        return Ok(existing.clone());
    }
    let parsed = CustomServiceAccount::from_json(service_account_json)
        .context("parse google service account json")?;
    let parsed = Arc::new(parsed);
    let entry = cache.entry(key).or_insert_with(|| parsed.clone());
    Ok(entry.clone())
}

fn secret_hash(value: &str) -> String {
    let digest = Sha256::digest(value.as_bytes());
    base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(digest)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn extracts_project_id_from_service_account_json() {
        let json = r#"{"project_id":"demo-project","client_email":"svc@example.com","private_key":"-----BEGIN PRIVATE KEY-----\n..."}"#;
        assert_eq!(
            project_id_from_service_account_json(json).as_deref(),
            Some("demo-project")
        );
    }

    #[test]
    fn leaves_plain_access_token_as_runtime_token() {
        let rt = tokio::runtime::Runtime::new().unwrap();
        let token = rt.block_on(vertex_access_token("ya29.test-token")).unwrap();
        assert_eq!(token, "ya29.test-token");
    }

    #[test]
    fn emits_bearer_header_for_resolved_access_token() {
        let provider = crate::db::models::Provider {
            id: "test".into(),
            name: "test".into(),
            vendor: Some("vertexai".into()),
            protocol: "openai-compatible".into(),
            base_url: "https://aiplatform.googleapis.com/v1/projects/demo/locations/global/endpoints/openapi".into(),
            preset_key: None,
            channel: None,
            models_source: None,
            static_models: None,
            api_key: "unused".into(),
            auth_mode: "apikey".into(),
            use_proxy: false,
            last_test_success: None,
            last_test_at: None,
            is_enabled: true,
            created_at: String::new(),
            updated_at: String::new(),
        };
        let ctx = VendorCtx {
            provider: &provider,
            protocol_id: OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
            api_key: "ya29.test-token",
            actual_model: "google/gemini-2.5-flash",
            credential: None,
        };

        let headers = VertexVendor.auth_headers(&ctx);

        assert_eq!(
            headers.get(AUTHORIZATION).and_then(|v| v.to_str().ok()),
            Some("Bearer ya29.test-token")
        );
    }
}
