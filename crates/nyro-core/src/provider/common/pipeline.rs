//! Standard 7-step request/response pipeline shared by every
//! OpenAI-compatible vendor.
//!
//! # Usage
//!
//! Delegate `build_request` and `parse_response` to the free functions here:
//!
//! ```rust,ignore
//! use crate::provider::common::pipeline;
//!
//! async fn build_request(&self, req, ctx) -> Result<OutboundRequest> {
//!     pipeline::build_request(self, req, ctx).await
//! }
//! async fn parse_response(&self, resp, ctx) -> Result<AiResponse> {
//!     pipeline::parse_response(self, resp, ctx).await
//! }
//! ```

use reqwest::header::HeaderMap;

use crate::error::GatewayError;
use crate::provider::vendor::Vendor;

/// Standard `build_request` pipeline:
/// `pre_request → normalize_tool_results → pre_encode → codec_encode →
///  post_encode → auth_headers → build_url`.
pub async fn build_request<V>(
    vendor: &V,
    req: &mut crate::protocol::ir::AiRequest,
    ctx: &crate::provider::vendor::ProviderCtx<'_>,
) -> Result<crate::provider::outbound::OutboundRequest, GatewayError>
where
    V: crate::provider::vendor::Vendor,
{
    req.model = ctx.actual_model.to_string();

    let vendor_ctx = ctx.to_vendor_ctx();

    // 1. pre_request hook
    vendor
        .pre_request(&vendor_ctx, req, ctx.gw)
        .await
        .map_err(GatewayError::internal)?;

    // 2. normalize tool results
    crate::protocol::codec::tool_correlation::normalize_request_tool_results(req);

    // 3. pre_encode hook
    vendor
        .pre_encode(&vendor_ctx, req)
        .await
        .map_err(GatewayError::internal)?;

    // 4. codec encode
    let egress_handler = ctx.protocol.handler();
    let encoder = egress_handler.make_request_encoder();
    let (mut body, mut extra_headers) = encoder
        .encode_request(req)
        .map_err(GatewayError::internal)?;

    // 5. post_encode hook
    vendor
        .post_encode(&vendor_ctx, &mut body, &mut extra_headers)
        .await
        .map_err(GatewayError::internal)?;

    // 6. auth headers
    //
    // OAuth drivers (codex, claude-code) stash their Bearer + provider-
    // specific headers in `RuntimeBinding.extra_headers` and ask the
    // dispatcher to skip the vendor's default `auth_headers` via
    // `ctx.disable_default_auth`. Skipping unconditionally would break
    // every API-key path; gating here keeps the OAuth invariant
    // ("no leaked empty x-api-key") in a single seam shared by every
    // openai-compatible adapter.
    let mut headers = if ctx.disable_default_auth {
        HeaderMap::new()
    } else {
        vendor.auth_headers(&vendor_ctx)
    };
    // Anthropic-protocol upstreams require `x-api-key` instead of
    // `Authorization: Bearer`. Most OpenAI-compatible vendors blindly emit
    // Bearer; rewrite here so any vendor with a declared anthropic endpoint
    // works out of the box.
    //
    // Skipped under `disable_default_auth`: when an OAuth driver owns auth
    // (claude-code uses `Bearer <oauth_token>` + `anthropic-beta=
    // oauth-2025-04-20`), `ctx.api_key` is the OAuth Bearer token, NOT a
    // real Anthropic API key. Rewriting it here would forward the Bearer
    // as a fake `x-api-key` and break the OAuth handshake.
    if !ctx.disable_default_auth
        && ctx.protocol.protocol == crate::protocol::ids::Protocol::AnthropicMessages
        && !headers.contains_key("x-api-key")
    {
        headers.remove(reqwest::header::AUTHORIZATION);
        if let Ok(v) = reqwest::header::HeaderValue::from_str(ctx.api_key) {
            headers.insert("x-api-key", v);
        }
    }
    headers.extend(extra_headers);

    // 7. build URL
    let egress_path = encoder.egress_path(ctx.actual_model, req.stream.enabled);
    let url = vendor.build_url(&vendor_ctx, ctx.egress_base_url, &egress_path);

    Ok(crate::provider::outbound::OutboundRequest { url, headers, body })
}

