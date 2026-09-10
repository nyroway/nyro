//! Opt-in same-protocol envelopes. Vendor payloads stay private to the runtime;
//! crossing a protocol boundary still requires the original strict codec.
use super::Failure;
use crate::{
    Request, Usage,
    codec::{
        ChatFormat, anthropic as anthropic_codec, gemini as gemini_codec, openai as openai_codec,
    },
};
mod anthropic;
mod gemini;
mod openai;
mod responses;
use nyro_protocol::framing::Event;
use serde_json::{Value, json};

// Preserve native detail extensions, but validate the known disjoint cache
// subsets when the upstream reports a write count.
fn validate_cache_write(details: &Value, input: u64) -> Result<(), Failure> {
    if let Some(written) = details.get("cache_write_tokens") {
        let written = written.as_u64().ok_or_else(Failure::upstream)?;
        let read = details
            .get("cached_tokens")
            .map_or(Ok(0), |v| v.as_u64().ok_or_else(Failure::upstream))?;
        if read.checked_add(written).is_none_or(|n| n > input) {
            return Err(Failure::upstream());
        }
    }
    Ok(())
}

pub(super) enum Input {
    Typed(Request),
    Native(NativeRequest),
}
impl Input {
    pub(super) fn model(&self) -> &str {
        match self {
            Self::Typed(request) => request.model(),
            Self::Native(request) => &request.model,
        }
    }
    pub(super) fn is_streaming(&self) -> bool {
        match self {
            Self::Typed(request) => request.is_streaming(),
            Self::Native(request) => request.streaming,
        }
    }
    pub(super) fn include_usage(&self) -> bool {
        match self {
            Self::Typed(Request::Chat(chat)) => chat
                .openai
                .stream_options
                .as_ref()
                .is_some_and(|options| options.include_usage == Some(true)),
            Self::Typed(_) => false,
            Self::Native(request) => request.include_usage,
        }
    }
    pub(super) fn native_for(&self, format: ChatFormat) -> bool {
        matches!(self, Self::Native(request) if request.format == format)
    }
    pub(super) fn typed(&self) -> Option<Request> {
        match self {
            Self::Typed(request) => Some(request.clone()),
            Self::Native(request) => match request.format {
                ChatFormat::Anthropic => anthropic_codec::decode_chat(request.value.clone()),
                ChatFormat::Gemini => gemini_codec::decode_chat(
                    request.value.clone(),
                    &request.model,
                    request.streaming,
                ),
                ChatFormat::OpenAiResponses => {
                    openai_codec::responses::decode_chat(request.value.clone())
                }
                ChatFormat::OpenAiChat => openai_codec::decode_chat(request.value.clone()),
            }
            .ok()
            .map(Request::Chat),
        }
    }
}

