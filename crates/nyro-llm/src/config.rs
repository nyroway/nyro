use crate::Workload;
use reqwest::{Url, header::HeaderValue};
use serde::{Deserialize, Serialize};
use std::collections::{BTreeMap, BTreeSet};
use thiserror::Error;

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Config {
    pub providers: BTreeMap<String, Provider>,
    pub models: BTreeMap<String, Model>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ProviderKind {
    Openai,
    Anthropic,
    Gemini,
}

#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Provider {
    pub kind: ProviderKind,
    pub base_url: String,
    #[serde(default)]
    pub api_key: Option<String>,
}

impl std::fmt::Debug for Provider {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("Provider")
            .field("kind", &self.kind)
            .field("base_url", &"<configured>")
            .field("api_key", &self.api_key.as_ref().map(|_| "[REDACTED]"))
            .finish()
    }
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Model {
    pub provider: String,
    pub upstream_model: String,
    pub workloads: Vec<Workload>,
    #[serde(default)]
    pub allow_anonymous: bool,
    #[serde(default)]
    pub subjects: BTreeSet<String>,
}

#[derive(Debug, Error)]
pub enum ConfigError {
    #[error("LLM configuration must define at least one provider")]
    NoProviders,
    #[error("LLM configuration must define at least one model")]
    NoModels,
    #[error("provider ID must not be empty")]
    EmptyProviderId,
    #[error("provider `{provider}` has an invalid base URL")]
    InvalidProviderUrl { provider: String },
    #[error("provider `{provider}` has an invalid API key")]
    InvalidProviderApiKey { provider: String },
    #[error("model ID must not be empty")]
    EmptyModelId,
    #[error("model `{model}` references unknown provider `{provider}`")]
    UnknownProvider { model: String, provider: String },
    #[error("model `{model}` must define an upstream model")]
    EmptyUpstreamModel { model: String },
    #[error("model `{model}` must define at least one workload")]
    NoWorkloads { model: String },
    #[error("model `{model}` contains duplicate workloads")]
    DuplicateWorkload { model: String },
    #[error("model `{model}` declares a workload unsupported by its provider")]
    UnsupportedWorkload { model: String },
    #[error("model `{model}` has an invalid Gemini upstream model name")]
    InvalidGeminiModel { model: String },
}

impl Config {
    pub fn validate(&self) -> Result<(), ConfigError> {
        if self.providers.is_empty() {
            return Err(ConfigError::NoProviders);
        }
        if self.models.is_empty() {
            return Err(ConfigError::NoModels);
        }

        for (id, provider) in &self.providers {
            if id.trim().is_empty() {
                return Err(ConfigError::EmptyProviderId);
            }
            let url =
                Url::parse(&provider.base_url).map_err(|_| ConfigError::InvalidProviderUrl {
                    provider: id.clone(),
                })?;
            let authority = provider
                .base_url
                .split_once("://")
                .map(|(_, rest)| rest.split(['/', '?', '#']).next().unwrap_or_default());
            if !matches!(url.scheme(), "http" | "https")
                || url.host_str().is_none()
                || authority.is_none_or(|authority| authority.contains('@'))
                || !url.username().is_empty()
                || url.password().is_some()
                || url.query().is_some()
                || url.fragment().is_some()
            {
                return Err(ConfigError::InvalidProviderUrl {
                    provider: id.clone(),
                });
            }
            if let Some(api_key) = &provider.api_key
                && (api_key.is_empty() || HeaderValue::try_from(api_key).is_err())
            {
                return Err(ConfigError::InvalidProviderApiKey {
                    provider: id.clone(),
                });
            }
        }

        for (id, model) in &self.models {
            if id.trim().is_empty() {
                return Err(ConfigError::EmptyModelId);
            }
            if !self.providers.contains_key(&model.provider) {
                return Err(ConfigError::UnknownProvider {
                    model: id.clone(),
                    provider: model.provider.clone(),
                });
            }
            if model.upstream_model.trim().is_empty() {
                return Err(ConfigError::EmptyUpstreamModel { model: id.clone() });
            }
            let kind = self.providers[&model.provider].kind;
            if kind != ProviderKind::Openai && model.workloads.contains(&Workload::Embedding) {
                return Err(ConfigError::UnsupportedWorkload { model: id.clone() });
            }
            if kind == ProviderKind::Gemini {
                let name = model
                    .upstream_model
                    .strip_prefix("models/")
                    .unwrap_or(&model.upstream_model);
                if name.is_empty()
                    || name == "."
                    || name == ".."
                    || !name
                        .bytes()
                        .all(|b| b.is_ascii_alphanumeric() || b"-_.".contains(&b))
                {
                    return Err(ConfigError::InvalidGeminiModel { model: id.clone() });
                }
            }
            if model.workloads.is_empty() {
                return Err(ConfigError::NoWorkloads { model: id.clone() });
            }
            let mut workloads = BTreeSet::new();
            if !model
                .workloads
                .iter()
                .all(|workload| workloads.insert(*workload))
            {
                return Err(ConfigError::DuplicateWorkload { model: id.clone() });
            }
        }

        Ok(())
    }
}
