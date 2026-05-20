//! Dispatcher: single orchestration point that drives a request through the
//! full proxy pipeline.
//!
//! `dispatch_pipeline` is the canonical entry point. Each ingress thin-shell
//! decodes the incoming body into an `InternalRequest` and calls this function.
//!
//! Pipeline:
//!   1. Route lookup + type gate (embedding vs chat).
//!   2. `authorize_route_access` (API-key auth + quota).
//!   3. Exact-cache check → singleflight dedup.
//!   4. Semantic-cache check.
//!   5. Target iteration (health-aware): for each live target →
//!      a. Resolve `Provider` + `ProviderRuntime`.
//!      b. Resolve egress protocol + base URL via `negotiate()`.
//!      c. Look up `Vendor` from `VendorRegistry`.
//!      d. Build outbound: `ProtocolMode::Native` + no mutations → `passthrough_run`;
//!         else full 7-step `adapter.build_request`.
//!      e. Merge `runtime_binding` extra-headers.
//!      f. HTTP call → `handle_non_stream` / `handle_stream`.
//!      g. On success: record health, return; on retryable error: continue.
//!   6. Finalize singleflight; return last error or 502.

mod accumulator;
mod auth;
mod non_stream;
mod stream;
mod util;
use self::accumulator::*;
use self::auth::{GatewayProxyAccessStore, authorize_route_access, get_provider};
use self::non_stream::{handle_non_stream, handle_non_stream_via_upstream_stream};
use self::stream::handle_stream;
use self::util::*;

use std::convert::Infallible;
use std::time::Instant;

use axum::Json;
use axum::body::Body;
use axum::http::{HeaderMap, HeaderValue, StatusCode, header};
use axum::response::{IntoResponse, Response};
use dashmap::mapref::entry::Entry as DashEntry;
use serde_json::Value;
use tokio::sync::broadcast;
use tokio::time::{Duration, timeout};
use tokio_stream::wrappers::ReceiverStream;

use crate::Gateway;
use crate::cache::entry::CacheEntry;
use crate::cache::key::{build_cache_key, build_semantic_partition};
use crate::db::models::Provider;
use crate::error::{AuthFailure, GatewayError};
use crate::protocol::ProviderProtocols;
use crate::protocol::ids::{
    OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1, OPENAI_COMPATIBLE_EMBEDDINGS_V1, ProtocolId,
};
use crate::protocol::ir::Usage;
use crate::protocol::ir::{AiRequest, AiResponse, RawEnvelope};
use crate::provider::vendor::ProviderCtx;
use crate::provider::{VendorCtx, VendorRegistry};
use crate::proxy::client::ProxyClient;
use crate::proxy::context::RequestContext;
use crate::proxy::observability::{LogExtras, send_log};
use crate::proxy::planner::{ProtocolMode, negotiate};
use crate::router::TargetSelector;

// ── Public entry points ───────────────────────────────────────────────────────

