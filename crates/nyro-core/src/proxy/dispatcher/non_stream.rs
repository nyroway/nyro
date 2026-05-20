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

use crate::cache::entry::CacheEntry;
use crate::integrations::{HookContext, HookRegistry};
use crate::provider::inbound::InboundResponse;
use crate::provider::vendor::ProviderCtx;
use crate::proxy::client::ProxyClient;
use crate::proxy::observability::headers_to_json;

use super::{
    CacheWriteCtx, CallCtx, LogBuilder, RequestExtras, StreamResponseAccumulator,
    compute_embedding, error_response, set_cache_headers,
};

// ── Non-streaming response handler ───────────────────────────────────────────

pub(super) async fn handle_non_stream(
    client: ProxyClient,
    url: &str,
    headers: ReqwestHeaderMap,
    body: Value,
    call_ctx: &CallCtx<'_>,
    cache_ctx: &CacheWriteCtx<'_>,
    req_extras: &RequestExtras,
    adapter: &dyn crate::provider::vendor::Vendor,
    // `ctx` is the vendor-level provider context used for codec operations.
    ctx: &ProviderCtx<'_>,
    // When true: Native protocol + no response mutations → skip IR round-trip.
    passthrough_resp: bool,
) -> Response {
    let gw = &call_ctx.gw;
    let provider = call_ctx.provider;
    let egress = call_ctx.egress;
    let ingress = call_ctx.ingress;
    let egress_str = call_ctx.egress_str; // used in tracing::debug!
    let actual_model = call_ctx.actual_model;
    let cache_key = cache_ctx.cache_key;
    let allow_exact_store = cache_ctx.allow_exact_store;
    let exact_cache_ttl = cache_ctx.exact_cache_ttl;
    let semantic_write_ctx = cache_ctx.semantic.clone();
    let expose_headers = cache_ctx.expose_headers;
    // Shared log builder pre-filled with identity + request-side extras.
    let log = LogBuilder::from_ctx(call_ctx).with_req_extras(req_extras);

    let upstream_start = std::time::Instant::now();
    let call_result = match client
        .call_non_stream(url, headers.clone(), body.clone())
        .await
    {
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

    let (resp, status, upstream_headers) = call_result;
    let upstream_hdrs_str = headers_to_json(&upstream_headers);
    let upstream_req_hdrs_str = crate::proxy::observability::reqwest_headers_to_json(&headers);
    let upstream_req_body_str = serde_json::to_string(&body).ok();

    if status >= 400 {
        let body_str = serde_json::to_string(&resp).ok();
        log.status(status)
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
                .with_upstream_request(upstream_req_hdrs_str, upstream_req_body_str)
                .with_upstream_response(
                    status as i32,
                    upstream_hdrs_str.clone(),
                    Some(
                        serde_json::json!({ "error": { "message": format!("parse error: {e}") } })
                            .to_string(),
                    ),
                    Some(upstream_latency_ms),
                )
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
            route_id: call_ctx.route_id.to_string(),
            provider_name: call_ctx.provider.name.clone(),
            model: ai_resp.model.clone(),
            api_key_id: call_ctx.api_key_id.map(str::to_string),
        };
        for hook in hook_registry.response_hooks() {
            hook.on_response(&hook_ctx, &mut ai_resp, latency_ms).await;
        }
    }

    let is_tool = !ai_resp.tool_calls.is_empty();
    let usage = ai_resp.usage.clone();
    let formatter = ingress.handler().make_response_encoder();
    let output = formatter.format_response(&ai_resp);

    let response_body_full = serde_json::to_string(&output).ok();
    log.status(status)
        .usage(usage.clone())
        .with_upstream_request(upstream_req_hdrs_str, upstream_req_body_str)
        .with_upstream_response(
            status as i32,
            upstream_hdrs_str,
            upstream_resp_str,
            Some(upstream_latency_ms),
        )
        .with_client_response(None, response_body_full)
        .emit();

    let mut response = (
        StatusCode::from_u16(status).unwrap_or(StatusCode::OK),
        Json(output.clone()),
    )
        .into_response();
    set_cache_headers(&mut response, "MISS", cache_key, None, expose_headers);

    if status < 400 && !is_tool {
        let entry = CacheEntry {
            payload: output,
            status_code: status,
            provider_name: provider.name.clone(),
            actual_model: Some(actual_model.to_string()),
            usage,
            created_at_epoch_ms: chrono::Utc::now().timestamp_millis(),
            internal_response: Some(ai_resp),
        };
        if let Ok(bytes) = serde_json::to_vec(&entry) {
            if allow_exact_store {
                let cache_backend = (**gw.cache_backend.load()).clone();
                if let (Some(key), Some(cache_backend)) = (cache_key, cache_backend.as_ref()) {
                    let _ = cache_backend.set(key, &bytes, exact_cache_ttl).await;
                }
            }
            let vector_store = (**gw.vector_store.load()).clone();
            if let (Some(vector_store), Some(ctx)) =
                (vector_store.as_ref(), semantic_write_ctx.as_ref())
            {
                let vector = if let Some(existing) = ctx.query_vector.clone() {
                    Some(existing)
                } else {
                    compute_embedding(&gw, &ctx.embedding_text).await.ok()
                };
                if let Some(vector) = vector {
                    let _ = vector_store
                        .upsert(&ctx.partition, ctx.key.clone(), vector, bytes)
                        .await;
                }
            }
        }
    }
    response
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
    cache_ctx: &CacheWriteCtx<'_>,
) -> Response {
    let gw = &call_ctx.gw;
    let provider = call_ctx.provider;
    let egress = call_ctx.egress;
    let ingress = call_ctx.ingress;
    let actual_model = call_ctx.actual_model;
    let cache_key = cache_ctx.cache_key;
    let allow_exact_store = cache_ctx.allow_exact_store;
    let exact_cache_ttl = cache_ctx.exact_cache_ttl;
    let semantic_write_ctx = cache_ctx.semantic.clone();
    let expose_headers = cache_ctx.expose_headers;
    let log = LogBuilder::from_ctx(call_ctx);

    let upstream_start = std::time::Instant::now();
    let call_result = match client.call_stream(url, headers.clone(), body.clone()).await {
        Ok(r) => r,
        Err(e) => {
            log.status(502).emit();
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

    let is_tool = !ai_resp.tool_calls.is_empty();
    let usage = ai_resp.usage.clone();
    let formatter = ingress.handler().make_response_encoder();
    let output = formatter.format_response(&ai_resp);

    let client_resp_body_str = serde_json::to_string(&output).ok();
    log.status(status)
        .usage(usage.clone())
        .with_upstream_request(upstream_req_hdrs_str, upstream_req_body_str)
        .upstream_resp_headers(upstream_hdrs_str)
        .with_client_response(None, client_resp_body_str)
        .emit();

    let mut response = (
        StatusCode::from_u16(status).unwrap_or(StatusCode::OK),
        Json(output.clone()),
    )
        .into_response();
    set_cache_headers(&mut response, "MISS", cache_key, None, expose_headers);

    if status < 400 && !is_tool {
        let entry = CacheEntry {
            payload: output,
            status_code: status,
            provider_name: provider.name.clone(),
            actual_model: Some(actual_model.to_string()),
            usage,
            created_at_epoch_ms: chrono::Utc::now().timestamp_millis(),
            internal_response: Some(ai_resp),
        };
        if let Ok(bytes) = serde_json::to_vec(&entry) {
            if allow_exact_store {
                let cache_backend = (**gw.cache_backend.load()).clone();
                if let (Some(key), Some(cache_backend)) = (cache_key, cache_backend.as_ref()) {
                    let _ = cache_backend.set(key, &bytes, exact_cache_ttl).await;
                }
            }
            let vector_store = (**gw.vector_store.load()).clone();
            if let (Some(vector_store), Some(ctx)) =
                (vector_store.as_ref(), semantic_write_ctx.as_ref())
            {
                let vector = if let Some(existing) = ctx.query_vector.clone() {
                    Some(existing)
                } else {
                    compute_embedding(&gw, &ctx.embedding_text).await.ok()
                };
                if let Some(vector) = vector {
                    let _ = vector_store
                        .upsert(&ctx.partition, ctx.key.clone(), vector, bytes)
                        .await;
                }
            }
        }
    }
    response
}
