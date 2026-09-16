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
    proxy_url: Option<String>,
    http1_only: bool,
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
            proxy_url: provider
                .transport
                .proxy()
                .expect("validated provider transport")
                .map(Into::into),
            http1_only: provider.transport.http1_only,
            upstream_model: backend.upstream_model.clone(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn proxy_identity_normalizes_urls_but_isolates_credentials_and_http_mode() {
        let backend = Backend {
            id: "a".into(),
            provider: "p".into(),
            upstream_model: "m".into(),
            weight: 1,
            priority: 0,
        };
        let mut provider: Provider = serde_json::from_value(serde_json::json!({
            "kind":"openai", "base_url":"http://upstream.test/v1",
            "transport":{"proxy_url":"http://user:password@LOCALHOST:80"}
        }))
        .unwrap();
        let original = BackendKey::new("public", &backend, &provider);
        provider.transport.proxy_url = Some("http://user:password@localhost/".into());
        assert!(original == BackendKey::new("public", &backend, &provider));
        provider.transport.proxy_url = Some("http://user:rotated@localhost/".into());
        assert!(original != BackendKey::new("public", &backend, &provider));
        provider.transport.proxy_url = Some("http://user:password@localhost/".into());
        provider.transport.http1_only = true;
        assert!(original != BackendKey::new("public", &backend, &provider));
    }
}
