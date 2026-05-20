//! Thin ingress shell: POST /v1beta/models/:model_action

use axum::Json;
use axum::extract::{Path, State};
use axum::http::HeaderMap;
use axum::response::Response;
use serde_json::Value;

use crate::Gateway;
use crate::protocol::codec::google::gemini::decoder::GoogleDecoder;
use crate::protocol::ids::GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA;
use crate::protocol::ir::RawEnvelope;
use crate::proxy::context::RequestContext;
use crate::proxy::dispatcher::{dispatch_pipeline, log_decode_error};

pub async fn handler(
    State(gw): State<Gateway>,
    mut ctx: axum::extract::Extension<RequestContext>,
    headers: HeaderMap,
    Path(model_action): Path<String>,
    Json(body): Json<Value>,
) -> Response {
    ctx.ingress_protocol = GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA;
    let (model, action) = match model_action.rsplit_once(':') {
        Some((m, a)) => (m.to_string(), a.to_string()),
        None => (model_action.clone(), "generateContent".to_string()),
    };
    let is_stream = action == "streamGenerateContent";
    let path = format!("/v1beta/models/{model_action}");
    let flat_headers: std::collections::HashMap<String, String> = headers
        .iter()
        .filter_map(|(k, v)| {
            v.to_str()
                .ok()
                .map(|vs| (k.as_str().to_lowercase(), vs.to_string()))
        })
        .collect();
    let envelope = RawEnvelope::new(Some(body.clone()), flat_headers, "POST", &path);
    let request = match GoogleDecoder.decode_with_model(body, &model, is_stream) {
        Ok(r) => r,
        Err(e) => {
            return log_decode_error(
                &gw,
                &envelope,
                GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA,
                format!("Gemini decode error: {e}"),
            );
        }
    };
    dispatch_pipeline(
        gw,
        headers,
        envelope,
        request,
        GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA,
    )
    .await
}
