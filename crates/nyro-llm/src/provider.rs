//! Provider-owned URLs and credentials; caller headers never cross this boundary.
use crate::{
    Request,
    codec::{ChatFormat, CodecError, anthropic, gemini, openai},
    config::{OpenAiApi, Provider, ProviderKind},
    runtime::BuildError,
};
use reqwest::{
    Client, Url,
    header::{HeaderName, HeaderValue},
};
use serde_json::Value;

pub(crate) struct Driver {
    client: Client,
    base: Url,
    kind: ProviderKind,
    pub(crate) format: ChatFormat,
    credential: Option<(HeaderName, HeaderValue)>,
}

impl Driver {
    pub(crate) fn new(config: &Provider) -> Result<Self, BuildError> {
        let mut base = Url::parse(&config.base_url).map_err(|_| BuildError)?;
        base.set_path(&format!("{}/", base.path().trim_end_matches('/')));
        let credential = config
            .api_key
            .as_ref()
            .map(|secret| {
                let (name, value) = match config.kind {
                    ProviderKind::Openai => ("authorization", format!("Bearer {secret}")),
                    ProviderKind::Anthropic => ("x-api-key", secret.clone()),
                    ProviderKind::Gemini => ("x-goog-api-key", secret.clone()),
                };
                let mut value = HeaderValue::from_str(&value).map_err(|_| BuildError)?;
                value.set_sensitive(true);
                Ok::<_, BuildError>((HeaderName::from_static(name), value))
            })
            .transpose()?;
        Ok(Self {
            client: Client::builder()
                .no_proxy()
                .redirect(reqwest::redirect::Policy::none())
                .build()
                .map_err(|_| BuildError)?,
            base,
            kind: config.kind,
            format: match config.kind {
                ProviderKind::Openai => match config.api.unwrap_or_default() {
                    OpenAiApi::ChatCompletions => ChatFormat::OpenAiChat,
                    OpenAiApi::Responses => ChatFormat::OpenAiResponses,
                },
                ProviderKind::Anthropic => ChatFormat::Anthropic,
                ProviderKind::Gemini => ChatFormat::Gemini,
            },
            credential,
        })
    }

    pub(crate) fn encode(&self, request: &Request) -> Result<Value, CodecError> {
        match (self.format, request) {
            (ChatFormat::OpenAiChat, Request::Chat(request)) => openai::encode_chat(request),
            (ChatFormat::OpenAiResponses, Request::Chat(request)) => {
                openai::responses::encode_chat(request)
            }
            (ChatFormat::OpenAiChat, Request::Embedding(request)) => {
                openai::encode_embedding(request)
            }
            (ChatFormat::Anthropic, Request::Chat(request)) => anthropic::encode_chat(request),
            (ChatFormat::Gemini, Request::Chat(request)) => gemini::encode_chat(request),
            _ => Err(CodecError("provider does not support this workload".into())),
        }
    }

    pub(crate) async fn send(
        &self,
        request: &Request,
        body: &Value,
    ) -> Result<reqwest::Response, reqwest::Error> {
        let mut url = self.base.clone();
        // Model names are validated as a single safe segment during candidate construction.
        let endpoint = match (self.format, request) {
            (ChatFormat::OpenAiChat, Request::Embedding(_)) => "embeddings".to_owned(),
            (ChatFormat::OpenAiChat, _) => "chat/completions".to_owned(),
            (ChatFormat::OpenAiResponses, _) => "responses".to_owned(),
            (ChatFormat::Anthropic, _) => "messages".to_owned(),
            (ChatFormat::Gemini, _) => format!(
                "models/{}:{}",
                request
                    .model()
                    .strip_prefix("models/")
                    .unwrap_or(request.model()),
                if request.is_streaming() {
                    "streamGenerateContent"
                } else {
                    "generateContent"
                }
            ),
        };
        url.set_path(&format!("{}{endpoint}", self.base.path()));
        if self.kind == ProviderKind::Gemini && request.is_streaming() {
            url.query_pairs_mut().append_pair("alt", "sse");
        }
        let mut builder = self.client.post(url).json(body);
        if let Some((name, value)) = &self.credential {
            builder = builder.header(name, value);
        }
        if self.kind == ProviderKind::Anthropic {
            builder = builder.header("anthropic-version", "2023-06-01");
        }
        builder.send().await
    }
}