/// Full pipeline entry point.
///
/// Each ingress shell captures the raw body in a `RawEnvelope` and decodes
/// the body into an `AiRequest`, then hands off here.
///
/// Pipeline:
///   a. Resolve egress protocol + base URL via `negotiate()`.
///   b. Auth + cache check.
///   c. Look up `Vendor` from `VendorRegistry`.
///   d. Build outbound: `ProtocolMode::Native` + no mutations → `passthrough_run`;
///      else full 7-step `adapter.build_request`.
///   e. HTTP call → `handle_non_stream` / `handle_stream`.
pub async fn dispatch_pipeline(
    gw: Gateway,
    headers: HeaderMap,
    envelope: RawEnvelope,
    request: AiRequest,
    ingress: ProtocolId,
) -> Response {
    // Derive logging strings from envelope.
    let method_owned = envelope.method.clone();
    let path_owned = envelope.path.clone();
    let request_body_str = envelope
        .body
        .as_ref()
        .and_then(|b| serde_json::to_string(b).ok());
    let request_headers_str = serde_json::to_string(&envelope.headers).ok();
    // Built early so it can be used by both pre-loop log entries and the per-target handlers.
    let req_extras = RequestExtras {
        method: method_owned.clone(),
        path: path_owned.clone(),
        headers: request_headers_str.clone(),
        body: request_body_str.clone(),
    };
    let mut request = request;
    let start = Instant::now();
    let request_model = request.model.clone();
    let is_stream = request.stream.enabled;
    let ingress_str = ingress.to_string();

    // ── Route lookup ─────────────────────────────────────────────────────────

    let route = {
        let cache = gw.route_cache.read().await;
        cache.match_route(&request_model).cloned()
    };
    let route = match route {
        Some(r) => r,
        None => {
            let msg = format!("no route for model: {request_model}");
            LogBuilder::from_dispatch(&gw, &ingress_str, &request_model, None, start)
                .stream_flag(is_stream)
                .status(404)
                .with_req_extras(&req_extras)
                .resp_body(Some(
                    serde_json::json!({ "error": { "message": msg.clone() } }).to_string(),
                ))
                .emit();
            return error_response(404, &msg);
        }
    };

    // ── Auth ─────────────────────────────────────────────────────────────────

    let access_store = GatewayProxyAccessStore::new(&gw);
    let auth_key = match authorize_route_access(&access_store, &route, &headers).await {
        Ok(v) => v,
        Err(resp) => {
            let status = resp.status().as_u16() as i32;
            LogBuilder::from_dispatch(&gw, &ingress_str, &request_model, None, start)
                .stream_flag(is_stream)
                .status_i32(status)
                .with_req_extras(&req_extras)
                .emit();
            return resp;
        }
    };

    // ── Cache setup ───────────────────────────────────────────────────────────

    let cache_config = gw.effective_cache_config();
    let cache_backend = (**gw.cache_backend.load()).clone();
    let vector_store = (**gw.vector_store.load()).clone();
    let route_cache = resolve_route_cache(&route);
    let request_has_image = request_has_image_input(&request);
    let exact_enabled_for_route = cache_config.exact.enabled
        && cache_backend.is_some()
        && route_cache.exact.is_some()
        && !request_has_image;
    let semantic_enabled_for_route = cache_config.semantic.enabled
        && vector_store.is_some()
        && route_cache.semantic.is_some()
        && !request_has_image;
    let semantic_write_temp_allowed = request.generation.temperature.unwrap_or(0.0) <= 0.0;
    let request_cache_key = if exact_enabled_for_route || semantic_enabled_for_route {
        Some(build_cache_key(&request, ingress))
    } else {
        None
    };

    let exact_ttl = route_exact_ttl(&route_cache, cache_config.exact.default_ttl);
    let semantic_ttl = route_semantic_ttl(&route_cache, cache_config.semantic.default_ttl);
    let semantic_threshold =
        route_semantic_threshold(&route_cache, cache_config.semantic.similarity_threshold);
    let semantic_entry_key = request_cache_key
        .clone()
        .unwrap_or_else(|| build_cache_key(&request, ingress));
    let semantic_embedding = extract_semantic_embedding_input(&request);
    let semantic_partition = semantic_embedding
        .as_ref()
        .map(|(system_prompt, _)| build_semantic_partition(&request.model, system_prompt));

    // ── Exact cache read ──────────────────────────────────────────────────────

    if let (Some(cache_backend), Some(key)) = (cache_backend.as_ref(), request_cache_key.as_deref())
        && exact_enabled_for_route
        && let Ok(Some(bytes)) = cache_backend.get(key).await
        && let Ok(cached_entry) = serde_json::from_slice::<CacheEntry>(&bytes)
    {
        let response = cached_entry_to_response(
            ingress,
            &cached_entry,
            is_stream,
            Some(key),
            "EXACT",
            None,
            cache_config.exact.stream_replay_tps,
            cache_config.exact.expose_headers,
        );
        let cached_usage = cached_entry.usage.clone();
        LogBuilder::from_dispatch(
            &gw,
            &ingress_str,
            &request_model,
            auth_key.id.as_deref(),
            start,
        )
        .stream_flag(is_stream)
        .upstream_model_str(
            cached_entry
                .actual_model
                .as_deref()
                .unwrap_or(&request_model),
        )
        .provider_id_str(&cached_entry.provider_name)
        .status_i32(cached_entry.status_code as i32)
        .usage(cached_usage)
        .with_req_extras(&req_extras)
        .resp_body(serde_json::to_string(&cached_entry.payload).ok())
        .emit();
        return response;
    }

    // ── Singleflight ─────────────────────────────────────────────────────────

    let mut singleflight_leader: Option<(String, broadcast::Sender<Vec<u8>>)> = None;
    if exact_enabled_for_route && let Some(key) = request_cache_key.as_ref() {
        match gw.cache_in_flight.entry(key.clone()) {
            DashEntry::Occupied(entry) => {
                let mut rx = entry.get().subscribe();
                drop(entry);
                if let Ok(Ok(bytes)) = timeout(Duration::from_secs(120), rx.recv()).await
                    && !bytes.is_empty()
                    && let Ok(cached_entry) = serde_json::from_slice::<CacheEntry>(&bytes)
                {
                    let response = cached_entry_to_response(
                        ingress,
                        &cached_entry,
                        is_stream,
                        Some(key),
                        "EXACT",
                        None,
                        cache_config.exact.stream_replay_tps,
                        cache_config.exact.expose_headers,
                    );
                    let cached_usage = cached_entry.usage.clone();
                    LogBuilder::from_dispatch(
                        &gw,
                        &ingress_str,
                        &request_model,
                        auth_key.id.as_deref(),
                        start,
                    )
                    .stream_flag(is_stream)
                    .upstream_model_str(
                        cached_entry
                            .actual_model
                            .as_deref()
                            .unwrap_or(&request_model),
                    )
                    .provider_id_str(&cached_entry.provider_name)
                    .status_i32(cached_entry.status_code as i32)
                    .usage(cached_usage)
                    .with_req_extras(&req_extras)
                    .resp_body(serde_json::to_string(&cached_entry.payload).ok())
                    .emit();
                    return response;
                }
            }
            DashEntry::Vacant(entry) => {
                let (tx, _) = broadcast::channel(16);
                entry.insert(tx.clone());
                singleflight_leader = Some((key.clone(), tx));
            }
        }
    }

    // ── Semantic cache read ───────────────────────────────────────────────────

    let mut semantic_query_vector: Option<Vec<f32>> = None;
    if semantic_enabled_for_route
        && let (Some(vector_store), Some(partition), Some((_, semantic_text))) = (
            vector_store.as_ref(),
            semantic_partition.as_deref(),
            semantic_embedding.as_ref(),
        )
        && let Ok(vector) = compute_embedding(&gw, semantic_text).await
    {
        semantic_query_vector = Some(vector.clone());
        if let Ok(Some(hit)) = vector_store
            .search(partition, &vector, semantic_threshold)
            .await
            && let Ok(cached_entry) = serde_json::from_slice::<CacheEntry>(&hit.data)
            && !is_semantic_entry_expired(&cached_entry, semantic_ttl)
        {
            if exact_enabled_for_route
                && let (Some(cache_backend), Some(key)) =
                    (cache_backend.as_ref(), request_cache_key.as_deref())
            {
                let _ = cache_backend.set(key, &hit.data, Some(exact_ttl)).await;
            }
            let response = cached_entry_to_response(
                ingress,
                &cached_entry,
                is_stream,
                Some(&hit.key),
                "SEMANTIC",
                Some(hit.score),
                cache_config.semantic.stream_replay_tps,
                cache_config.semantic.expose_headers,
            );
            let cached_usage = cached_entry.usage.clone();
            LogBuilder::from_dispatch(
                &gw,
                &ingress_str,
                &request_model,
                auth_key.id.as_deref(),
                start,
            )
            .stream_flag(is_stream)
            .upstream_model_str(
                cached_entry
                    .actual_model
                    .as_deref()
                    .unwrap_or(&request_model),
            )
            .provider_id_str(&cached_entry.provider_name)
            .status_i32(cached_entry.status_code as i32)
            .usage(cached_usage)
            .with_req_extras(&req_extras)
            .resp_body(serde_json::to_string(&cached_entry.payload).ok())
            .emit();
            return response;
        }
    }

    let semantic_write_ctx = if semantic_enabled_for_route && semantic_write_temp_allowed {
        if let (Some(partition), Some((_, semantic_text))) =
            (semantic_partition.clone(), semantic_embedding.clone())
        {
            Some(SemanticWriteContext {
                partition,
                embedding_text: semantic_text,
                key: semantic_entry_key,
                query_vector: semantic_query_vector.clone(),
            })
        } else {
            None
        }
    } else {
        None
    };

    // ── Request hooks ──────────────────────────────────────────────────────────

    let hook_registry = crate::integrations::HookRegistry::global();
    if hook_registry.has_request_hooks() {
        let hook_ctx = crate::integrations::HookContext {
            route_id: route.id.clone(),
            provider_name: String::new(),
            model: request.model.clone(),
            api_key_id: auth_key.id.clone(),
        };
        for hook in hook_registry.request_hooks() {
            if let Err(e) = hook.on_request(&hook_ctx, &mut request).await {
                tracing::warn!(hook = hook.name(), error = %e, "request hook rejected request");
                LogBuilder::from_dispatch(
                    &gw,
                    &ingress_str,
                    &request_model,
                    auth_key.id.as_deref(),
                    start,
                )
                .stream_flag(is_stream)
                .status(500)
                .with_req_extras(&req_extras)
                .emit();
                return error_response(500, &e.to_string());
            }
        }
    }

    // ── Target iteration ──────────────────────────────────────────────────────

    let targets = load_route_targets(&gw, &route).await;
    if targets.is_empty() {
        LogBuilder::from_dispatch(
            &gw,
            &ingress_str,
            &request_model,
            auth_key.id.as_deref(),
            start,
        )
        .stream_flag(is_stream)
        .status(503)
        .with_req_extras(&req_extras)
        .emit();
        return error_response(503, "no route targets configured");
    }
    let ordered_targets = TargetSelector::select_ordered(&route.strategy, &targets);
    if ordered_targets.is_empty() {
        LogBuilder::from_dispatch(
            &gw,
            &ingress_str,
            &request_model,
            auth_key.id.as_deref(),
            start,
        )
        .stream_flag(is_stream)
        .status(503)
        .with_req_extras(&req_extras)
        .emit();
        return error_response(503, "no route targets configured");
    }

    let miss_expose_headers =
        cache_config.exact.expose_headers || cache_config.semantic.expose_headers;

    let mut last_response: Option<Response> = None;
    for target in ordered_targets {
        let target_key = format!("{}:{}", target.provider_id, target.model);
        if !gw.health_registry.is_healthy(&target_key) {
            continue;
        }
        let provider = match get_provider(&access_store, &target.provider_id).await {
            Ok(p) => p,
            Err(_) => continue,
        };
        let actual_model = if target.model.is_empty() || target.model == "*" {
            request_model.clone()
        } else {
            target.model.clone()
        };

        let mut request_for_target = request.clone();

        let provider_runtime = match gw.admin().resolve_provider_runtime(&provider).await {
            Ok(runtime) => runtime,
            Err(e) => {
                last_response = Some(error_response(
                    502,
                    &format!("provider credential error: {e}"),
                ));
                continue;
            }
        };

        // Resolve egress protocol + base URL via negotiate().
        let provider_protocols = ProviderProtocols::from_provider(&provider);
        let mut req_ctx = RequestContext::new(ingress, std::time::Duration::from_secs(30));
        let plan = match negotiate(ingress, None, Some(&provider_protocols), &mut req_ctx) {
            Ok(p) => p,
            Err(e) => {
                last_response = Some(e.render(None));
                continue;
            }
        };
        let egress = plan.egress;
        let egress_base_url = if let Some(base_url_override) = provider_runtime
            .binding
            .base_url_override
            .clone()
            .filter(|v| !v.trim().is_empty())
        {
            base_url_override
        } else if plan.base_url.is_empty() {
            provider.base_url.clone()
        } else {
            plan.base_url.clone()
        };

        // Look up Vendor for this vendor_id.
        let vendor_id = provider
            .vendor
            .as_deref()
            .map(str::trim)
            .filter(|v| !v.is_empty())
            .unwrap_or("custom");
        let adapter = match VendorRegistry::global().get_vendor(vendor_id) {
            Some(a) => a.clone(),
            None => {
                last_response = Some(error_response(
                    503,
                    &format!("no vendor registered for '{vendor_id}'"),
                ));
                continue;
            }
        };

        let credential = provider_runtime.access_token.clone();
        let ctx = ProviderCtx {
            provider: &provider,
            protocol: egress,
            egress_base_url: &egress_base_url,
            api_key: &credential,
            actual_model: &actual_model,
            credential: None,
            gw: &gw,
            disable_default_auth: provider_runtime.binding.disable_default_auth,
        };

        // Build outbound request — PassThrough (Native + no mutations) or full 7-step pipeline.
        let passthrough_req =
            plan.mode == ProtocolMode::Native && !adapter.declared_request_mutations();
        let passthrough_resp =
            plan.mode == ProtocolMode::Native && !adapter.declared_response_mutations();
        let mut outbound = if passthrough_req {
            let raw = envelope.body.clone().unwrap_or_default();
            match crate::provider::common::pipeline::passthrough_run(adapter.as_ref(), raw, &ctx)
                .await
            {
                Ok(o) => o,
                Err(e) => {
                    last_response = Some(e.render(None));
                    continue;
                }
            }
        } else {
            match adapter.build_request(&mut request_for_target, &ctx).await {
                Ok(o) => o,
                Err(e) => {
                    last_response = Some(e.render(None));
                    continue;
                }
            }
        };

        // Merge runtime-binding extra headers (runtime binding < adapter, adapter wins).
        match runtime_binding_headers(&provider_runtime.binding) {
            Ok(binding_hdrs) => {
                let mut merged = binding_hdrs;
                merged.extend(outbound.headers);
                outbound.headers = merged;
            }
            Err(e) => {
                last_response = Some(error_response(
                    502,
                    &format!("provider runtime binding error: {e}"),
                ));
                continue;
            }
        }

        let client = match gw.http_client_for_provider(provider.use_proxy).await {
            Ok(http_client) => ProxyClient::new(http_client),
            Err(e) => {
                let msg = format!("provider transport error: {e}");
                last_response = Some(error_response(502, &msg));
                continue;
            }
        };

        let egress_str = egress.to_string();
        let egress_caps = egress.handler().capabilities();
        let upstream_forces_stream = egress_caps.force_upstream_stream;

        // ── Build per-target context structs ─────────────────────────────────
        let call_ctx = CallCtx {
            gw: gw.clone(),
            provider: &provider,
            route_id: &route.id,
            route_name: &route.name,
            egress,
            ingress,
            ingress_str: &ingress_str,
            egress_str: &egress_str,
            request_model: &request_model,
            actual_model: &actual_model,
            api_key_id: auth_key.id.as_deref(),
            api_key_name: auth_key.name.as_deref(),
            is_stream,
            start,
        };
        let cache_ctx = CacheWriteCtx {
            cache_key: request_cache_key.as_deref(),
            allow_exact_store: exact_enabled_for_route,
            exact_cache_ttl: Some(exact_ttl),
            semantic: semantic_write_ctx.clone(),
            expose_headers: miss_expose_headers,
        };

        let response = if is_stream {
            handle_stream(
                client,
                &outbound.url,
                outbound.headers,
                outbound.body,
                &call_ctx,
                &cache_ctx,
                &req_extras,
                singleflight_leader.as_ref().map(|(k, _)| k.as_str()),
                singleflight_leader.as_ref().map(|(_, tx)| tx.clone()),
                passthrough_resp,
            )
            .await
        } else if upstream_forces_stream {
            handle_non_stream_via_upstream_stream(
                client,
                &outbound.url,
                outbound.headers,
                outbound.body,
                &call_ctx,
                &cache_ctx,
            )
            .await
        } else {
            handle_non_stream(
                client,
                &outbound.url,
                outbound.headers,
                outbound.body,
                &call_ctx,
                &cache_ctx,
                &req_extras,
                adapter.as_ref(),
                &ctx,
                passthrough_resp,
            )
            .await
        };

        let status = response.status().as_u16();
        if status < 400 {
            if !is_stream {
                finalize_singleflight(&gw, singleflight_leader.as_ref(), true).await;
            }
            gw.health_registry.record_success(&target_key);
            let elapsed_ms = start.elapsed().as_secs_f64() * 1000.0;
            TargetSelector::record_selected(&route.strategy, &target_key);
            TargetSelector::record_latency(&route.strategy, &target_key, elapsed_ms);
            return response;
        }
        gw.health_registry.record_failure(&target_key);
        if is_retryable(status) {
            last_response = Some(response);
            continue;
        }
        finalize_singleflight(&gw, singleflight_leader.as_ref(), false).await;
        return response;
    }

    finalize_singleflight(&gw, singleflight_leader.as_ref(), false).await;
    last_response.unwrap_or_else(|| {
        LogBuilder::from_dispatch(
            &gw,
            &ingress_str,
            &request_model,
            auth_key.id.as_deref(),
            start,
        )
        .stream_flag(is_stream)
        .status(502)
        .with_req_extras(&req_extras)
        .emit();
        error_response(502, "all route targets failed")
    })
}

