//! Response conversion owns an accepted upstream; it never dispatches another attempt.
use super::{Failure, Runtime, endpoint::Endpoint, json_response, stream::StreamState};
use crate::{
    Request, Workload,
    codec::{ChatFormat, anthropic, gemini, openai},
    health::Attempt,
    provider::Driver,
    quota::AttemptQuota,
};
use axum::{
    body::Body,
    http::{Response as HttpResponse, StatusCode},
};
use futures::StreamExt;
use serde_json::Value;

impl Runtime {
    pub(super) async fn decode_response(
        &self,
        response: reqwest::Response,
        provider: &Driver,
        endpoint: &Endpoint,
        request: &Request,
        health: &mut Option<Attempt>,
        quota: &mut Option<AttemptQuota>,
    ) -> Result<HttpResponse<Body>, Failure> {
        let streaming = request.is_streaming();
        let public_model = request.model().to_owned();
        let workload = endpoint.workload;
        let include_usage = matches!(request, Request::Chat(chat) if chat.openai.stream_options.as_ref().is_some_and(|options| options.include_usage == Some(true)));
        if streaming {
            let is_sse = response
                .headers()
                .get("content-type")
                .and_then(|v| v.to_str().ok())
                .is_some_and(|v| {
                    v.split(';')
                        .next()
                        .is_some_and(|v| v.trim().eq_ignore_ascii_case("text/event-stream"))
                });
            if !is_sse {
                return Err(Failure::upstream());
            }
            let mut state = StreamState::new(
                response,
                provider.format,
                endpoint.format,
                public_model,
                self.options.max_frame_bytes,
                include_usage,
                quota.take(),
            );
            // Validate one complete frame before handing the response to HTTP. This is not a flush acknowledgement.
            let first = state.next_frame().await?.ok_or_else(Failure::upstream)?;
            let health = health.take();
            let output = futures::stream::once(async { Ok::<_, Failure>(first) }).chain(
                futures::stream::try_unfold((state, health), |(mut state, health)| async move {
                    match state.next_frame().await {
                        Ok(Some(frame)) => Ok(Some((frame, (state, health)))),
                        Ok(None) => {
                            if let Some(health) = health {
                                health.success();
                            }
                            Ok(None)
                        }
                        Err(error) => {
                            if let Some(health) = health {
                                health.failure();
                            }
                            Err(error)
                        }
                    }
                }),
            );
            return Ok(HttpResponse::builder()
                .header("content-type", "text/event-stream")
                .header("cache-control", "no-cache")
                .body(Body::from_stream(output))
                .expect("static response headers"));
        }
        let mut input = response.bytes_stream();
        let mut bytes = Vec::new();
        while let Some(chunk) = input.next().await {
            let chunk = chunk.map_err(|_| Failure::upstream())?;
            if chunk.len() > self.options.max_response_bytes.saturating_sub(bytes.len()) {
                return Err(Failure::upstream());
            }
            bytes.extend_from_slice(&chunk);
        }
        let payload: Value = serde_json::from_slice(&bytes).map_err(|_| Failure::upstream())?;
        let payload = match workload {
            Workload::Chat => {
                let mut response = match provider.format {
                    ChatFormat::OpenAiResponses => openai::responses::decode_chat_response(payload),
                    ChatFormat::OpenAiChat => openai::decode_chat_response(payload),
                    ChatFormat::Anthropic => anthropic::decode_chat_response(payload),
                    ChatFormat::Gemini => gemini::decode_chat_response(payload),
                }
                .map_err(|_| Failure::upstream())?;
                if let Some(quota) = quota.as_mut()
                    && let Some(usage) = response.usage.as_ref()
                {
                    quota.observe(usage).map_err(|_| Failure::upstream())?;
                }
                if let Some(quota) = quota.take() {
                    quota.complete();
                }
                response.model = public_model;
                match endpoint.format {
                    ChatFormat::OpenAiResponses => {
                        openai::responses::encode_chat_response(&response)
                    }
                    ChatFormat::OpenAiChat => openai::encode_chat_response(&response),
                    ChatFormat::Anthropic => anthropic::encode_chat_response(&response),
                    ChatFormat::Gemini => gemini::encode_chat_response(&response),
                }
            }
            Workload::Embedding => {
                let mut response =
                    openai::decode_embedding_response(payload).map_err(|_| Failure::upstream())?;
                if let Some(quota) = quota.as_mut() {
                    quota
                        .observe_embedding(&response.usage)
                        .map_err(|_| Failure::upstream())?;
                }
                if let Some(quota) = quota.take() {
                    quota.complete();
                }
                response.model = public_model;
                openai::encode_embedding_response(&response)
            }
        }
        .map_err(|_| Failure::upstream())?;
        Ok(json_response(StatusCode::OK, payload))
    }
}
