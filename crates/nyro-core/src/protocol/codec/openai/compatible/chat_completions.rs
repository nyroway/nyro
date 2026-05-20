//! OpenAI Chat Completions API (`POST /v1/chat/completions`).
//!
//! `EndpointHandler` registration shell — wraps
//! [`super::decoder`], [`super::encoder`], and [`super::stream`] codecs.

use crate::protocol::ids::{
    EndpointCapabilities, OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1, ProtocolEndpoint,
};
use crate::protocol::registry::EndpointRegistration;
use crate::protocol::traits::*;

pub struct OpenAIChatCompletionsV1;

const CAPS: EndpointCapabilities = EndpointCapabilities {
    streaming: true,
    tools: true,
    reasoning: true,
    embeddings: false,
    force_upstream_stream: false,
    override_model_in_body: false,
    ingress_routes: &[("POST", "/v1/chat/completions")],
    ..EndpointCapabilities::CHAT_STANDARD
};

impl EndpointHandler for OpenAIChatCompletionsV1 {
    fn id(&self) -> ProtocolEndpoint {
        OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1
    }
    fn capabilities(&self) -> &'static EndpointCapabilities {
        &CAPS
    }
    fn make_request_decoder(&self) -> Box<dyn RequestDecoder + Send> {
        Box::new(super::decoder::OpenAIDecoder)
    }
    fn make_request_encoder(&self) -> Box<dyn RequestEncoder + Send> {
        Box::new(super::encoder::OpenAIEncoder)
    }
    fn make_response_decoder(&self) -> Box<dyn ResponseDecoder> {
        Box::new(super::stream::OpenAIResponseParser)
    }
    fn make_response_encoder(&self) -> Box<dyn ResponseEncoder> {
        Box::new(super::stream::OpenAIResponseFormatter)
    }
    fn make_stream_response_decoder(&self) -> Box<dyn StreamResponseDecoder> {
        Box::new(super::stream::OpenAIStreamParser::new())
    }
    fn make_stream_response_encoder(&self) -> Box<dyn StreamResponseEncoder> {
        Box::new(super::stream::OpenAIStreamFormatter::new())
    }
}

inventory::submit! {
    EndpointRegistration { make: || Box::new(OpenAIChatCompletionsV1) }
}