/// Legacy entry point: takes a raw `Value` body, wraps it in a `RawEnvelope`,
/// decodes it, and calls `dispatch_pipeline`.
pub async fn dispatch(
    gw: Gateway,
    headers: HeaderMap,
    body: Value,
    ingress: ProtocolId,
    method: &'static str,
    path: &'static str,
    _ctx: &mut RequestContext,
) -> Response {
    let flat_headers: std::collections::HashMap<String, String> = headers
        .iter()
        .filter_map(|(k, v)| {
            v.to_str()
                .ok()
                .map(|vs| (k.as_str().to_lowercase(), vs.to_string()))
        })
        .collect();
    let envelope = RawEnvelope::new(Some(body.clone()), flat_headers, method, path);

    let decoder = ingress.handler().make_request_decoder();
    let request = match decoder.decode_request(body) {
        Ok(r) => r,
        Err(e) => return log_decode_error(&gw, &envelope, ingress, e),
    };

    dispatch_pipeline(gw, headers, envelope, request, ingress).await
}

// ── Handler context types ─────────────────────────────────────────────────────

/// Core per-request dispatch context: routing identity, timing, and log
/// metadata. Shared by all three HTTP-level handlers so they no longer need
/// a long flat parameter list for the same information.
struct CallCtx<'a> {
    gw: Gateway,
    provider: &'a Provider,
    route_id: &'a str,
    route_name: &'a str,
    egress: ProtocolId,
    ingress: ProtocolId,
    ingress_str: &'a str,
    egress_str: &'a str,
    request_model: &'a str,
    actual_model: &'a str,
    api_key_id: Option<&'a str>,
    api_key_name: Option<&'a str>,
    is_stream: bool,
    start: Instant,
}

