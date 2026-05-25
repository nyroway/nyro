//! Non-streaming response handlers.
//!
//! `handle_non_stream`: standard non-streaming upstream call.
//! `handle_non_stream_via_upstream_stream`: upstream forces SSE but client
//!   requested non-stream — accumulate into a single response.

use axum::Json;
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use futures::StreamExt;
use reqwest::header::HeaderMap as ReqwestHeaderMap;
use serde_json::Value;

use crate::integrations::{HookContext, HookRegistry};
use crate::provider::inbound::InboundResponse;
use crate::provider::vendor::ProviderCtx;
use crate::proxy::client::{ProxyClient, UpstreamResponseDecodeError};
use crate::proxy::observability::headers_to_json;

use super::{CallCtx, LogBuilder, RequestExtras, StreamResponseAccumulator, error_response};

// ── Non-streaming response handler ───────────────────────────────────────────

pub(super) async fn handle_non_stream(
    client: ProxyClient,
    url: &str,
    headers: ReqwestHeaderMap,
    body: Value,
    call_ctx: &CallCtx<'_>,
    req_extras: &RequestExtras,
    adapter: &dyn crate::provider::vendor::Vendor,
    // `ctx` is the vendor-level provider context used for codec operations.
    ctx: &ProviderCtx<'_>,
    // When true: Native protocol + no response mutations → skip IR round-trip.
    passthrough_resp: bool,
) -> Response {
    let egress = call_ctx.egress;
    let ingress = call_ctx.ingress;
    let egress_str = call_ctx.egress_str; // used in tracing::debug!
    let actual_model = call_ctx.actual_model;
    // Shared log builder pre-filled with identity + request-side extras.
    let log = LogBuilder::from_ctx(call_ctx).with_req_extras(req_extras);
    let upstream_req_hdrs_str = crate::proxy::observability::reqwest_headers_to_json(&headers);
    let upstream_req_body_str = serde_json::to_string(&body).ok();

    let upstream_start = std::time::Instant::now();
    let call_result = match client
        .call_non_stream(url, headers.clone(), body.clone())
        .await
    {
        Ok(r) => r,
        Err(e) => {
            let upstream_latency_ms = upstream_start.elapsed().as_millis() as i64;
            let log = log
                .upstream_url(url)
                .with_upstream_request(upstream_req_hdrs_str, upstream_req_body_str);
            if let Some(decode) = e.downcast_ref::<UpstreamResponseDecodeError>() {
                let upstream_hdrs_str = headers_to_json(&decode.headers);
                let upstream_body_str = Some(decode.body_text());
                log.status(502)
                    .with_upstream_response(
                        decode.status as i32,
                        upstream_hdrs_str,
                        upstream_body_str,
                        Some(upstream_latency_ms),
                    )
                    .resp_body(Some(
                        serde_json::json!({ "error": { "message": format!("upstream error: {e}") } })
                            .to_string(),
                    ))
                    .emit();
            } else {
                log.status(502)
                    .resp_body(Some(
                        serde_json::json!({ "error": { "message": format!("upstream error: {e}") } })
                            .to_string(),
                    ))
                    .emit();
            }
            return error_response(502, &format!("upstream error: {e}"));
        }
    };
    let upstream_latency_ms = upstream_start.elapsed().as_millis() as i64;

    let (resp, status, upstream_headers) = call_result;
    let upstream_hdrs_str = headers_to_json(&upstream_headers);

    if status >= 400 {
        let body_str = serde_json::to_string(&resp).ok();
        log.status(status)
            .upstream_url(url)
            .upstream_status(status as i32)
            .with_upstream_request(upstream_req_hdrs_str, upstream_req_body_str)
            .with_upstream_response(
                status as i32,
                upstream_hdrs_str.clone(),
                body_str.clone(),
                Some(upstream_latency_ms),
            )
            .resp_body(body_str)
            .emit();
        return (
            StatusCode::from_u16(status).unwrap_or(StatusCode::BAD_GATEWAY),
            Json(resp),
        )
            .into_response();
    }

    // Embeddings: passthrough response (parse_response is not implemented for codec).
    if egress.handler().capabilities().embeddings {
        let usage = crate::protocol::codec::openai::compatible::embeddings::parse_usage(&resp);
        let resp_str = serde_json::to_string(&resp).ok();
        log.status(status)
            .upstream_url(url)
            .usage(usage)
            .with_upstream_request(upstream_req_hdrs_str, upstream_req_body_str)
            .with_upstream_response(
                status as i32,
                upstream_hdrs_str.clone(),
                resp_str.clone(),
                Some(upstream_latency_ms),
            )
            .with_client_response(None, resp_str)
            .emit();
        return (
            StatusCode::from_u16(status).unwrap_or(StatusCode::OK),
            Json(resp),
        )
            .into_response();
    }

    // PassThrough: Native protocol + no response mutations → forward upstream JSON verbatim,
    // skipping the IR round-trip (parse_response → InternalResponse → format_response).
    if passthrough_resp {
        tracing::debug!(
            mode = "passthrough",
            egress = egress_str,
            "bypassing IR round-trip"
        );
        let resp_str = serde_json::to_string(&resp).ok();
        log.status(status)
            .upstream_url(url)
            .with_upstream_request(upstream_req_hdrs_str, upstream_req_body_str)
            .with_upstream_response(
                status as i32,
                upstream_hdrs_str.clone(),
                resp_str.clone(),
                Some(upstream_latency_ms),
            )
            .with_client_response(None, resp_str)
            .emit();
        return (
            StatusCode::from_u16(status).unwrap_or(StatusCode::OK),
            Json(resp),
        )
            .into_response();
    }

    // Parse response via ProviderAdapter.
    let upstream_resp_str = serde_json::to_string(&resp).ok();
    let inbound = InboundResponse { status, body: resp };
    let mut ai_resp = match adapter.parse_response(inbound, ctx).await {
        Ok(r) => r,
        Err(e) => {
            log.status(500)
                .upstream_url(url)
                .with_upstream_request(upstream_req_hdrs_str, upstream_req_body_str)
                .with_upstream_response(
                    status as i32,
                    upstream_hdrs_str.clone(),
                    upstream_resp_str,
                    Some(upstream_latency_ms),
                )
                .resp_body(Some(
                    serde_json::json!({ "error": { "message": format!("parse error: {e}") } })
                        .to_string(),
                ))
                .emit();
            return error_response(500, &format!("parse error: {e}"));
        }
    };

    // Ensure actual_model is set in the response.
    if ai_resp.model.is_empty() {
        ai_resp.model = actual_model.to_string();
    }

    // ── Response hooks ──────────────────────────────────────────────────────
    let hook_registry = HookRegistry::global();
    if hook_registry.has_response_hooks() {
        let latency_ms = call_ctx.start.elapsed().as_millis() as u64;
        let hook_ctx = HookContext {
            model_id: call_ctx.model_id.to_string(),
            provider_name: call_ctx.provider.name.clone(),
            model: ai_resp.model.clone(),
            api_key_id: call_ctx.api_key_id.map(str::to_string),
        };
        for hook in hook_registry.response_hooks() {
            hook.on_response(&hook_ctx, &mut ai_resp, latency_ms).await;
        }
    }

    let usage = ai_resp.usage.clone();
    let formatter = ingress.handler().make_response_encoder();
    let output = formatter.format_response(&ai_resp);

    let response_body_full = serde_json::to_string(&output).ok();
    log.status(status)
        .upstream_url(url)
        .usage(usage)
        .with_upstream_request(upstream_req_hdrs_str, upstream_req_body_str)
        .with_upstream_response(
            status as i32,
            upstream_hdrs_str,
            upstream_resp_str,
            Some(upstream_latency_ms),
        )
        .with_client_response(None, response_body_full)
        .emit();

    let response = (
        StatusCode::from_u16(status).unwrap_or(StatusCode::OK),
        Json(output),
    )
        .into_response();
    response
}

