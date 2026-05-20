//! OpenAI Embeddings API (`POST /v1/embeddings`).
//!
//! Embeddings is a passthrough endpoint — the gateway forwards the
//! request body verbatim and only inspects `usage.prompt_tokens` from
//! the response. We still register a `EndpointHandler` so that:
//!
//! 1. `ProtocolRegistry::find_by_ingress_route` resolves
//!    `POST /v1/embeddings` (and `/embeddings`) to a known
//!    `ProtocolId` — same routing model as chat / responses /
//!    messages.
//! 2. `capabilities()` advertises `embeddings = true` (and
//!    `streaming`, `tools`, `force_upstream_stream` = `false`) so call
//!    sites branch declaratively.
//! 3. Vendor extension lookup goes through the same `(provider,
//!    protocol_id)` pair as chat completions.
//!
//! # PR-12: full field mapping + vendor extensions 三段化
//!
//! The decoder now explicitly parses every known field and segregates
//! vendor-specific (unknown) fields into `__vendor_ingress` following
//! the three-segment pattern (`ingress / egress / passthrough_safe`).
//! The encoder reconstructs the upstream body from the explicit keys,
//! forwarding only the fields allowed by `VendorFieldPolicy`.
//!
//! The original passthrough body is preserved in [`EMBEDDINGS_BODY_KEY`]
//! as a safety-net fallback so that no data is lost if the decoder
//! encounters an unexpected body shape.

use std::collections::HashMap;

use reqwest::header::HeaderMap;
use serde_json::Value;

use crate::protocol::SseEvent;
use crate::protocol::ids::{
    EndpointCapabilities, OPENAI_COMPATIBLE_EMBEDDINGS_V1, ProtocolEndpoint,
};
use crate::protocol::ir::Usage;
use crate::protocol::ir::{AiRequest, GenerationConfig, Message, StreamConfig};
use crate::protocol::registry::EndpointRegistration;
use crate::protocol::traits::*;

/// Key under which the complete original request body is kept as a
/// fallback (used by `embeddings_proxy` in handler.rs).
pub const EMBEDDINGS_BODY_KEY: &str = "__embeddings_passthrough_body__";

/// OpenAI-spec field names for the embeddings endpoint.
const KNOWN_EMBEDDINGS_FIELDS: &[&str] =
    &["model", "input", "dimensions", "encoding_format", "user"];

const CAPS: EndpointCapabilities = EndpointCapabilities {
    streaming: false,
    tools: false,
    reasoning: false,
    embeddings: true,
    force_upstream_stream: false,
    override_model_in_body: false,
    ingress_routes: &[("POST", "/v1/embeddings")],
    multimodal: false,
    structured_output: false,
    function_calling: false,
    parallel_tool_calls: false,
    extended_reasoning: false,
    deterministic_seed: false,
    stream: crate::protocol::ids::StreamCaps::DEFAULT,
    unknown_field_policy: crate::protocol::ids::VendorFieldPolicy::Drop,
    lossy_default_reject: true,
};

pub struct OpenAIEmbeddingsV1;

impl EndpointHandler for OpenAIEmbeddingsV1 {
    fn id(&self) -> ProtocolEndpoint {
        OPENAI_COMPATIBLE_EMBEDDINGS_V1
    }
    fn capabilities(&self) -> &'static EndpointCapabilities {
        &CAPS
    }
    fn make_request_decoder(&self) -> Box<dyn RequestDecoder + Send> {
        Box::new(EmbeddingsDecoder)
    }
    fn make_request_encoder(&self) -> Box<dyn RequestEncoder + Send> {
        Box::new(EmbeddingsEncoder)
    }
    fn make_response_decoder(&self) -> Box<dyn ResponseDecoder> {
        Box::new(EmbeddingsResponseParser)
    }
    fn make_response_encoder(&self) -> Box<dyn ResponseEncoder> {
        Box::new(EmbeddingsResponseFormatter)
    }
    fn make_stream_response_decoder(&self) -> Box<dyn StreamResponseDecoder> {
        Box::new(EmbeddingsStreamParser)
    }
    fn make_stream_response_encoder(&self) -> Box<dyn StreamResponseEncoder> {
        Box::new(EmbeddingsStreamFormatter)
    }
}

inventory::submit! {
    EndpointRegistration { make: || Box::new(OpenAIEmbeddingsV1) }
}

// ── Decoder ───────────────────────────────────────────────────────────────────

struct EmbeddingsDecoder;