/// Cache write parameters for a single upstream call. Shared by all three
/// HTTP-level handlers.
struct CacheWriteCtx<'a> {
    cache_key: Option<&'a str>,
    allow_exact_store: bool,
    exact_cache_ttl: Option<Duration>,
    /// Semantic-cache write context; `None` when semantic cache is disabled.
    semantic: Option<SemanticWriteContext>,
    expose_headers: bool,
}

/// Owned request HTTP metadata kept for log entries. Used by the non-stream
/// and stream handlers (not the force-stream handler which omits request
/// details from its log path).
struct RequestExtras {
    method: String,
    path: String,
    headers: Option<String>,
    body: Option<String>,
}

// ── Log builder ───────────────────────────────────────────────────────────────

/// Fluent builder for `LogEntry`. Eliminates the long flat parameter list at
/// call sites.
///
/// Create via `LogBuilder::from_ctx` (inside handler functions, where a
/// `CallCtx` is available) or `LogBuilder::from_dispatch` (in
/// `dispatch_pipeline` before a provider has been selected).  Chain setter
/// methods for the per-call fields, then call `emit` to enqueue the entry.
#[derive(Clone)]
struct LogBuilder {
    gw: Gateway,
    client_protocol: String,
    upstream_protocol: String,
    client_model: String,
    upstream_model: String,
    api_key_id: Option<String>,
    api_key_name: Option<String>,
    provider_id: String,
    provider_name: String,
    route_id: Option<String>,
    route_name: Option<String>,
    is_stream: bool,
    start: Instant,
    client_status_code: i32,
    usage: Usage,
    extras: LogExtras,
}