pub(super) struct NativeRequest {
    pub(super) format: ChatFormat,
    value: Value,
    model: String,
    streaming: bool,
    include_usage: bool,
}
impl NativeRequest {
    pub(super) fn parse(
        value: Value,
        endpoint: &super::endpoint::Endpoint,
    ) -> Result<Self, Failure> {
        let format = endpoint.format;
        if format == ChatFormat::Gemini {
            gemini::validate_request(&value)?;
            return Ok(Self {
                format,
                value,
                model: endpoint.model.clone().expect("Gemini path model"),
                streaming: endpoint.streaming,
                include_usage: false,
            });
        }
        let invalid = || Failure::invalid("Invalid native Chat request envelope");
        let model = value
            .get("model")
            .and_then(Value::as_str)
            .filter(|model| !model.is_empty())
            .ok_or_else(invalid)?
            .to_owned();
        let streaming = optional_bool(&value["stream"]).ok_or_else(invalid)?;
        if format == ChatFormat::OpenAiResponses {
            responses::validate_request(&value, streaming)?;
            return Ok(Self {
                format,
                value,
                model,
                streaming,
                include_usage: false,
            });
        }
        let messages = value
            .get("messages")
            .and_then(Value::as_array)
            .filter(|messages| !messages.is_empty())
            .ok_or_else(invalid)?;
        if messages.iter().any(|message| {
            message
                .get("role")
                .and_then(Value::as_str)
                .is_none_or(str::is_empty)
        }) {
            return Err(invalid());
        }
        let streaming = optional_bool(&value["stream"]).ok_or_else(invalid)?;
        let include_usage = match format {
            ChatFormat::OpenAiChat => {
                let options = &value["stream_options"];
                if !options.is_null() && !options.is_object() {
                    return Err(invalid());
                }
                optional_bool(&options["include_obfuscation"]).ok_or_else(invalid)?;
                optional_bool(&options["include_usage"]).ok_or_else(invalid)?
            }
            ChatFormat::Anthropic => {
                anthropic::validate_request(&value)?;
                false
            }
            _ => return Err(invalid()),
        };
        Ok(Self {
            format,
            value,
            model,
            streaming,
            include_usage,
        })
    }
    pub(super) fn encode(&self, model: &str) -> Value {
        let mut value = self.value.clone();
        if self.format != ChatFormat::Gemini {
            value["model"] = json!(model);
        }
        if self.format == ChatFormat::OpenAiResponses {
            value["store"] = json!(false);
        }
        if self.streaming && self.format == ChatFormat::OpenAiChat {
            if value["stream_options"].is_null() {
                value["stream_options"] = json!({});
            }
            value["stream_options"]["include_usage"] = json!(true);
        }
        value
    }
}
fn optional_bool(value: &Value) -> Option<bool> {
    if value.is_null() {
        Some(false)
    } else {
        value.as_bool()
    }
}

pub(super) struct Frame {
    pub output: String,
    pub usage: Option<Usage>,
    pub done: bool,
}

pub(super) fn response(
    value: Value,
    model: &str,
    format: ChatFormat,
) -> Result<(Value, Option<Usage>), Failure> {
    match format {
        ChatFormat::OpenAiChat => openai::response(value, model, false),
        ChatFormat::Anthropic => {
            anthropic::response(value, model).map(|(value, usage)| (value, Some(usage)))
        }
        ChatFormat::Gemini => gemini::response(value),
        ChatFormat::OpenAiResponses => responses::response(value, model),
    }
}

pub(super) enum Stream {
    Gemini(gemini::StreamDecoder),
    Responses {
        model: String,
        decoder: responses::StreamDecoder,
    },
    Openai {
        model: String,
        include_usage: bool,
    },
    Anthropic {
        model: String,
        decoder: anthropic::StreamDecoder,
    },
}
impl Stream {
    pub(super) fn new(
        format: ChatFormat,
        model: String,
        include_usage: bool,
    ) -> Result<Self, Failure> {
        match format {
            ChatFormat::Gemini => Ok(Self::Gemini(gemini::StreamDecoder::default())),
            ChatFormat::OpenAiChat => Ok(Self::Openai {
                model,
                include_usage,
            }),
            ChatFormat::Anthropic => Ok(Self::Anthropic {
                model,
                decoder: anthropic::StreamDecoder::default(),
            }),
            ChatFormat::OpenAiResponses => Ok(Self::Responses {
                model,
                decoder: responses::StreamDecoder::default(),
            }),
        }
    }
    pub(super) fn finish(&self) -> Result<(), Failure> {
        match self {
            Self::Gemini(decoder) => decoder.finish(),
            Self::Responses { decoder, .. } => decoder.finish(),
            _ => Err(Failure::upstream()),
        }
    }
    pub(super) fn push(&mut self, event: Event) -> Result<Frame, Failure> {
        match self {
            Self::Gemini(decoder) => decoder.push(event),
            Self::Responses { model, decoder } => decoder.push(event, model),
            Self::Openai {
                model,
                include_usage,
            } => openai::frame(event, model, *include_usage),
            Self::Anthropic { model, decoder } => decoder.push(event, model),
        }
    }
}
