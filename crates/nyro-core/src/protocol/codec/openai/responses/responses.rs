//! OpenAI Responses API (`POST /v1/responses`).
//!
//! `force_upstream_stream` is true: the upstream call is always made in
//! streaming mode regardless of the client's `stream` flag.

use crate::protocol::ids::{EndpointCapabilities, OPENAI_RESPONSES_V1, ProtocolEndpoint};
use crate::protocol::registry::EndpointRegistration;
use crate::protocol::traits::*;

pub struct OpenAIResponsesV1;

const CAPS: EndpointCapabilities = EndpointCapabilities {
    streaming: true,
    tools: true,
    reasoning: true,
    embeddings: false,
    force_upstream_stream: true,
    override_model_in_body: false,
    ingress_routes: &[("POST", "/v1/responses")],
    ..EndpointCapabilities::CHAT_STANDARD
};

impl EndpointHandler for OpenAIResponsesV1 {
    fn id(&self) -> ProtocolEndpoint {
        OPENAI_RESPONSES_V1
    }
    fn capabilities(&self) -> &'static EndpointCapabilities {
        &CAPS
    }
    fn make_request_decoder(&self) -> Box<dyn RequestDecoder + Send> {
        Box::new(super::decoder::ResponsesDecoder)
    }
    fn make_request_encoder(&self) -> Box<dyn RequestEncoder + Send> {
        Box::new(super::encoder::ResponsesEncoder)
    }
    fn make_response_decoder(&self) -> Box<dyn ResponseDecoder> {
        Box::new(super::parser::ResponsesResponseParser)
    }
    fn make_response_encoder(&self) -> Box<dyn ResponseEncoder> {
        Box::new(super::formatter::ResponsesResponseFormatter)
    }
    fn make_stream_response_decoder(&self) -> Box<dyn StreamResponseDecoder> {
        Box::new(super::parser::ResponsesStreamParser::new())
    }
    fn make_stream_response_encoder(&self) -> Box<dyn StreamResponseEncoder> {
        Box::new(super::stream::ResponsesStreamFormatter::new())
    }
}

inventory::submit! {
    EndpointRegistration { make: || Box::new(OpenAIResponsesV1) }
}