impl LogBuilder {
    /// Build from a handler-level `CallCtx`; identity fields are pre-filled.
    fn from_ctx(call_ctx: &CallCtx<'_>) -> Self {
        Self {
            gw: call_ctx.gw.clone(),
            client_protocol: call_ctx.ingress_str.to_string(),
            upstream_protocol: call_ctx.egress_str.to_string(),
            client_model: call_ctx.request_model.to_string(),
            upstream_model: call_ctx.actual_model.to_string(),
            api_key_id: call_ctx.api_key_id.map(ToString::to_string),
            api_key_name: call_ctx.api_key_name.map(ToString::to_string),
            provider_id: call_ctx.provider.id.clone(),
            provider_name: call_ctx.provider.name.clone(),
            route_id: Some(call_ctx.route_id.to_string()),
            route_name: Some(call_ctx.route_name.to_string()),
            is_stream: call_ctx.is_stream,
            start: call_ctx.start,
            client_status_code: 200,
            usage: Usage::default(),
            extras: LogExtras::default(),
        }
    }

    /// Build from dispatch-pipeline context before a provider is selected.
    /// `upstream_protocol` defaults to `client_protocol`; `upstream_model` and
    /// `provider_id` default to empty strings.
    fn from_dispatch(
        gw: &Gateway,
        ingress: &str,
        request_model: &str,
        api_key_id: Option<&str>,
        start: Instant,
    ) -> Self {
        Self {
            gw: gw.clone(),
            client_protocol: ingress.to_string(),
            upstream_protocol: ingress.to_string(),
            client_model: request_model.to_string(),
            upstream_model: String::new(),
            api_key_id: api_key_id.map(ToString::to_string),
            api_key_name: None,
            provider_id: String::new(),
            provider_name: String::new(),
            route_id: None,
            route_name: None,
            is_stream: false,
            start,
            client_status_code: 200,
            usage: Usage::default(),
            extras: LogExtras::default(),
        }
    }

    fn stream_flag(mut self, v: bool) -> Self {
        self.is_stream = v;
        self
    }

    fn status(mut self, code: u16) -> Self {
        self.client_status_code = code as i32;
        self
    }

    fn status_i32(mut self, code: i32) -> Self {
        self.client_status_code = code;
        self
    }

    fn upstream_model_str(mut self, m: &str) -> Self {
        self.upstream_model = m.to_string();
        self
    }

    fn provider_id_str(mut self, id: &str) -> Self {
        self.provider_id = id.to_string();
        self
    }

    fn usage(mut self, u: Usage) -> Self {
        self.usage = u;
        self
    }

    fn error(self, _msg: impl Into<String>) -> Self {
        // Error info is embedded in response body; kept for call-site compat.
        self
    }

    fn maybe_error(self, _msg: Option<String>) -> Self {
        self
    }

    /// Pre-fill the client request-side `LogExtras` fields (method, path,
    /// headers, body) from a `RequestExtras`.
    fn with_req_extras(mut self, req: &RequestExtras) -> Self {
        self.extras.method = Some(req.method.clone());
        self.extras.path = Some(req.path.clone());
        self.extras.client_request_headers = req.headers.clone();
        self.extras.client_request_body = req.body.clone();
        self
    }

    /// Set the upstream request wire (headers + body encoded for upstream).
    fn with_upstream_request(mut self, headers: Option<String>, body: Option<String>) -> Self {
        self.extras.upstream_request_headers = headers;
        self.extras.upstream_request_body = body;
        self
    }

    /// Set the upstream response wire.
    fn with_upstream_response(
        mut self,
        status: i32,
        headers: Option<String>,
        body: Option<String>,
        latency_ms: Option<i64>,
    ) -> Self {
        self.extras.upstream_status_code = Some(status);
        self.extras.upstream_response_headers = headers;
        self.extras.upstream_response_body = body;
        self.extras.latency_upstream_ms = latency_ms;
        self
    }

    fn upstream_resp_headers(mut self, h: Option<String>) -> Self {
        self.extras.upstream_response_headers = h;
        self
    }

    fn upstream_resp_body(mut self, b: Option<String>) -> Self {
        self.extras.upstream_response_body = b;
        self
    }

    fn upstream_status(mut self, code: i32) -> Self {
        self.extras.upstream_status_code = Some(code);
        self
    }

    /// Set the client response wire.
    fn with_client_response(mut self, headers: Option<String>, body: Option<String>) -> Self {
        self.extras.client_response_headers = headers;
        self.extras.client_response_body = body;
        self
    }

    fn stream_metrics(mut self, chunks: i32, first_chunk_ms: Option<i64>) -> Self {
        self.extras.stream_chunks_count = chunks;
        self.extras.stream_first_chunk_ms = first_chunk_ms;
        self
    }

    // ── Legacy shim ────────────────────────────────────────────────────────

    /// Maps `response_body` → `client_response_body`.
    fn resp_body(mut self, b: Option<String>) -> Self {
        self.extras.client_response_body = b;
        self
    }

    fn emit(self) {
        use crate::logging::LogEntry;
        let latency_total_ms = self.start.elapsed().as_millis() as i64;
        let entry = LogEntry {
            api_key_id: self.api_key_id,
            api_key_name: self.api_key_name,
            created_at: chrono::Utc::now().timestamp_millis(),
            client_protocol: self.client_protocol,
            upstream_protocol: self.upstream_protocol,
            provider_id: self.provider_id,
            provider_name: self.provider_name,
            route_id: self.route_id,
            route_name: self.route_name,
            upstream_url: self.extras.upstream_url,
            client_model: self.client_model,
            upstream_model: self.upstream_model,
            method: self.extras.method,
            path: self.extras.path,
            client_request_headers: self.extras.client_request_headers,
            client_request_body: self.extras.client_request_body,
            client_response_headers: self.extras.client_response_headers,
            client_response_body: self.extras.client_response_body,
            upstream_request_headers: self.extras.upstream_request_headers,
            upstream_request_body: self.extras.upstream_request_body,
            upstream_response_headers: self.extras.upstream_response_headers,
            upstream_response_body: self.extras.upstream_response_body,
            upstream_status_code: self.extras.upstream_status_code,
            client_status_code: self.client_status_code,
            latency_total_ms,
            latency_upstream_ms: self.extras.latency_upstream_ms,
            usage: self.usage,
            is_stream: self.is_stream,
            stream_chunks_count: self.extras.stream_chunks_count,
            stream_first_chunk_ms: self.extras.stream_first_chunk_ms,
        };
        send_log(&self.gw, entry);
    }
}