/// Standard `parse_response` pipeline:
/// `pre_parse → codec_parse → reasoning_normalization → post_parse`.
pub async fn parse_response<V>(
    vendor: &V,
    resp: crate::provider::inbound::InboundResponse,
    ctx: &crate::provider::vendor::ProviderCtx<'_>,
) -> Result<crate::protocol::ir::AiResponse, GatewayError>
where
    V: crate::provider::vendor::Vendor,
{
    let vendor_ctx = ctx.to_vendor_ctx();
    let mut body = resp.body;

    // 1. pre_parse hook
    vendor
        .pre_parse(&vendor_ctx, &mut body)
        .await
        .map_err(GatewayError::internal)?;

    // 2. codec parse
    let egress_handler = ctx.protocol.handler();
    let parser = egress_handler.make_response_decoder();
    let mut ai_resp = parser
        .parse_response(body)
        .map_err(GatewayError::internal)?;

    // 3. reasoning normalization
    crate::protocol::codec::reasoning::normalize_response_reasoning(&mut ai_resp);

    // 4. post_parse hook
    vendor
        .post_parse(&vendor_ctx, &mut ai_resp)
        .await
        .map_err(GatewayError::internal)?;

    Ok(ai_resp)
}

/// PassThrough request builder: skips the IR codec entirely.
///
/// Used when [`crate::proxy::planner::ProtocolMode::Native`] is in effect
/// (ingress == egress) and the vendor declares no request mutations via
/// [`Vendor::declared_request_mutations`]. Only auth headers and the egress
/// URL are computed; the client body is forwarded verbatim.
pub async fn passthrough_run(
    vendor: &dyn Vendor,
    mut raw_body: serde_json::Value,
    ctx: &crate::provider::vendor::ProviderCtx<'_>,
) -> Result<crate::provider::outbound::OutboundRequest, GatewayError> {
    // Replace the model field with the route-configured actual model so the
    // upstream receives the real model name, not the client's virtual alias.
    if let Some(obj) = raw_body.as_object_mut() {
        obj.insert(
            "model".to_string(),
            serde_json::Value::String(ctx.actual_model.to_string()),
        );
    }

    let vendor_ctx = ctx.to_vendor_ctx();

    let mut headers = if ctx.disable_default_auth {
        HeaderMap::new()
    } else {
        vendor.auth_headers(&vendor_ctx)
    };

    // Anthropic-family egress: rewrite Bearer → x-api-key (mirrors build_request).
    if !ctx.disable_default_auth
        && ctx.protocol.protocol == crate::protocol::ids::Protocol::AnthropicMessages
        && !headers.contains_key("x-api-key")
    {
        headers.remove(reqwest::header::AUTHORIZATION);
        if let Ok(v) = reqwest::header::HeaderValue::from_str(ctx.api_key) {
            headers.insert("x-api-key", v);
        }
    }

    let is_stream = raw_body
        .get("stream")
        .and_then(|v| v.as_bool())
        .unwrap_or(false);
    let egress_path = ctx
        .protocol
        .handler()
        .make_request_encoder()
        .egress_path(ctx.actual_model, is_stream);
    let url = vendor.build_url(&vendor_ctx, ctx.egress_base_url, &egress_path);

    Ok(crate::provider::outbound::OutboundRequest {
        url,
        headers,
        body: raw_body,
    })
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    //! Tests cover the `disable_default_auth` gate inside `build_request`.
    //! When `ProviderCtx.disable_default_auth` is set, the vendor's default
    //! `auth_headers` AND the Anthropic-egress `Authorization → x-api-key`
    //! rewrite MUST be suppressed. Both directions are pinned so a future
    //! refactor that flips a gate fails loudly.
    use super::*;
    use crate::Gateway;
    use crate::GatewayConfig;
    use crate::db::models::Provider;
    use crate::error::GatewayError;
    use crate::protocol::ids::{
        ANTHROPIC_MESSAGES_2023_06_01, OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1, ProtocolId,
    };
    use crate::protocol::ir::{AiRequest, AiResponse};
    use crate::provider::inbound::InboundResponse;
    use crate::provider::outbound::OutboundRequest;
    use crate::provider::registry::VendorScope;
    use crate::provider::vendor::{ProviderCtx, Vendor};
    use crate::provider::vendor_ext::VendorCtx;
    use async_trait::async_trait;
    use reqwest::header::HeaderMap as ExtHeaderMap;
    use serde_json::Value;
    use std::path::PathBuf;
    use uuid::Uuid;

    /// Stand-in vendor: injects `x-api-key: <ctx.api_key>`, mirroring
    /// how `AnthropicVendor::auth_headers` behaves.
    struct FakeApiKeyVendor;

    #[async_trait]
    impl Vendor for FakeApiKeyVendor {
        fn scope(&self) -> VendorScope {
            VendorScope::Vendor {
                vendor_id: "fake-test",
            }
        }
        fn auth_headers(&self, ctx: &VendorCtx<'_>) -> ExtHeaderMap {
            let mut h = ExtHeaderMap::new();
            if !ctx.api_key.is_empty() {
                h.insert(
                    "x-api-key",
                    reqwest::header::HeaderValue::from_str(ctx.api_key).unwrap(),
                );
            }
            h
        }
        fn vendor_id(&self) -> &'static str {
            "fake-test"
        }
        fn supported_protocols(&self) -> &'static [ProtocolId] {
            &[OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1]
        }
        async fn build_request(
            &self,
            _req: &mut AiRequest,
            _ctx: &ProviderCtx<'_>,
        ) -> Result<OutboundRequest, GatewayError> {
            unreachable!()
        }
        async fn parse_response(
            &self,
            _resp: InboundResponse,
            _ctx: &ProviderCtx<'_>,
        ) -> Result<AiResponse, GatewayError> {
            unreachable!()
        }
        fn map_error(&self, status: u16, _body: Value) -> GatewayError {
            GatewayError::upstream_status("fake-test", status, None)
        }
    }

    /// Emits `Authorization: Bearer <ctx.api_key>`, mirroring OpenAI-compat
    /// vendors. PR #105's rewrite turns this into `x-api-key` on Anthropic egress.
    struct FakeBearerVendor;

    #[async_trait]
    impl Vendor for FakeBearerVendor {
        fn scope(&self) -> VendorScope {
            VendorScope::Vendor {
                vendor_id: "fake-bearer",
            }
        }
        fn auth_headers(&self, ctx: &VendorCtx<'_>) -> ExtHeaderMap {
            let mut h = ExtHeaderMap::new();
            if !ctx.api_key.is_empty() {
                h.insert(
                    reqwest::header::AUTHORIZATION,
                    reqwest::header::HeaderValue::from_str(&format!("Bearer {}", ctx.api_key))
                        .unwrap(),
                );
            }
            h
        }
        fn vendor_id(&self) -> &'static str {
            "fake-bearer"
        }
        fn supported_protocols(&self) -> &'static [ProtocolId] {
            &[OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1]
        }
        async fn build_request(
            &self,
            _req: &mut AiRequest,
            _ctx: &ProviderCtx<'_>,
        ) -> Result<OutboundRequest, GatewayError> {
            unreachable!()
        }
        async fn parse_response(
            &self,
            _resp: InboundResponse,
            _ctx: &ProviderCtx<'_>,
        ) -> Result<AiResponse, GatewayError> {
            unreachable!()
        }
        fn map_error(&self, status: u16, _body: Value) -> GatewayError {
            GatewayError::upstream_status("fake-bearer", status, None)
        }
    }

    fn provider_with_api_key(api_key: &str) -> Provider {
        Provider {
            id: "p".into(),
            name: "p".into(),
            vendor: Some("fake-test".into()),
            protocol: "openai".into(),
            base_url: "https://upstream.local".into(),
            preset_key: Some("fake-test".into()),
            channel: Some("default".into()),
            models_source: None,
            static_models: None,
            api_key: api_key.into(),
            auth_mode: "apikey".into(),
            use_proxy: false,
            last_test_success: None,
            last_test_at: None,
            is_enabled: true,
            created_at: String::new(),
            updated_at: String::new(),
        }
    }

    fn minimal_chat_request() -> AiRequest {
        use crate::protocol::ir::{Message, MessageContent, Role};
        let messages = vec![Message {
            role: Role::User,
            content: MessageContent::Text("ping".into()),
            tool_calls: None,
            tool_call_id: None,
            meta: None,
        }];
        let mut req = AiRequest::new("ignored-by-actual-model", messages);
        req.meta.source_protocol = Some(OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1);
        req
    }

    async fn build_test_gateway() -> Gateway {
        let mut config = GatewayConfig::default();
        config.data_dir = PathBuf::from(std::env::temp_dir())
            .join(format!("nyro-pipeline-test-{}", Uuid::new_v4()));
        let (gw, _log_rx) = Gateway::new(config).await.expect("gateway init");
        gw
    }

    #[tokio::test]
    async fn build_request_suppresses_default_auth_when_oauth_owns_it() {
        let gw = build_test_gateway().await;
        let provider = provider_with_api_key("would-leak-if-bypassed");
        let mut req = minimal_chat_request();
        let ctx = ProviderCtx {
            provider: &provider,
            protocol: OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
            egress_base_url: "https://upstream.local",
            api_key: &provider.api_key,
            actual_model: "gpt-test",
            credential: None,
            gw: &gw,
            disable_default_auth: true,
        };
        let out = build_request(&FakeApiKeyVendor, &mut req, &ctx)
            .await
            .expect("build_request succeeds");
        assert!(
            out.headers.get("x-api-key").is_none(),
            "OAuth provider must not emit fallback x-api-key, got: {:?}",
            out.headers.get("x-api-key"),
        );
    }

    #[tokio::test]
    async fn build_request_keeps_default_auth_when_no_oauth() {
        let gw = build_test_gateway().await;
        let provider = provider_with_api_key("apikey-abc");
        let mut req = minimal_chat_request();
        let ctx = ProviderCtx {
            provider: &provider,
            protocol: OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
            egress_base_url: "https://upstream.local",
            api_key: &provider.api_key,
            actual_model: "gpt-test",
            credential: None,
            gw: &gw,
            disable_default_auth: false,
        };
        let out = build_request(&FakeApiKeyVendor, &mut req, &ctx)
            .await
            .expect("build_request succeeds");
        assert_eq!(
            out.headers.get("x-api-key").and_then(|v| v.to_str().ok()),
            Some("apikey-abc"),
            "API-key path must still propagate x-api-key to upstream",
        );
    }

    /// Pins the interaction: when an OAuth driver owns auth
    /// (`disable_default_auth=true`) AND the egress family is Anthropic, the
    /// `Authorization → x-api-key` rewrite must NOT fire.
    #[tokio::test]
    async fn build_request_does_not_rewrite_oauth_bearer_to_xapikey_on_anthropic_egress() {
        let gw = build_test_gateway().await;
        let provider = provider_with_api_key("");
        let mut req = minimal_chat_request();
        let ctx = ProviderCtx {
            provider: &provider,
            protocol: ANTHROPIC_MESSAGES_2023_06_01,
            egress_base_url: "https://api.anthropic.com",
            api_key: "oauth_bearer_token_should_not_become_xapikey",
            actual_model: "claude-sonnet-4-6",
            credential: None,
            gw: &gw,
            disable_default_auth: true,
        };
        let out = build_request(&FakeBearerVendor, &mut req, &ctx)
            .await
            .expect("build_request succeeds");
        assert!(
            out.headers.get("x-api-key").is_none(),
            "OAuth Bearer must not be rewritten as x-api-key, got: {:?}",
            out.headers.get("x-api-key"),
        );
        assert!(
            out.headers.get(reqwest::header::AUTHORIZATION).is_none(),
            "default Authorization must be suppressed under disable_default_auth too, got: {:?}",
            out.headers.get(reqwest::header::AUTHORIZATION),
        );
    }

    /// Mirror of #105's main use case: API-key-mode OpenAI-compat vendor
    /// hitting Anthropic egress — the rewrite block MUST fire and turn
    /// `Authorization: Bearer` into `x-api-key`.
    #[tokio::test]
    async fn build_request_rewrites_bearer_to_xapikey_on_anthropic_egress_for_apikey_path() {
        let gw = build_test_gateway().await;
        let provider = provider_with_api_key("real-anthropic-key");
        let mut req = minimal_chat_request();
        let ctx = ProviderCtx {
            provider: &provider,
            protocol: ANTHROPIC_MESSAGES_2023_06_01,
            egress_base_url: "https://api.anthropic.com",
            api_key: &provider.api_key,
            actual_model: "claude-sonnet-4-6",
            credential: None,
            gw: &gw,
            disable_default_auth: false,
        };
        let out = build_request(&FakeBearerVendor, &mut req, &ctx)
            .await
            .expect("build_request succeeds");
        assert_eq!(
            out.headers.get("x-api-key").and_then(|v| v.to_str().ok()),
            Some("real-anthropic-key"),
            "API-key path on Anthropic egress must produce x-api-key",
        );
        assert!(
            out.headers.get(reqwest::header::AUTHORIZATION).is_none(),
            "Authorization must be removed once x-api-key is set",
        );
    }
}
