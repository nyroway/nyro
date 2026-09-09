use super::Failure;
use crate::{
    ChatEvent,
    codec::{ChatFormat, CodecError, anthropic, gemini, openai},
    observation::AttemptObservation,
};
use bytes::Bytes;
use futures::{StreamExt, stream::BoxStream};
use nyro_protocol::framing::{Decoder, Event};
use std::collections::VecDeque;

enum Decode {
    Openai { done: bool },
    Responses(openai::responses::StreamDecoder),
    Anthropic(anthropic::StreamDecoder),
    Gemini(gemini::StreamDecoder),
}
impl Decode {
    fn push(&mut self, event: &Event) -> Result<Vec<ChatEvent>, CodecError> {
        match self {
            Self::Openai { done } => {
                if *done {
                    return Err(CodecError("event after stream completion".into()));
                }
                let event = openai::decode_chat_event(&event.data)?;
                *done = event.is_done();
                Ok(vec![event])
            }
            Self::Responses(decoder) => decoder.push(event),
            Self::Anthropic(decoder) => decoder.push(event),
            Self::Gemini(decoder) => decoder.push(event),
        }
    }
    fn finish(&mut self) -> Result<Vec<ChatEvent>, CodecError> {
        match self {
            Self::Openai { done: true } => Ok(vec![]),
            Self::Openai { done: false } => Err(CodecError("missing stream completion".into())),
            Self::Responses(decoder) => decoder.finish(),
            Self::Anthropic(decoder) => decoder.finish(),
            Self::Gemini(decoder) => decoder.finish(),
        }
    }
}
enum Encode {
    Openai { model: String, include_usage: bool },
    Responses(openai::responses::StreamEncoder),
    Anthropic(anthropic::StreamEncoder),
    Gemini(gemini::StreamEncoder),
}
impl Encode {
    fn push(&mut self, event: &ChatEvent) -> Result<String, CodecError> {
        match self {
            Self::Openai {
                model,
                include_usage,
            } => {
                if !*include_usage && let ChatEvent::Chunk(chunk) = event {
                    if chunk.choices.is_empty() {
                        return Ok(String::new());
                    }
                    let mut chunk = chunk.clone();
                    chunk.usage = None;
                    return openai::encode_chat_event(&ChatEvent::Chunk(chunk), model);
                }
                openai::encode_chat_event(event, model)
            }
            Self::Responses(encoder) => encoder.push(event),
            Self::Anthropic(encoder) => encoder.push(event),
            Self::Gemini(encoder) => encoder.push(event),
        }
    }
}

pub(super) struct StreamState {
    input: BoxStream<'static, Result<Bytes, reqwest::Error>>,
    pending: Bytes,
    framing: Decoder,
    events: VecDeque<Event>,
    canonical: VecDeque<ChatEvent>,
    decoder: Decode,
    encoder: Encode,
    done: bool,
    eof: bool,
    max_bytes: usize,
    attempt: Option<AttemptObservation>,
}
impl StreamState {
    pub(super) fn new(
        response: reqwest::Response,
        upstream: ChatFormat,
        downstream: ChatFormat,
        model: String,
        max_bytes: usize,
        include_usage: bool,
        attempt: Option<AttemptObservation>,
    ) -> Self {
        let decoder = match upstream {
            ChatFormat::OpenAiChat => Decode::Openai { done: false },
            ChatFormat::OpenAiResponses => {
                Decode::Responses(openai::responses::StreamDecoder::with_limit(max_bytes))
            }
            ChatFormat::Anthropic => {
                Decode::Anthropic(anthropic::StreamDecoder::with_limit(max_bytes))
            }
            ChatFormat::Gemini => Decode::Gemini(gemini::StreamDecoder::with_limit(max_bytes)),
        };
        let encoder = match downstream {
            ChatFormat::OpenAiResponses => Encode::Responses(
                openai::responses::StreamEncoder::with_limit(model, max_bytes),
            ),
            ChatFormat::OpenAiChat => Encode::Openai {
                model,
                include_usage,
            },
            ChatFormat::Anthropic => {
                Encode::Anthropic(anthropic::StreamEncoder::with_limit(model, max_bytes))
            }
            ChatFormat::Gemini => {
                Encode::Gemini(gemini::StreamEncoder::with_limit(model, max_bytes))
            }
        };
        Self {
            input: response.bytes_stream().boxed(),
            pending: Bytes::new(),
            framing: Decoder::new(max_bytes),
            events: VecDeque::new(),
            canonical: VecDeque::new(),
            decoder,
            encoder,
            done: false,
            eof: false,
            max_bytes,
            attempt,
        }
    }
    pub(super) async fn next_frame(&mut self) -> Result<Option<String>, Failure> {
        let result = self.next_frame_inner().await;
        if result.is_err()
            && let Some(attempt) = self.attempt.as_mut()
        {
            attempt.fail("protocol_error");
        }
        result
    }

    async fn next_frame_inner(&mut self) -> Result<Option<String>, Failure> {
        loop {
            if let Some(event) = self.canonical.pop_front() {
                if let Some(attempt) = self.attempt.as_mut()
                    && let ChatEvent::Chunk(chunk) = &event
                    && let Some(usage) = chunk.usage.as_ref()
                {
                    attempt.observe(usage).map_err(|_| Failure::upstream())?;
                }
                self.done = event.is_done();
                if self.done
                    && let Some(mut attempt) = self.attempt.take()
                {
                    attempt.complete();
                }
                let output = self.encoder.push(&event).map_err(|_| Failure::upstream())?;
                if output.len() > self.max_bytes {
                    return Err(Failure::upstream());
                }
                if !output.is_empty() {
                    return Ok(Some(output));
                }
                continue;
            }
            if self.done {
                return Ok(None);
            }
            if let Some(event) = self.events.pop_front() {
                self.canonical
                    .extend(self.decoder.push(&event).map_err(|_| Failure::upstream())?);
                continue;
            }
            if self.eof {
                self.canonical
                    .extend(self.decoder.finish().map_err(|_| Failure::upstream())?);
                if self.canonical.is_empty() {
                    return Err(Failure::upstream());
                }
                continue;
            }
            if !self.pending.is_empty() {
                let bytes = self.pending.split_to(self.pending.len().min(8192));
                self.events
                    .extend(self.framing.push(&bytes).map_err(|_| Failure::upstream())?);
                continue;
            }
            match self.input.next().await {
                Some(Ok(bytes)) => self.pending = bytes,
                Some(Err(_)) => {
                    if let Some(attempt) = self.attempt.as_mut() {
                        attempt.fail("transport_error");
                    }
                    return Err(Failure::upstream());
                }
                None => {
                    self.eof = true;
                    self.events
                        .extend(self.framing.finish().map_err(|_| Failure::upstream())?);
                }
            }
        }
    }
}