#[cfg(test)]
mod tests {
    use super::*;
    use async_trait::async_trait;
    use reqwest::header::HeaderValue;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    use crate::Gateway;
    use crate::config::GatewayConfig;
    use crate::db::models::Provider;
    use crate::error::GatewayError;
    use crate::protocol::ids::{
        GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA, OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
    };
    use crate::protocol::ir::{AiRequest, AiResponse};
    use crate::provider::outbound::OutboundRequest;
    use crate::provider::registry::VendorScope;
    use crate::provider::vendor::Vendor;
    use crate::provider::vendor_ext::VendorCtx;

    struct NoopVendor;

    #[async_trait]
    impl Vendor for NoopVendor {
        fn scope(&self) -> VendorScope {
            VendorScope::Vendor { vendor_id: "noop" }
        }

        fn vendor_id(&self) -> &'static str {
            "noop"
        }

        fn supported_protocols(&self) -> &'static [crate::protocol::ids::ProtocolId] {
            &[GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA]
        }

        async fn build_request(
            &self,
            _req: &mut AiRequest,
            _ctx: &ProviderCtx<'_>,
        ) -> Result<OutboundRequest, GatewayError> {
            unreachable!("test calls handle_non_stream after outbound is built")
        }

        async fn parse_response(
            &self,
            _resp: InboundResponse,
            _ctx: &ProviderCtx<'_>,
        ) -> Result<AiResponse, GatewayError> {
            unreachable!("decode error happens before provider response parsing")
        }

        fn map_error(&self, status: u16, _body: Value) -> GatewayError {
            GatewayError::upstream_status("noop", status, None)
        }

        fn auth_headers(&self, _ctx: &VendorCtx<'_>) -> ReqwestHeaderMap {
            ReqwestHeaderMap::new()
        }
    }

    fn fake_provider(base_url: String) -> Provider {
        Provider {
            id: "provider-google".into(),
            name: "Google".into(),
            vendor: Some("google".into()),
            protocol: "google-gemini".into(),
            base_url,
            preset_key: None,
            channel: Some("default".into()),
            models_source: None,
            static_models: None,
            api_key: "secret".into(),
            auth_mode: "apikey".into(),
            use_proxy: false,
            last_test_success: None,
            last_test_at: None,
            is_enabled: true,
            created_at: String::new(),
            updated_at: String::new(),
        }
    }

    async fn serve_invalid_json_once() -> String {
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0")
            .await
            .expect("bind test server");
        let addr = listener.local_addr().expect("local addr");
        tokio::spawn(async move {
            let (mut socket, _) = listener.accept().await.expect("accept request");
            let mut buf = [0_u8; 2048];
            let _ = socket.read(&mut buf).await.expect("read request");
            socket
                .write_all(
                    b"HTTP/1.1 200 OK\r\ncontent-type: text/plain\r\nx-request-id: upstream-123\r\ncontent-length: 16\r\n\r\nnot valid json!!",
                )
                .await
                .expect("write response");
        });
        format!("http://{addr}/v1beta/models/gemini:generateContent?key=secret")
    }

    #[tokio::test]
    async fn logs_upstream_wire_data_when_non_stream_response_json_decode_fails() {
        let url = serve_invalid_json_once().await;
        let base_url = url.split("/v1beta").next().unwrap().to_string();
        let provider = fake_provider(base_url);
        let mut config = GatewayConfig::default();
        config.data_dir =
            std::env::temp_dir().join(format!("nyro-decode-log-test-{}", uuid::Uuid::new_v4()));
        let (gw, mut log_rx) = Gateway::new(config).await.expect("gateway init");

        let call_ctx = CallCtx {
            gw: gw.clone(),
            provider: &provider,
            model_id: "route-google",
            model_name: "Google route",
            egress: GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA,
            ingress: OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
            ingress_str: "openai/chat/v1",
            egress_str: "google/gemini/generateContent/v1beta",
            request_model: "virtual-gemini",
            actual_model: "gemini-2.5-flash",
            api_key_id: None,
            api_key_name: None,
            is_stream: false,
            start: std::time::Instant::now(),
        };
        let req_extras = RequestExtras {
            method: "POST".into(),
            path: "/v1/chat/completions".into(),
            headers: None,
            body: Some(r#"{"model":"virtual-gemini"}"#.into()),
        };
        let provider_ctx = ProviderCtx {
            provider: &provider,
            protocol: GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA,
            egress_base_url: &provider.base_url,
            api_key: "secret",
            actual_model: "gemini-2.5-flash",
            credential: None,
            gw: &gw,
            disable_default_auth: false,
        };
        let mut headers = ReqwestHeaderMap::new();
        headers.insert(
            reqwest::header::CONTENT_TYPE,
            HeaderValue::from_static("application/json"),
        );

        let response = handle_non_stream(
            ProxyClient::new(reqwest::Client::new()),
            &url,
            headers,
            serde_json::json!({"model": "gemini-2.5-flash"}),
            &call_ctx,
            &req_extras,
            &NoopVendor,
            &provider_ctx,
            false,
        )
        .await;

        assert_eq!(response.status(), StatusCode::BAD_GATEWAY);
        let entry = tokio::time::timeout(std::time::Duration::from_secs(1), log_rx.recv())
            .await
            .expect("log entry should be emitted")
            .expect("log channel should remain open");

        assert_eq!(entry.upstream_status_code, Some(200));
        assert_eq!(
            entry.upstream_response_body.as_deref(),
            Some("not valid json!!")
        );
        assert!(
            entry
                .upstream_response_headers
                .as_deref()
                .is_some_and(|h| h.contains("upstream-123"))
        );
        assert!(
            entry
                .upstream_request_body
                .as_deref()
                .is_some_and(|b| b.contains("gemini-2.5-flash"))
        );
        assert!(
            entry
                .upstream_url
                .as_deref()
                .is_some_and(|u| u.contains("generateContent") && u.contains("key=***"))
        );
        assert!(
            entry
                .client_response_body
                .as_deref()
                .is_some_and(|b| b.contains("error decoding response body"))
        );
    }
}