impl RequestDecoder for EmbeddingsDecoder {
    fn decode_request(&self, body: Value) -> anyhow::Result<AiRequest> {
        let obj = body
            .as_object()
            .ok_or_else(|| anyhow::anyhow!("embeddings request must be a JSON object"))?;

        let model = obj
            .get("model")
            .and_then(|v| v.as_str())
            .map(str::trim)
            .filter(|m| !m.is_empty())
            .map(ToString::to_string)
            .ok_or_else(|| anyhow::anyhow!("model is required for embeddings"))?;

        // ── Vendor ingress bag (backward compat for EmbeddingsEncoder) ─────────
        let mut ingress: HashMap<String, Value> = HashMap::new();

        // Keep the original body so `embeddings_proxy` can forward it.
        ingress.insert(EMBEDDINGS_BODY_KEY.to_string(), body.clone());

        if let Some(input) = obj.get("input") {
            ingress.insert("__emb_input".into(), input.clone());
        }
        if let Some(dims) = obj.get("dimensions") {
            ingress.insert("__emb_dimensions".into(), dims.clone());
        }
        if let Some(ef) = obj.get("encoding_format") {
            ingress.insert("__emb_encoding_format".into(), ef.clone());
        }
        if let Some(user) = obj.get("user") {
            ingress.insert("__emb_user".into(), user.clone());
        }

        // Collect unknown fields into __vendor_ingress.
        let mut vendor_ingress = serde_json::Map::new();
        for (k, v) in obj {
            if !KNOWN_EMBEDDINGS_FIELDS.contains(&k.as_str()) {
                vendor_ingress.insert(k.clone(), v.clone());
            }
        }
        if !vendor_ingress.is_empty() {
            ingress.insert("__vendor_ingress".into(), Value::Object(vendor_ingress));
        }

        let mut ai_req = AiRequest::new(model, Vec::<Message>::new());
        ai_req.generation = GenerationConfig::default();
        ai_req.stream = StreamConfig {
            enabled: false,
            include_usage: false,
        };
        ai_req.meta.source_protocol = Some(OPENAI_COMPATIBLE_EMBEDDINGS_V1);
        ai_req.meta.vendor.ingress = ingress;

        Ok(ai_req)
    }
}

// ── Encoder ───────────────────────────────────────────────────────────────────

struct EmbeddingsEncoder;

impl RequestEncoder for EmbeddingsEncoder {
    fn encode_request(&self, req: &AiRequest) -> anyhow::Result<(Value, HeaderMap)> {
        let ingress = &req.meta.vendor.ingress;
        let mut obj = serde_json::Map::new();

        obj.insert("model".into(), Value::String(req.model.clone()));

        if let Some(input) = ingress.get("__emb_input") {
            obj.insert("input".into(), input.clone());
        } else if let Some(pb) = ingress.get(EMBEDDINGS_BODY_KEY) {
            if let Some(inp) = pb.get("input") {
                obj.insert("input".into(), inp.clone());
            }
        }

        if let Some(dims) = ingress.get("__emb_dimensions") {
            obj.insert("dimensions".into(), dims.clone());
        }
        if let Some(ef) = ingress.get("__emb_encoding_format") {
            obj.insert("encoding_format".into(), ef.clone());
        }
        if let Some(user) = ingress.get("__emb_user") {
            obj.insert("user".into(), user.clone());
        }

        Ok((Value::Object(obj), HeaderMap::new()))
    }

    fn egress_path(&self, _model: &str, _stream: bool) -> String {
        "/v1/embeddings".to_string()
    }
}

struct EmbeddingsResponseParser;

impl ResponseDecoder for EmbeddingsResponseParser {
    fn parse_response(&self, _resp: Value) -> anyhow::Result<crate::protocol::ir::AiResponse> {
        unreachable!(
            "embeddings_proxy bypasses the codec pipeline; \
             revisit codec/openai/embeddings.rs before routing through it"
        )
    }
}

struct EmbeddingsResponseFormatter;

impl ResponseEncoder for EmbeddingsResponseFormatter {
    fn format_response(&self, _resp: &crate::protocol::ir::AiResponse) -> Value {
        unreachable!(
            "embeddings_proxy bypasses the codec pipeline; \
             revisit codec/openai/embeddings.rs before routing through it"
        )
    }
}

struct EmbeddingsStreamParser;

impl StreamResponseDecoder for EmbeddingsStreamParser {
    fn parse_chunk(
        &mut self,
        _raw: &str,
    ) -> anyhow::Result<Vec<crate::protocol::ir::AiStreamDelta>> {
        unreachable!("embeddings has streaming=false; check capabilities before calling")
    }
    fn finish(&mut self) -> anyhow::Result<Vec<crate::protocol::ir::AiStreamDelta>> {
        unreachable!("embeddings has streaming=false; check capabilities before calling")
    }
}

struct EmbeddingsStreamFormatter;

impl StreamResponseEncoder for EmbeddingsStreamFormatter {
    fn format_deltas(&mut self, _deltas: &[crate::protocol::ir::AiStreamDelta]) -> Vec<SseEvent> {
        unreachable!("embeddings has streaming=false; check capabilities before calling")
    }
    fn format_done(&mut self) -> Vec<SseEvent> {
        unreachable!("embeddings has streaming=false; check capabilities before calling")
    }
    fn usage(&self) -> Usage {
        Usage::default()
    }
}

/// Pull `usage.prompt_tokens` out of an OpenAI embeddings response.
/// Shared with `proxy::handler::embeddings_proxy` so the passthrough
/// path and any future codec route agree on accounting.
pub fn parse_usage(payload: &Value) -> Usage {
    let prompt = payload
        .get("usage")
        .and_then(|u| u.get("prompt_tokens"))
        .and_then(|v| v.as_i64())
        .unwrap_or(0);
    Usage {
        prompt_tokens: prompt.max(0) as u32,
        ..Usage::default()
    }
}
