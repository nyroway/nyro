//! Effective identity shared by routing history and passive health.
use crate::config::{Backend, OpenAiApi, Provider, ProviderKind};

// Do not derive Debug: effective identity includes provider credentials.
#[derive(Eq, PartialEq, Hash)]
pub(crate) struct BackendKey {
    model: String,
    backend: String,
    provider: String,
    kind: ProviderKind,
    api: OpenAiApi,
    native_chat: bool,
    base_url: String,
    api_key: Option<String>,
    upstream_model: String,
}

impl BackendKey {
    pub(crate) fn new(model: &str, backend: &Backend, provider: &Provider) -> Self {
        // Match Driver's effective endpoint and default API selection.
        let mut base = reqwest::Url::parse(&provider.base_url).expect("validated provider URL");
        base.set_path(&format!("{}/", base.path().trim_end_matches('/')));
        Self {
            model: model.into(),
            backend: backend.id.clone(),
            provider: backend.provider.clone(),
            kind: provider.kind,
            api: provider.api.unwrap_or_default(),
            native_chat: provider.native_chat,
            base_url: base.into(),
            api_key: provider.api_key.clone(),
            upstream_model: backend.upstream_model.clone(),
        }
    }
}
