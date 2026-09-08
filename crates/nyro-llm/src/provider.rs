//! Provider-owned URL and credentials, separate from codec and ingress policy.
use crate::{
    Workload,
    config::{Provider, ProviderKind},
    runtime::BuildError,
};
use reqwest::{
    Client, Url,
    header::{AUTHORIZATION, HeaderValue},
};
use serde_json::Value;

pub(crate) struct OpenAi {
    client: Client,
    chat_url: Url,
    embedding_url: Url,
    authorization: Option<HeaderValue>,
}

impl OpenAi {
    pub(crate) fn new(config: &Provider) -> Result<Self, BuildError> {
        match config.kind {
            ProviderKind::Openai => {}
        }
        let mut base = Url::parse(&config.base_url).map_err(|_| BuildError)?;
        base.set_path(&format!("{}/", base.path().trim_end_matches('/')));
        let authorization = config
            .api_key
            .as_ref()
            .map(|secret| {
                let mut value =
                    HeaderValue::from_str(&format!("Bearer {secret}")).map_err(|_| BuildError)?;
                value.set_sensitive(true);
                Ok::<_, BuildError>(value)
            })
            .transpose()?;
        Ok(Self {
            client: Client::builder()
                .no_proxy()
                .redirect(reqwest::redirect::Policy::none())
                .build()
                .map_err(|_| BuildError)?,
            chat_url: base.join("chat/completions").map_err(|_| BuildError)?,
            embedding_url: base.join("embeddings").map_err(|_| BuildError)?,
            authorization,
        })
    }

    pub(crate) async fn send(
        &self,
        workload: Workload,
        body: &Value,
    ) -> Result<reqwest::Response, reqwest::Error> {
        let url = match workload {
            Workload::Chat => &self.chat_url,
            Workload::Embedding => &self.embedding_url,
        };
        let mut request = self.client.post(url.clone()).json(body);
        if let Some(value) = &self.authorization {
            request = request.header(AUTHORIZATION, value.clone());
        }
        request.send().await
    }
}