// ── Non-streaming / streaming handlers: see non_stream.rs and stream.rs ───────
// ── Auth helpers: see auth.rs ─────────────────────────────────────────────

// Cache helpers (SemanticWriteContext, resolve_route_cache, route_*_ttl,
// is_semantic_entry_expired, request_has_image_input, extract_semantic_embedding_input,
// is_retryable, runtime_binding_headers, load_route_targets) are in util.rs.

fn set_cache_headers(
    response: &mut Response,
    cache_status: &str,
    key: Option<&str>,
    score: Option<f64>,
    expose_headers: bool,
) {
    if !expose_headers {
        return;
    }
    let headers = response.headers_mut();
    if let Ok(value) = HeaderValue::from_str(cache_status) {
        headers.insert("X-NYRO-CACHE", value);
    }
    if let Some(key) = key
        && let Ok(value) = HeaderValue::from_str(key)
    {
        headers.insert("X-NYRO-CACHE-KEY", value);
    }
    if let Some(score) = score
        && let Ok(value) = HeaderValue::from_str(&format!("{score:.4}"))
    {
        headers.insert("X-NYRO-CACHE-SCORE", value);
    }
}

#[allow(clippy::too_many_arguments)]
fn cached_entry_to_response(
    ingress: ProtocolId,
    entry: &CacheEntry,
    is_stream: bool,
    cache_key: Option<&str>,
    cache_status: &str,
    score: Option<f64>,
    stream_replay_tps: u32,
    expose_headers: bool,
) -> Response {
    if is_stream && let Some(internal) = entry.internal_response.as_ref() {
        return replay_cached_stream(
            ingress,
            internal,
            cache_key,
            cache_status,
            score,
            stream_replay_tps,
            expose_headers,
        );
    }
    let mut response = (
        StatusCode::from_u16(entry.status_code).unwrap_or(StatusCode::OK),
        Json(entry.payload.clone()),
    )
        .into_response();
    set_cache_headers(
        &mut response,
        cache_status,
        cache_key,
        score,
        expose_headers,
    );
    response
}

fn replay_cached_stream(
    ingress: ProtocolId,
    ai_resp: &AiResponse,
    cache_key: Option<&str>,
    cache_status: &str,
    score: Option<f64>,
    stream_replay_tps: u32,
    expose_headers: bool,
) -> Response {
    let mut formatter = ingress.handler().make_stream_response_encoder();
    let ai_deltas = ai_response_to_deltas(ai_resp);
    let ai_deltas = if stream_replay_tps > 0 {
        split_text_deltas(ai_deltas, 4)
    } else {
        ai_deltas
    };
    let mut payloads: Vec<String> = formatter
        .format_deltas(&ai_deltas)
        .into_iter()
        .map(|ev| ev.to_sse_string())
        .collect();
    payloads.extend(
        formatter
            .format_done()
            .into_iter()
            .map(|ev| ev.to_sse_string()),
    );

    let interval = if stream_replay_tps > 0 {
        Some(std::time::Duration::from_micros(
            1_000_000 / stream_replay_tps as u64,
        ))
    } else {
        None
    };

    let (tx, rx) = tokio::sync::mpsc::channel::<Result<String, Infallible>>(payloads.len().max(1));
    tokio::spawn(async move {
        for (i, payload) in payloads.into_iter().enumerate() {
            if i > 0
                && let Some(d) = interval
            {
                tokio::time::sleep(d).await;
            }
            if tx.send(Ok(payload)).await.is_err() {
                break;
            }
        }
    });

    let body = Body::from_stream(ReceiverStream::new(rx));
    let mut response = Response::builder()
        .status(StatusCode::OK)
        .header(header::CONTENT_TYPE, "text/event-stream")
        .header(header::CACHE_CONTROL, "no-cache")
        .header(header::CONNECTION, "keep-alive")
        .body(body)
        .unwrap();
    set_cache_headers(
        &mut response,
        cache_status,
        cache_key,
        score,
        expose_headers,
    );
    response
}

fn ai_response_to_deltas(resp: &AiResponse) -> Vec<crate::protocol::ir::AiStreamDelta> {
    use crate::protocol::ir::AiStreamDelta;
    use crate::protocol::ir::response::ResponseItem;
    let mut deltas = vec![AiStreamDelta::MessageStart {
        id: if resp.id.is_empty() {
            format!("chatcmpl-{}", uuid::Uuid::new_v4().simple())
        } else {
            resp.id.clone()
        },
        model: resp.model.clone(),
    }];
    if let Some(reasoning) = &resp.reasoning_content
        && !reasoning.is_empty()
    {
        deltas.push(AiStreamDelta::ThinkingDelta(reasoning.clone()));
        if let Some(sig) = resp.reasoning_signature.as_ref().filter(|s| !s.is_empty()) {
            deltas.push(AiStreamDelta::ThinkingSignature(sig.clone()));
        }
    }

    if let Some(items) = &resp.items {
        let mut tool_index = 0;
        for item in items {
            match item {
                ResponseItem::OutputText { text } if !text.is_empty() => {
                    deltas.push(AiStreamDelta::TextDelta(text.clone()));
                }
                ResponseItem::Thinking { text } if !text.is_empty() => {
                    deltas.push(AiStreamDelta::ThinkingDelta(text.clone()));
                }
                ResponseItem::FunctionCall {
                    call_id,
                    name,
                    arguments,
                } => {
                    deltas.push(AiStreamDelta::ToolCallStart {
                        index: tool_index,
                        id: call_id.clone(),
                        name: name.clone(),
                    });
                    if !arguments.is_empty() {
                        deltas.push(AiStreamDelta::ToolCallDelta {
                            index: tool_index,
                            arguments: arguments.clone(),
                        });
                    }
                    tool_index += 1;
                }
                ResponseItem::Unknown { raw } => {
                    deltas.push(AiStreamDelta::Unknown {
                        raw: raw.to_string(),
                    });
                }
                _ => {}
            }
        }
    } else {
        if !resp.content.is_empty() {
            deltas.push(AiStreamDelta::TextDelta(resp.content.clone()));
        }
        for (index, tool_call) in resp.tool_calls.iter().enumerate() {
            deltas.push(AiStreamDelta::ToolCallStart {
                index,
                id: tool_call.id.clone(),
                name: tool_call.name.clone(),
            });
            if !tool_call.arguments.is_empty() {
                deltas.push(AiStreamDelta::ToolCallDelta {
                    index,
                    arguments: tool_call.arguments.clone(),
                });
            }
        }
    }

    if let Some(metadata) = resp.vendor.ingress.get("__google_response_metadata") {
        deltas.push(AiStreamDelta::Unknown {
            raw: serde_json::json!({"__google_response_metadata": metadata}).to_string(),
        });
    }
    deltas.push(AiStreamDelta::Usage(resp.usage.clone()));
    deltas.push(AiStreamDelta::Done {
        stop_reason: resp
            .stop_reason
            .clone()
            .unwrap_or_else(|| "stop".to_string()),
    });
    deltas
}

