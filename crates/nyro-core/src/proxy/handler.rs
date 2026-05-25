//! Ingress handlers that remain in the legacy handler module.
//!
//! PR-16: The full proxy pipeline (`proxy_pipeline`, `handle_non_stream`,
//! `handle_stream`, etc.) has been moved to `proxy/dispatcher.rs`.
//! Old ingress handlers (`openai_proxy`, `anthropic_proxy`, etc.) have been
//! replaced by `proxy/ingress/*.rs` thin shells wired directly in `server.rs`.
//!
//! This file now contains only `models_list`, which is a read-only endpoint
//! that does not go through the proxy pipeline.

use std::collections::{BTreeSet, HashSet};

use axum::Json;
use axum::extract::State;
use axum::http::HeaderMap;
use axum::response::{IntoResponse, Response};

use crate::Gateway;
use crate::proxy::security::{extract_api_key, is_key_expired};

// ── GET /v1/models ────────────────────────────────────────────────────────────

pub async fn models_list(State(gw): State<Gateway>, headers: HeaderMap) -> Response {
    let mut accessible_route_ids = HashSet::new();

    if let Some(raw_key) = extract_api_key(&headers)
        && let Some(store) = gw.storage.auth()
        && let Ok(Some(key_row)) = store.find_api_key(&raw_key).await
    {
        let key_active = key_row.is_enabled
            && key_row
                .expires_at
                .as_ref()
                .map(|expires| !is_key_expired(expires))
                .unwrap_or(true);

        if key_active && let Ok(bound_route_ids) = store.list_bound_model_ids(&key_row.id).await {
            accessible_route_ids.extend(bound_route_ids);
        }
    }

    let cache = gw.model_cache.read().await;
    let models = cache
        .models
        .iter()
        .filter(|model| !model.access_control || accessible_route_ids.contains(&model.id))
        .map(|model| model.virtual_model.trim())
        .filter(|model| !model.is_empty())
        .map(ToString::to_string)
        .collect::<BTreeSet<_>>();

    let data = models
        .into_iter()
        .map(|model| {
            serde_json::json!({
                "id": model,
                "object": "model",
                "created": 0,
                "owned_by": "Nyro"
            })
        })
        .collect::<Vec<_>>();

    Json(serde_json::json!({
        "object": "list",
        "data": data
    }))
    .into_response()
}
