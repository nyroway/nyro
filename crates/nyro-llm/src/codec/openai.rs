//! Explicit OpenAI wire ↔ typed workload conversion; unsupported fields are rejected.
pub use super::CodecError;
pub mod responses;
use crate::ir::*;
use nyro_protocol::openai::{chat, embedding, stream};
use serde::{Serialize, de::DeserializeOwned};
use serde_json::Value;
fn invalid(message: &str) -> CodecError {
    CodecError(message.into())
}
// Serde maps structurally identical typed leaves, never stores an opaque request.
fn convert<T: Serialize, U: DeserializeOwned>(value: T) -> Result<U, CodecError> {
    Ok(serde_json::from_value(serde_json::to_value(value)?)?)
}
fn model(value: &str) -> Result<(), CodecError> {
    if value.trim().is_empty() {
        Err(invalid("model must be nonempty"))
    } else {
        Ok(())
    }
}
pub fn decode_chat(value: Value) -> Result<ChatRequest, CodecError> {
    let wire: chat::Request = serde_json::from_value(value)?;
    let request = ChatRequest {
        model: wire.model,
        messages: convert(wire.messages)?,
        stream: wire.stream,
        generation: Generation {
            temperature: wire.temperature,
            max_tokens: wire.max_tokens,
            max_completion_tokens: wire.max_completion_tokens,
            top_p: wire.top_p,
            frequency_penalty: wire.frequency_penalty,
            presence_penalty: wire.presence_penalty,
            seed: wire.seed,
            stop: wire.stop,
            n: wire.n,
            logit_bias: wire.logit_bias,
        },
        openai: Box::new(OpenAiOptions {
            tools: wire.tools,
            tool_choice: wire.tool_choice,
            parallel_tool_calls: wire.parallel_tool_calls,
            stream_options: wire.stream_options,
            response_format: wire.response_format,
            modalities: wire.modalities,
            audio: wire.audio,
            prediction: wire.prediction,
            reasoning_effort: wire.reasoning_effort,
            service_tier: wire.service_tier,
            user: wire.user,
            store: wire.store,
            metadata: wire.metadata,
            safety_identifier: wire.safety_identifier,
            prompt_cache_key: wire.prompt_cache_key,
        }),
    };
    validate_chat(&request)?;
    Ok(request)
}
fn validate_chat(request: &ChatRequest) -> Result<(), CodecError> {
    model(&request.model)?;
    if request.messages.is_empty() {
        return Err(invalid("messages must be nonempty"));
    }
    for message in &request.messages {
        if message.role == Role::Tool && message.tool_call_id.as_deref().is_none_or(str::is_empty) {
            return Err(invalid("tool messages require tool_call_id"));
        }
        if message.content.is_none()
            && message.tool_calls.as_ref().is_none_or(Vec::is_empty)
            && message.refusal.is_none()
            && message.audio.is_none()
        {
            return Err(invalid(
                "message requires content, tool calls, refusal or audio",
            ));
        }
        if message.tool_calls.is_some() && message.role != Role::Assistant {
            return Err(invalid("tool calls require assistant role"));
        }
    }
    let g = &request.generation;
    if g.temperature
        .is_some_and(|v| !v.is_finite() || !(0.0..=2.0).contains(&v))
        || g.top_p
            .is_some_and(|v| !v.is_finite() || !(0.0..=1.0).contains(&v))
        || [g.frequency_penalty, g.presence_penalty]
            .into_iter()
            .flatten()
            .any(|v| !v.is_finite() || !(-2.0..=2.0).contains(&v))
    {
        return Err(invalid("generation parameter out of range"));
    }
    if [g.max_tokens, g.max_completion_tokens, g.n].contains(&Some(0)) {
        return Err(invalid("token and choice limits must be positive"));
    }
    if request.openai.stream_options.is_some() && !request.stream.unwrap_or(false) {
        return Err(invalid("stream_options requires streaming"));
    }
    Ok(())
}
pub fn encode_chat(request: &ChatRequest) -> Result<Value, CodecError> {
    validate_chat(request)?;
    let wire = chat::Request {
        model: request.model.clone(),
        messages: convert(&request.messages)?,
        stream: request.stream,
        temperature: request.generation.temperature,
        max_tokens: request.generation.max_tokens,
        max_completion_tokens: request.generation.max_completion_tokens,
        top_p: request.generation.top_p,
        frequency_penalty: request.generation.frequency_penalty,
        presence_penalty: request.generation.presence_penalty,
        seed: request.generation.seed,
        stop: request.generation.stop.clone(),
        n: request.generation.n,
        logit_bias: request.generation.logit_bias.clone(),
        tools: request.openai.tools.clone(),
        tool_choice: request.openai.tool_choice.clone(),
        parallel_tool_calls: request.openai.parallel_tool_calls,
        stream_options: request.openai.stream_options.clone(),
        response_format: request.openai.response_format.clone(),
        modalities: request.openai.modalities.clone(),
        audio: request.openai.audio.clone(),
        prediction: request.openai.prediction.clone(),
        reasoning_effort: request.openai.reasoning_effort.clone(),
        service_tier: request.openai.service_tier.clone(),
        user: request.openai.user.clone(),
        store: request.openai.store,
        metadata: request.openai.metadata.clone(),
        safety_identifier: request.openai.safety_identifier.clone(),
        prompt_cache_key: request.openai.prompt_cache_key.clone(),
    };
    Ok(serde_json::to_value(wire)?)
}
pub fn decode_embedding(value: Value) -> Result<EmbeddingRequest, CodecError> {
    let wire: embedding::Request = serde_json::from_value(value)?;
    let request: EmbeddingRequest = convert(wire)?;
    validate_embedding(&request)?;
    Ok(request)
}
fn validate_embedding(request: &EmbeddingRequest) -> Result<(), CodecError> {
    model(&request.model)?;
    let empty = match &request.input {
        Input::Text(s) => s.is_empty(),
        Input::Texts(v) => v.is_empty() || v.iter().any(String::is_empty),
        Input::Tokens(v) => v.is_empty(),
        Input::TokenBatches(v) => v.is_empty() || v.iter().any(Vec::is_empty),
    };
    if empty || request.dimensions == Some(0) {
        return Err(invalid("embedding input and dimensions must be nonempty"));
    }
    Ok(())
}
pub fn encode_embedding(request: &EmbeddingRequest) -> Result<Value, CodecError> {
    validate_embedding(request)?;
    let wire: embedding::Request = convert(request)?;
    Ok(serde_json::to_value(wire)?)
}
pub fn decode_chat_response(value: Value) -> Result<ChatResponse, CodecError> {
    let wire: chat::Response = serde_json::from_value(value)?;
    if wire.object != "chat.completion" {
        return Err(invalid("expected chat.completion object"));
    }
    if wire
        .choices
        .iter()
        .any(|c| c.message.role != chat::Role::Assistant)
    {
        return Err(invalid("response messages require assistant role"));
    }
    convert(wire)
}
pub fn encode_chat_response(response: &ChatResponse) -> Result<Value, CodecError> {
    let wire: chat::Response = convert(response)?;
    Ok(serde_json::to_value(wire)?)
}
pub fn decode_embedding_response(value: Value) -> Result<EmbeddingResponse, CodecError> {
    let wire: embedding::Response = serde_json::from_value(value)?;
    if wire.object != "list" || wire.data.iter().any(|item| item.object != "embedding") {
        return Err(invalid("expected embedding list object"));
    }
    convert(wire)
}
pub fn encode_embedding_response(response: &EmbeddingResponse) -> Result<Value, CodecError> {
    let wire: embedding::Response = convert(response)?;
    Ok(serde_json::to_value(wire)?)
}
pub fn decode_chat_event(data: &str) -> Result<ChatEvent, CodecError> {
    if data.trim() == "[DONE]" {
        return Ok(ChatEvent::Done);
    }
    let wire: stream::Chunk = serde_json::from_str(data)?;
    if wire.object != "chat.completion.chunk" {
        return Err(invalid("expected chat.completion.chunk object"));
    }
    Ok(ChatEvent::Chunk(Box::new(convert(wire)?)))
}
pub fn encode_chat_event(event: &ChatEvent, public_model: &str) -> Result<String, CodecError> {
    match event {
        ChatEvent::Done => Ok("data: [DONE]\n\n".into()),
        ChatEvent::Chunk(chunk) => {
            let mut wire: stream::Chunk = convert(chunk)?;
            wire.model = public_model.into();
            Ok(format!("data: {}\n\n", serde_json::to_string(&wire)?))
        }
    }
}
