//! Dispatcher: single orchestration point that drives a request through the
//! full proxy pipeline.
//!
//! `dispatch_pipeline` is the canonical entry point. Each ingress thin-shell
//! decodes the incoming body into an `InternalRequest` and calls this function.
//!
//! Pipeline:
//!   1. Route lookup + type gate (embedding vs chat).
//!   2. `authorize_route_access` (API-key auth + quota).
//!   3. Request hooks.
//!   4. Target iteration (health-aware): for each live target →
//!      a. Resolve `Provider` + `ProviderRuntime`.
//!      b. Resolve egress protocol + base URL via `negotiate()`.
//!      c. Look up `Vendor` from `VendorRegistry`.
//!      d. Build outbound: `ProtocolMode::Native` + no mutations → `passthrough_run`;
//!         else full 7-step `adapter.build_request`.
//!      e. Merge `runtime_binding` extra-headers.
//!      f. HTTP call → `handle_non_stream` / `handle_stream`.
//!      g. On success: record health, return; on retryable error: continue.
//!   5. Return last error or 502.

mod accumulator;
mod auth;
mod non_stream;
mod stream;
mod util;
use self::accumulator::*;
use self::auth::{GatewayProxyAccessStore, authorize_model_access, get_provider};
use self::non_stream::{handle_non_stream, handle_non_stream_via_upstream_stream};
use self::stream::handle_stream;
use self::util::*;

use std::time::Instant;

use axum::http::HeaderMap;
use axum::response::Response;
use serde_json::Value;

use crate::Gateway;
use crate::db::models::Provider;
use crate::error::{AuthFailure, GatewayError};
use crate::protocol::ProviderProtocols;
use crate::protocol::ids::ProtocolId;
use crate::protocol::ir::Usage;
use crate::protocol::ir::{AiRequest, AiResponse, RawEnvelope};
use crate::provider::VendorRegistry;
use crate::provider::vendor::ProviderCtx;
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
///   b. Auth.
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
    let request_headers_str =
        crate::proxy::observability::header_map_to_redacted_json(&envelope.headers);
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
        let cache = gw.model_cache.read().await;
        cache.match_model(&request_model).cloned()
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
    let auth_key = match authorize_model_access(&access_store, &route, &headers).await {
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

    // ── Request hooks ──────────────────────────────────────────────────────────

    let hook_registry = crate::integrations::HookRegistry::global();
    if hook_registry.has_request_hooks() {
        let hook_ctx = crate::integrations::HookContext {
            model_id: route.id.clone(),
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

    let targets = load_model_backends(&gw, &route).await;
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
    let ordered_targets = TargetSelector::select_ordered(&route.balance, &targets);
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
            match crate::provider::common::pipeline::passthrough_run(
                adapter.as_ref(),
                raw,
                &ctx,
                is_stream,
            )
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

        // Merge runtime-binding extra headers and safe client headers.
        //
        // Precedence: runtime binding < forwarded client hints < adapter.
        // Sensitive client headers (auth keys, cookies, IP/host forwarding
        // metadata, hop-by-hop transport headers) are filtered in
        // `forwarded_client_headers`, while adapter/provider auth remains
        // authoritative.
        match runtime_binding_headers(&provider_runtime.binding) {
            Ok(binding_hdrs) => {
                let mut merged = binding_hdrs;
                merged.extend(forwarded_client_headers(&headers));
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
            model_id: &route.id,
            model_name: &route.name,
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
        let response = if is_stream {
            handle_stream(
                client,
                &outbound.url,
                outbound.headers,
                outbound.body,
                &call_ctx,
                &req_extras,
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
            )
            .await
        } else {
            handle_non_stream(
                client,
                &outbound.url,
                outbound.headers,
                outbound.body,
                &call_ctx,
                &req_extras,
                adapter.as_ref(),
                &ctx,
                passthrough_resp,
            )
            .await
        };

        let status = response.status().as_u16();
        if status < 400 {
            gw.health_registry.record_success(&target_key);
            let elapsed_ms = start.elapsed().as_secs_f64() * 1000.0;
            TargetSelector::record_selected(&route.balance, &target_key);
            TargetSelector::record_latency(&route.balance, &target_key, elapsed_ms);
            return response;
        }
        gw.health_registry.record_failure(&target_key);
        if is_retryable(status) {
            last_response = Some(response);
            continue;
        }
        return response;
    }

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
    model_id: &'a str,
    model_name: &'a str,
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
    model_id: Option<String>,
    model_name: Option<String>,
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
            model_id: Some(call_ctx.model_id.to_string()),
            model_name: Some(call_ctx.model_name.to_string()),
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
            model_id: None,
            model_name: None,
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

    fn upstream_url(mut self, url: &str) -> Self {
        self.extras.upstream_url = Some(crate::proxy::observability::redact_url_credentials(url));
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
            model_id: self.model_id,
            model_name: self.model_name,
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

// Utility helpers (is_retryable, runtime_binding_headers, load_model_backends,
// forwarded_client_headers) are in util.rs.

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
        404 => GatewayError::ModelNotFound {
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

#[cfg(test)]
mod tests {
    use super::dispatch_pipeline;
    use crate::Gateway;
    use crate::protocol::ids::OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1;
    use crate::protocol::ir::{AiRequest, RawEnvelope};
    use axum::http::{HeaderMap, StatusCode};
    use serde_json::Value;
    use std::collections::HashMap;

    #[tokio::test]
    async fn dispatch_logs_client_request_headers_redacted_when_route_missing() {
        let mut config = crate::config::GatewayConfig::default();
        config.data_dir = std::env::temp_dir().join(format!(
            "nyro-client-header-redaction-test-{}",
            uuid::Uuid::new_v4()
        ));
        let (gw, mut log_rx) = Gateway::new(config).await.expect("gateway init");
        let mut envelope_headers = HashMap::new();
        envelope_headers.insert("authorization".into(), "Bearer client-secret".into());
        envelope_headers.insert("x-api-key".into(), "client-key".into());
        envelope_headers.insert("content-type".into(), "application/json".into());
        let envelope = RawEnvelope::new(
            Some(serde_json::json!({"model": "missing-model"})),
            envelope_headers,
            "POST",
            "/v1/chat/completions",
        );
        let request = AiRequest::new("missing-model", Vec::new());

        let response = dispatch_pipeline(
            gw,
            HeaderMap::new(),
            envelope,
            request,
            OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
        )
        .await;

        assert_eq!(response.status(), StatusCode::NOT_FOUND);
        let entry = tokio::time::timeout(std::time::Duration::from_secs(1), log_rx.recv())
            .await
            .expect("log entry should be emitted")
            .expect("log channel should remain open");
        let headers = entry
            .client_request_headers
            .as_deref()
            .expect("client headers should be logged");
        let parsed: Value = serde_json::from_str(headers).expect("headers should be JSON");
        assert_eq!(parsed["authorization"], "***");
        assert_eq!(parsed["x-api-key"], "***");
        assert_eq!(parsed["content-type"], "application/json");
        assert!(!headers.contains("client-secret"));
        assert!(!headers.contains("client-key"));
    }
}