fn split_text_deltas(
    deltas: Vec<crate::protocol::ir::AiStreamDelta>,
    chunk_chars: usize,
) -> Vec<crate::protocol::ir::AiStreamDelta> {
    use crate::protocol::ir::AiStreamDelta;
    deltas
        .into_iter()
        .flat_map(|d| match d {
            AiStreamDelta::TextDelta(text) => {
                let chars: Vec<char> = text.chars().collect();
                if chars.len() <= chunk_chars {
                    return vec![AiStreamDelta::TextDelta(text)];
                }
                chars
                    .chunks(chunk_chars)
                    .map(|c| AiStreamDelta::TextDelta(c.iter().collect()))
                    .collect()
            }
            AiStreamDelta::ThinkingDelta(text) => {
                let chars: Vec<char> = text.chars().collect();
                if chars.len() <= chunk_chars {
                    return vec![AiStreamDelta::ThinkingDelta(text)];
                }
                chars
                    .chunks(chunk_chars)
                    .map(|c| AiStreamDelta::ThinkingDelta(c.iter().collect()))
                    .collect()
            }
            other => vec![other],
        })
        .collect()
}

async fn finalize_singleflight(
    gw: &Gateway,
    leader: Option<&(String, broadcast::Sender<Vec<u8>>)>,
    success: bool,
) {
    let Some((key, tx)) = leader else {
        return;
    };
    let payload = if success {
        let cache_backend = (**gw.cache_backend.load()).clone();
        if let Some(cache_backend) = cache_backend.as_ref() {
            cache_backend
                .get(key)
                .await
                .ok()
                .flatten()
                .unwrap_or_default()
        } else {
            Vec::new()
        }
    } else {
        Vec::new()
    };
    let _ = tx.send(payload);
    gw.cache_in_flight.remove(key);
}

/// Emit a `LogEntry` for a request that failed to decode at the ingress
/// boundary (before `dispatch_pipeline` runs) and return the corresponding
/// 400 `Response`. Ensures decode failures show up in the in-app log module
/// rather than only in stdout tracing.
pub(crate) fn log_decode_error(
    gw: &Gateway,
    envelope: &RawEnvelope,
    ingress: ProtocolId,
    err: impl std::fmt::Display,
) -> Response {
    let msg = format!("invalid request: {err}");
    let request_body_str = envelope
        .body
        .as_ref()
        .and_then(|b| serde_json::to_string(b).ok());
    let request_headers_str = serde_json::to_string(&envelope.headers).ok();
    let ingress_str = ingress.to_string();
    LogBuilder::from_dispatch(gw, &ingress_str, "", None, Instant::now())
        .status(400)
        .with_req_extras(&RequestExtras {
            method: envelope.method.clone(),
            path: envelope.path.clone(),
            headers: request_headers_str,
            body: request_body_str,
        })
        .resp_body(Some(
            serde_json::json!({ "error": { "message": msg.clone() } }).to_string(),
        ))
        .emit();
    error_response(400, &msg)
}

pub(crate) fn error_response(status: u16, message: &str) -> Response {
    let err: GatewayError = match status {
        400 => GatewayError::bad_request("bad_request", message),
        401 => GatewayError::Unauthorized {
            reason: AuthFailure::Invalid,
        },
        403 => GatewayError::Forbidden {
            reason: crate::error::AccessDenial::Custom(message.to_string()),
        },
        404 => GatewayError::RouteNotFound {
            model: message.to_string(),
        },
        429 => GatewayError::QuotaExceeded {
            window: crate::error::QuotaWindow {
                window_type: "request".to_string(),
                reset_at_secs: None,
            },
        },
        503 => GatewayError::provider_unavailable("unknown", message),
        502 => GatewayError::upstream_status("unknown", 502, Some(message.to_string())),
        _ => GatewayError::Internal {
            source: anyhow::anyhow!("{}", message),
        },
    };
    err.render(None)
}

// StreamResponseAccumulator and ensure_tool_index are in accumulator.rs.

// ── Semantic embedding computation ────────────────────────────────────────────

