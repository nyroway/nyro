//! Thin ingress shell: POST /v1/chat/completions

use axum::Json;
use axum::extract::State;
use axum::http::HeaderMap;
use axum::response::Response;
use serde_json::Value;

use crate::Gateway;
use crate::protocol::ids::OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1;
use crate::protocol::ir::RawEnvelope;
use crate::proxy::context::RequestContext;
use crate::proxy::dispatcher::{dispatch_pipeline, log_decode_error};

pub async fn handler(
    State(gw): State<Gateway>,
    mut ctx: axum::extract::Extension<RequestContext>,
    headers: HeaderMap,
    Json(body): Json<Value>,
) -> Response {
    ctx.ingress_protocol = OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1;
    let flat_headers: std::collections::HashMap<String, String> = headers
        .iter()
        .filter_map(|(k, v)| {
            v.to_str()
                .ok()
                .map(|vs| (k.as_str().to_lowercase(), vs.to_string()))
        })
        .collect();
    let envelope = RawEnvelope::new(
        Some(body.clone()),
        flat_headers,
        "POST",
        "/v1/chat/completions",
    );
    let decoder = OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1
        .handler()
        .make_request_decoder();
    let request = match decoder.decode_request(body) {
        Ok(r) => r,
        Err(e) => {
            return log_decode_error(&gw, &envelope, OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1, e);
        }
    };
    dispatch_pipeline(
        gw,
        headers,
        envelope,
        request,
        OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
    )
    .await
}
