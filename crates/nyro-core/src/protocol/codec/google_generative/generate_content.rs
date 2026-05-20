//! Google Generative AI (`POST /v1beta/models/:model:generateContent`).
//!
//! Wire version `v1beta` matches Google's URL versioning.
//!
//! `override_model_in_body` is true: the encoder embeds the actual model name
//! in the request body / URL path rather than a top-level `model` field.

use crate::protocol::ids::{
    EndpointCapabilities, GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA, ProtocolEndpoint,
};
use crate::protocol::registry::EndpointRegistration;
use crate::protocol::traits::*;

pub struct GoogleGenerateContentV1Beta;

const CAPS: EndpointCapabilities = EndpointCapabilities {
    streaming: true,
    tools: true,
    reasoning: true,
    embeddings: false,
    force_upstream_stream: false,
    override_model_in_body: true,
    ingress_routes: &[("POST", "/v1beta/models/:model_action")],
    ..EndpointCapabilities::CHAT_STANDARD
};

impl EndpointHandler for GoogleGenerateContentV1Beta {
    fn id(&self) -> ProtocolEndpoint {
        GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA
    }
    fn capabilities(&self) -> &'static EndpointCapabilities {
        &CAPS
    }
    fn make_request_decoder(&self) -> Box<dyn RequestDecoder + Send> {
        Box::new(super::decoder::GoogleDecoder)
    }
    fn make_request_encoder(&self) -> Box<dyn RequestEncoder + Send> {
        Box::new(super::encoder::GoogleEncoder)
    }
    fn make_response_decoder(&self) -> Box<dyn ResponseDecoder> {
        Box::new(super::stream::GoogleResponseParser)
    }
    fn make_response_encoder(&self) -> Box<dyn ResponseEncoder> {
        Box::new(super::stream::GoogleResponseFormatter)
    }
    fn make_stream_response_decoder(&self) -> Box<dyn StreamResponseDecoder> {
        Box::new(super::stream::GoogleStreamParser::new())
    }
    fn make_stream_response_encoder(&self) -> Box<dyn StreamResponseEncoder> {
        Box::new(super::stream::GoogleStreamFormatter::new())
    }
}

inventory::submit! {
    EndpointRegistration { make: || Box::new(GoogleGenerateContentV1Beta) }
}