/// Compute an embedding vector for the given text using the configured
/// semantic-cache embedding route.
///
/// Uses `VendorRegistry` directly because this is an internal embedding
/// call on the embeddings endpoint, outside the main chat proxy pipeline.
async fn compute_embedding(gw: &Gateway, text: &str) -> anyhow::Result<Vec<f32>> {
    let runtime_cache = gw.effective_cache_config();
    let embedding_route = runtime_cache.semantic.embedding_route.trim();
    if embedding_route.is_empty() {
        anyhow::bail!("semantic cache embedding_route is empty");
    }
    let route = {
        let cache = gw.route_cache.read().await;
        cache.match_route(embedding_route).cloned()
    }
    .ok_or_else(|| anyhow::anyhow!("embedding route not found: {embedding_route}"))?;

    let targets = load_route_targets(gw, &route).await;
    if targets.is_empty() {
        anyhow::bail!("embedding route has no targets: {embedding_route}");
    }
    let ordered_targets = TargetSelector::select_ordered(&route.strategy, &targets);
    let access_store = GatewayProxyAccessStore::new(gw);
    let mut missing_openai_endpoint = false;

    for target in ordered_targets {
        let provider = match get_provider(&access_store, &target.provider_id).await {
            Ok(p) => p,
            Err(_) => continue,
        };
        let provider_runtime = match gw.admin().resolve_provider_runtime(&provider).await {
            Ok(r) => r,
            Err(_) => continue,
        };
        let openai_base_url = provider_runtime
            .binding
            .base_url_override
            .clone()
            .filter(|v| !v.trim().is_empty())
            .or_else(|| resolve_openai_base_url(&provider));
        let Some(openai_base_url) = openai_base_url else {
            missing_openai_endpoint = true;
            continue;
        };
        let actual_model = if target.model.is_empty() || target.model == "*" {
            embedding_route.to_string()
        } else {
            target.model.clone()
        };
        let extension =
            match VendorRegistry::global().resolve(&provider, OPENAI_COMPATIBLE_EMBEDDINGS_V1) {
                Some(ext) => ext.clone(),
                None => continue,
            };
        let credential = provider_runtime.access_token.clone();
        let upstream_url;
        let mut request_headers;
        {
            let ctx = VendorCtx {
                provider: &provider,
                protocol_id: OPENAI_COMPATIBLE_EMBEDDINGS_V1,
                api_key: &credential,
                actual_model: &actual_model,
                credential: None,
            };
            upstream_url = extension.build_url(&ctx, &openai_base_url, "/v1/embeddings");
            request_headers = match runtime_binding_headers(&provider_runtime.binding) {
                Ok(h) => h,
                Err(_) => continue,
            };
            if !provider_runtime.binding.disable_default_auth {
                request_headers.extend(extension.auth_headers(&ctx));
            }
        }
        let client = match gw.http_client_for_provider(provider.use_proxy).await {
            Ok(c) => ProxyClient::new(c),
            Err(_) => continue,
        };
        let request_body = serde_json::json!({ "model": actual_model, "input": text });
        match client
            .call_non_stream(&upstream_url, request_headers, request_body)
            .await
        {
            Ok((payload, status, _)) if status < 400 => {
                if let Some(vector) = parse_embedding_vector(&payload) {
                    return Ok(vector);
                }
            }
            _ => {}
        }
    }

    if missing_openai_endpoint {
        anyhow::bail!("embedding route targets must expose openai protocol");
    }
    anyhow::bail!("failed to compute embedding from route: {embedding_route}")
}

fn parse_embedding_vector(payload: &Value) -> Option<Vec<f32>> {
    let embedding = payload
        .get("data")
        .and_then(Value::as_array)?
        .first()?
        .get("embedding")
        .and_then(Value::as_array)?;
    let mut out = Vec::with_capacity(embedding.len());
    for v in embedding {
        out.push(v.as_f64()? as f32);
    }
    if out.is_empty() { None } else { Some(out) }
}

fn resolve_openai_base_url(provider: &Provider) -> Option<String> {
    let protocols = ProviderProtocols::from_provider(provider);
    if !protocols.supports(OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1) {
        return None;
    }
    let resolved = protocols.resolve_egress(OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1);
    let trimmed = resolved.base_url.trim();
    if trimmed.is_empty() {
        None
    } else {
        Some(trimmed.to_string())
    }
}

#[cfg(test)]
mod tests {
    use super::ai_response_to_deltas;
    use crate::protocol::StreamResponseEncoder;
    use crate::protocol::codec::google::gemini::stream::GoogleStreamFormatter;
    use crate::protocol::ir::response::ResponseItem;
    use crate::protocol::ir::{AiResponse, Usage};
    use serde_json::Value;

    #[test]
    fn cached_non_stream_gemini_response_replays_inline_data_as_stream() {
        let mut resp = AiResponse::new("cached-image", "gemini-3.1-flash-image-preview");
        resp.items = Some(vec![ResponseItem::Unknown {
            raw: serde_json::json!({
                "inlineData": {
                    "mimeType": "image/png",
                    "data": "iVBORw0KGgoAAAANSUhEUgA"
                }
            }),
        }]);
        resp.stop_reason = Some("stop".to_string());
        resp.usage = Usage {
            prompt_tokens: 24,
            completion_tokens: 1120,
            ..Usage::default()
        };
        resp.vendor.ingress.insert(
            "__google_response_metadata".to_string(),
            serde_json::json!({
                "responseId": "cached-image",
                "modelVersion": "gemini-3.1-flash-image-preview",
                "usageMetadata": {
                    "promptTokenCount": 24,
                    "candidatesTokenCount": 1120,
                    "totalTokenCount": 1144,
                    "trafficType": "ON_DEMAND"
                }
            }),
        );

        let deltas = ai_response_to_deltas(&resp);
        let mut formatter = GoogleStreamFormatter::new();
        let events = formatter.format_deltas(&deltas);

        let image_event = events
            .iter()
            .map(|event| serde_json::from_str::<Value>(&event.data).unwrap())
            .find(|value| {
                value["candidates"][0]["content"]["parts"]
                    .as_array()
                    .is_some_and(|parts| parts.iter().any(|part| part.get("inlineData").is_some()))
            })
            .expect("cached stream replay should contain inlineData");
        assert_eq!(
            image_event["candidates"][0]["content"]["parts"][0]["inlineData"]["data"],
            "iVBORw0KGgoAAAANSUhEUgA"
        );

        let usage_event = events
            .iter()
            .map(|event| serde_json::from_str::<Value>(&event.data).unwrap())
            .find(|value| value.get("usageMetadata").is_some())
            .expect("cached stream replay should contain terminal usage");
        assert_eq!(usage_event["usageMetadata"]["trafficType"], "ON_DEMAND");
        assert_eq!(usage_event["responseId"], "cached-image");
    }
}