// ── Force-stream non-stream handler ──────────────────────────────────────────

/// Consume a streaming upstream response and return a non-streaming client
/// response. Used when the egress protocol forces `stream: true` upstream
/// (e.g. Responses API) but the ingress client requested non-stream.
pub(super) async fn handle_non_stream_via_upstream_stream(
    client: ProxyClient,
    url: &str,
    headers: ReqwestHeaderMap,
    body: Value,
    call_ctx: &CallCtx<'_>,
) -> Response {
    let egress = call_ctx.egress;
    let ingress = call_ctx.ingress;
    let actual_model = call_ctx.actual_model;
    let log = LogBuilder::from_ctx(call_ctx).upstream_url(url);

    let upstream_start = std::time::Instant::now();
    let call_result = match client.call_stream(url, headers.clone(), body.clone()).await {
        Ok(r) => r,
        Err(e) => {
            log.status(502)
                .resp_body(Some(
                    serde_json::json!({ "error": { "message": format!("upstream error: {e}") } })
                        .to_string(),
                ))
                .emit();
            return error_response(502, &format!("upstream error: {e}"));
        }
    };
    let upstream_latency_ms = upstream_start.elapsed().as_millis() as i64;

    let (resp, status) = call_result;
    let upstream_hdrs_str = headers_to_json(resp.headers());
    let upstream_req_hdrs_str = crate::proxy::observability::reqwest_headers_to_json(&headers);
    let upstream_req_body_str = serde_json::to_string(&body).ok();

    if status >= 400 {
        let err_body: Value = resp
            .json()
            .await
            .unwrap_or_else(|_| serde_json::json!({"error": {"message": "upstream error"}}));
        let err_body_str = serde_json::to_string(&err_body).ok();
        log.status(status)
            .upstream_url(url)
            .with_upstream_request(upstream_req_hdrs_str, upstream_req_body_str)
            .with_upstream_response(
                status as i32,
                upstream_hdrs_str,
                err_body_str.clone(),
                Some(upstream_latency_ms),
            )
            .resp_body(err_body_str)
            .emit();
        return (
            StatusCode::from_u16(status).unwrap_or(StatusCode::BAD_GATEWAY),
            Json(err_body),
        )
            .into_response();
    }

    let mut stream_parser = egress.handler().make_stream_response_decoder();
    let mut byte_stream = resp.bytes_stream();
    let mut accumulator = StreamResponseAccumulator::default();

    while let Some(chunk) = byte_stream.next().await {
        let bytes = match chunk {
            Ok(b) => b,
            Err(e) => {
                log.status(502)
                    .error(format!("stream read error: {e}"))
                    .with_upstream_request(upstream_req_hdrs_str, upstream_req_body_str)
                    .upstream_resp_headers(upstream_hdrs_str)
                    .resp_body(Some(
                        serde_json::json!({ "error": { "message": format!("upstream stream error: {e}") } })
                            .to_string(),
                    ))
                    .emit();
                return error_response(502, &format!("upstream stream error: {e}"));
            }
        };
        let text = String::from_utf8_lossy(&bytes);
        if let Ok(ai_deltas) = stream_parser.parse_chunk(&text) {
            accumulator.apply_all(&ai_deltas);
        }
    }

    if let Ok(ai_deltas) = stream_parser.finish() {
        accumulator.apply_all(&ai_deltas);
    }

    let mut ai_resp = accumulator.into_ai_response();
    if ai_resp.id.is_empty() {
        ai_resp.id = format!("chatcmpl-{}", uuid::Uuid::new_v4().simple());
    }
    if ai_resp.model.is_empty() {
        ai_resp.model = actual_model.to_string();
    }
    if ai_resp.stop_reason.is_none() {
        ai_resp.stop_reason = Some("stop".to_string());
    }

    let usage = ai_resp.usage.clone();
    let formatter = ingress.handler().make_response_encoder();
    let output = formatter.format_response(&ai_resp);

    let client_resp_body_str = serde_json::to_string(&output).ok();
    log.status(status)
        .upstream_url(url)
        .usage(usage)
        .with_upstream_request(upstream_req_hdrs_str, upstream_req_body_str)
        .with_upstream_response(
            status as i32,
            upstream_hdrs_str,
            None,
            Some(upstream_latency_ms),
        )
        .with_client_response(None, client_resp_body_str)
        .emit();

    let response = (
        StatusCode::from_u16(status).unwrap_or(StatusCode::OK),
        Json(output),
    )
        .into_response();
    response
}
