use super::Failure;
use crate::{
    Request, Workload,
    codec::{ChatFormat, anthropic, gemini, openai},
    config::ProviderKind,
};
use axum::http::{HeaderMap, Method, StatusCode, Uri};
use serde_json::Value;

pub(super) struct Endpoint {
    pub kind: ProviderKind,
    pub format: ChatFormat,
    pub workload: Workload,
    model: Option<String>,
    streaming: bool,
}

pub(super) fn kind(path: &str) -> ProviderKind {
    if path == "/v1/messages" {
        ProviderKind::Anthropic
    } else if path.starts_with("/v1beta/models/") || path.starts_with("/v1/models/") {
        ProviderKind::Gemini
    } else {
        ProviderKind::Openai
    }
}

impl Endpoint {
    pub(super) fn parse(uri: &Uri, method: &Method) -> Result<Self, Failure> {
        let path = uri.path();
        let kind = kind(path);
        let (workload, model, streaming) = match path {
            "/v1/chat/completions" | "/v1/responses" | "/v1/messages" => {
                (Workload::Chat, None, false)
            }
            "/v1/embeddings" => (Workload::Embedding, None, false),
            _ => {
                let model_path = path
                    .strip_prefix("/v1beta/models/")
                    .or_else(|| path.strip_prefix("/v1/models/"))
                    .ok_or_else(|| {
                        Failure::new(StatusCode::NOT_FOUND, "not_found", "Unknown endpoint")
                    })?;
                let (model, action) = model_path
                    .rsplit_once(':')
                    .ok_or_else(|| Failure::invalid("Invalid Gemini model endpoint"))?;
                if model.is_empty()
                    || model == "."
                    || model == ".."
                    || !model
                        .bytes()
                        .all(|b| b.is_ascii_alphanumeric() || b"-_.".contains(&b))
                {
                    return Err(Failure::invalid("Invalid Gemini model alias"));
                }
                let streaming = match action {
                    "generateContent" => false,
                    "streamGenerateContent" => true,
                    _ => {
                        return Err(Failure::new(
                            StatusCode::NOT_FOUND,
                            "not_found",
                            "Unknown endpoint",
                        ));
                    }
                };
                (Workload::Chat, Some(model.to_owned()), streaming)
            }
        };
        if method != Method::POST {
            return Err(Failure::new(
                StatusCode::METHOD_NOT_ALLOWED,
                "method_not_allowed",
                "POST is required",
            ));
        }
        validate_query(uri, kind, streaming)?;
        Ok(Self {
            kind,
            format: match kind {
                ProviderKind::Openai if path == "/v1/responses" => ChatFormat::OpenAiResponses,
                ProviderKind::Openai => ChatFormat::OpenAiChat,
                ProviderKind::Anthropic => ChatFormat::Anthropic,
                ProviderKind::Gemini => ChatFormat::Gemini,
            },
            workload,
            model,
            streaming,
        })
    }

    pub(super) fn decode(&self, value: Value) -> Result<Request, Failure> {
        let result = match (self.format, self.workload) {
            (ChatFormat::OpenAiChat, Workload::Embedding) => {
                openai::decode_embedding(value).map(Request::Embedding)
            }
            (ChatFormat::OpenAiChat, Workload::Chat) => {
                openai::decode_chat(value).map(Request::Chat)
            }
            (ChatFormat::OpenAiResponses, _) => {
                openai::responses::decode_chat(value).map(Request::Chat)
            }
            (ChatFormat::Anthropic, _) => anthropic::decode_chat(value).map(Request::Chat),
            (ChatFormat::Gemini, _) => gemini::decode_chat(
                value,
                self.model.as_deref().expect("Gemini path model"),
                self.streaming,
            )
            .map(Request::Chat),
        };
        result.map_err(|_| Failure::invalid("Invalid or unsupported request"))
    }
}

pub(super) fn credential(kind: ProviderKind, headers: &HeaderMap) -> Result<Option<&str>, Failure> {
    let names = ["authorization", "x-api-key", "x-goog-api-key"];
    let mut supplied = names
        .into_iter()
        .flat_map(|name| headers.get_all(name).iter().map(move |value| (name, value)));
    let Some((name, value)) = supplied.next() else {
        return Ok(None);
    };
    if supplied.next().is_some() {
        return Err(Failure::unauthorized());
    }
    let value = value.to_str().map_err(|_| Failure::unauthorized())?;
    let value = match name {
        "authorization" => {
            let (scheme, secret) = value.split_once(' ').ok_or_else(Failure::unauthorized)?;
            if !scheme.eq_ignore_ascii_case("bearer") {
                return Err(Failure::unauthorized());
            }
            secret
        }
        "x-api-key" if kind == ProviderKind::Anthropic => value,
        "x-goog-api-key" if kind == ProviderKind::Gemini => value,
        _ => return Err(Failure::unauthorized()),
    };
    if value.is_empty() || !value.bytes().all(|b| b.is_ascii_graphic()) {
        return Err(Failure::unauthorized());
    }
    Ok(Some(value))
}

pub(super) fn validate_query(
    uri: &Uri,
    kind: ProviderKind,
    streaming: bool,
) -> Result<(), Failure> {
    // Parse percent-encoded query keys too; credentials are accepted only in headers.
    if let Some(query) = uri.query() {
        let url = reqwest::Url::parse(&format!("http://localhost/?{query}"))
            .map_err(|_| Failure::invalid("Invalid query"))?;
        let mut alt = false;
        for (key, value) in url.query_pairs() {
            if key == "key" || key == "api_key" || key == "access_token" {
                return Err(Failure::unauthorized());
            }
            if kind != ProviderKind::Gemini || !streaming || key != "alt" || value != "sse" || alt {
                return Err(Failure::invalid("Unsupported query parameter"));
            }
            alt = true;
        }
    }
    Ok(())
}
