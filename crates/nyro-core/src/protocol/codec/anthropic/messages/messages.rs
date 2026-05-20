//! Anthropic Messages API (`POST /v1/messages`).
//!
//! Wire version is the schema date `2023-06-01` (the `anthropic-version` header
//! the API requires), not the URL prefix `v1`.

use crate::protocol::ids::{ANTHROPIC_MESSAGES_2023_06_01, EndpointCapabilities, ProtocolEndpoint};
use crate::protocol::registry::EndpointRegistration;
use crate::protocol::traits::*;

pub struct AnthropicMessages2023;

const CAPS: EndpointCapabilities = EndpointCapabilities {
    streaming: true,
    tools: true,
    reasoning: true,
    embeddings: false,
    force_upstream_stream: false,
    override_model_in_body: false,
    ingress_routes: &[("POST", "/v1/messages")],
    extended_reasoning: true,
    ..EndpointCapabilities::CHAT_STANDARD
};

impl EndpointHandler for AnthropicMessages2023 {
    fn id(&self) -> ProtocolEndpoint {
        ANTHROPIC_MESSAGES_2023_06_01
    }
    fn capabilities(&self) -> &'static EndpointCapabilities {
        &CAPS
    }
    fn make_request_decoder(&self) -> Box<dyn RequestDecoder + Send> {
        Box::new(super::decoder::AnthropicDecoder)
    }
    fn make_request_encoder(&self) -> Box<dyn RequestEncoder + Send> {
        Box::new(super::encoder::AnthropicEncoder)
    }
    fn make_response_decoder(&self) -> Box<dyn ResponseDecoder> {
        Box::new(super::stream::AnthropicResponseParser)
    }
    fn make_response_encoder(&self) -> Box<dyn ResponseEncoder> {
        Box::new(super::stream::AnthropicResponseFormatter)
    }
    fn make_stream_response_decoder(&self) -> Box<dyn StreamResponseDecoder> {
        Box::new(super::stream::AnthropicStreamParser::new())
    }
    fn make_stream_response_encoder(&self) -> Box<dyn StreamResponseEncoder> {
        Box::new(super::stream::AnthropicStreamFormatter::new())
    }
}

inventory::submit! {
    EndpointRegistration { make: || Box::new(AnthropicMessages2023) }
}
