use crate::Workload;
use reqwest::{Url, header::HeaderValue};
use serde::{Deserialize, Serialize};
use std::{
    collections::{BTreeMap, BTreeSet},
    time::{Duration, Instant},
};
use thiserror::Error;

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Config {
    pub providers: BTreeMap<String, Provider>,
    pub models: BTreeMap<String, Model>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum ProviderKind {
    Openai,
    Anthropic,
    Gemini,
}

/// Selects an API within the OpenAI protocol family.
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum OpenAiApi {
    #[default]
    ChatCompletions,
    Responses,
}

#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Provider {
    pub kind: ProviderKind,
    /// Preserve native JSON for matching OpenAI Chat, Anthropic Messages or Gemini endpoints.
    #[serde(default)]
    pub native_chat: bool,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub api: Option<OpenAiApi>,
    pub base_url: String,
    #[serde(default)]
    pub api_key: Option<String>,
}

impl std::fmt::Debug for Provider {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("Provider")
            .field("kind", &self.kind)
            .field("native_chat", &self.native_chat)
            .field("api", &self.api)
            .field("base_url", &"<configured>")
            .field("api_key", &self.api_key.as_ref().map(|_| "[REDACTED]"))
            .finish()
    }
}

#[derive(Clone, Debug, Serialize)]
pub struct Model {
    pub backends: Vec<Backend>,
    pub max_attempts: u32,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub health: Option<HealthConfig>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub rate: Option<RateConfig>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub quota: Option<QuotaConfig>,
    pub workloads: Vec<Workload>,
    pub allow_anonymous: bool,
    pub subjects: BTreeSet<String>,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Backend {
    pub id: String,
    pub provider: String,
    pub upstream_model: String,
    #[serde(default = "default_weight")]
    pub weight: u32,
    #[serde(default)]
    pub priority: u32,
}

#[derive(Clone, Debug, Eq, PartialEq, Hash, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct HealthConfig {
    pub failure_threshold: u32,
    pub cooldown_ms: u64,
}

impl Default for HealthConfig {
    fn default() -> Self {
        Self {
            failure_threshold: 3,
            cooldown_ms: 30_000,
        }
    }
}

/// Continuous request refill rate and initial/maximum token capacity.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct RateConfig {
    pub requests: u32,
    pub period_ms: u64,
    #[serde(default = "default_burst")]
    pub burst: u32,
}

/// Process-local cumulative token budget and provision reserved per upstream attempt.
#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct QuotaConfig {
    pub total_tokens: u64,
    pub reserve_tokens: u64,
}

const fn default_burst() -> u32 {
    1
}

const fn default_max_attempts() -> u32 {
    1
}

const fn default_weight() -> u32 {
    100
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct RawModel {
    #[serde(default = "default_max_attempts")]
    max_attempts: u32,
    #[serde(default)]
    health: Option<HealthConfig>,
    #[serde(default, deserialize_with = "present")]
    rate: Option<RateConfig>,
    #[serde(default, deserialize_with = "present")]
    quota: Option<QuotaConfig>,
    #[serde(default, deserialize_with = "present")]
    backends: Option<Vec<Backend>>,
    #[serde(default, deserialize_with = "present")]
    provider: Option<String>,
    #[serde(default, deserialize_with = "present")]
    upstream_model: Option<String>,
    workloads: Vec<Workload>,
    #[serde(default)]
    allow_anonymous: bool,
    #[serde(default)]
    subjects: BTreeSet<String>,
}

// Missing fields are allowed here; explicitly null fields are not.
fn present<'de, D, T>(deserializer: D) -> Result<Option<T>, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Deserialize<'de>,
{
    T::deserialize(deserializer).map(Some)
}

impl<'de> Deserialize<'de> for Model {
    fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        let raw = RawModel::deserialize(deserializer)?;
        let backends = match (raw.backends, raw.provider, raw.upstream_model) {
            (Some(backends), None, None) => backends,
            (None, Some(provider), Some(upstream_model)) => vec![Backend {
                id: "default".into(),
                provider,
                upstream_model,
                weight: default_weight(),
                priority: 0,
            }],
            _ => {
                return Err(serde::de::Error::custom(
                    "model must define either backends or provider and upstream_model",
                ));
            }
        };
        Ok(Self {
            backends,
            max_attempts: raw.max_attempts,
            health: raw.health,
            rate: raw.rate,
            quota: raw.quota,
            workloads: raw.workloads,
            allow_anonymous: raw.allow_anonymous,
            subjects: raw.subjects,
        })
    }
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
    #[error("provider `{provider}` declares an API selector outside the OpenAI family")]
    InvalidProviderApi { provider: String },
    #[error(
        "provider `{provider}` native_chat requires OpenAI Chat Completions, Anthropic Messages or Gemini generateContent"
    )]
    InvalidNativeChat { provider: String },
    #[error("model ID must not be empty")]
    EmptyModelId,
    #[error("model `{model}` max_attempts must be greater than zero")]
    InvalidMaxAttempts { model: String },
    #[error("model `{model}` health policy requires positive bounds and a representable cooldown")]
    InvalidHealth { model: String },
    #[error("model `{model}` rate policy requires positive, representable bounds")]
    InvalidRate { model: String },
    #[error(
        "model `{model}` quota policy requires positive bounds and reserve_tokens <= total_tokens"
    )]
    InvalidQuota { model: String },
    #[error("model `{model}` must define at least one backend")]
    NoBackends { model: String },
    #[error("model `{model}` has an empty backend ID")]
    EmptyBackendId { model: String },
    #[error("model `{model}` contains duplicate backend ID `{backend}`")]
    DuplicateBackendId { model: String, backend: String },
    #[error("model `{model}` must have a positive total backend weight within u64 bounds")]
    InvalidBackendWeight { model: String },
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
            if provider.native_chat && provider.api == Some(OpenAiApi::Responses) {
                return Err(ConfigError::InvalidNativeChat {
                    provider: id.clone(),
                });
            }
            if id.trim().is_empty() {
                return Err(ConfigError::EmptyProviderId);
            }
            if provider.kind != ProviderKind::Openai && provider.api.is_some() {
                return Err(ConfigError::InvalidProviderApi {
                    provider: id.clone(),
                });
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
            if model.max_attempts == 0 {
                return Err(ConfigError::InvalidMaxAttempts { model: id.clone() });
            }
            if model.health.as_ref().is_some_and(|health| {
                health.failure_threshold == 0
                    || health.cooldown_ms == 0
                    || Instant::now()
                        .checked_add(Duration::from_millis(health.cooldown_ms))
                        .is_none()
            }) {
                return Err(ConfigError::InvalidHealth { model: id.clone() });
            }
            if model.rate.as_ref().is_some_and(|rate| {
                nyro_limit::rate::RateLimit::new(
                    rate.requests,
                    Duration::from_millis(rate.period_ms),
                    rate.burst,
                )
                .is_err()
            }) {
                return Err(ConfigError::InvalidRate { model: id.clone() });
            }
            if model.quota.as_ref().is_some_and(|quota| {
                quota.total_tokens == 0
                    || quota.reserve_tokens == 0
                    || quota.reserve_tokens > quota.total_tokens
            }) {
                return Err(ConfigError::InvalidQuota { model: id.clone() });
            }
            if model.backends.is_empty() {
                return Err(ConfigError::NoBackends { model: id.clone() });
            }
            let mut backend_ids = BTreeSet::new();
            let mut total_weight = 0u64;
            for backend in &model.backends {
                if backend.id.trim().is_empty() {
                    return Err(ConfigError::EmptyBackendId { model: id.clone() });
                }
                if !backend_ids.insert(&backend.id) {
                    return Err(ConfigError::DuplicateBackendId {
                        model: id.clone(),
                        backend: backend.id.clone(),
                    });
                }
                total_weight = total_weight
                    .checked_add(u64::from(backend.weight))
                    .ok_or_else(|| ConfigError::InvalidBackendWeight { model: id.clone() })?;
                let provider = self.providers.get(&backend.provider).ok_or_else(|| {
                    ConfigError::UnknownProvider {
                        model: id.clone(),
                        provider: backend.provider.clone(),
                    }
                })?;
                if backend.upstream_model.trim().is_empty() {
                    return Err(ConfigError::EmptyUpstreamModel { model: id.clone() });
                }
                let kind = provider.kind;
                if (kind != ProviderKind::Openai || provider.api == Some(OpenAiApi::Responses))
                    && model.workloads.contains(&Workload::Embedding)
                {
                    return Err(ConfigError::UnsupportedWorkload { model: id.clone() });
                }
                if kind == ProviderKind::Gemini {
                    let name = backend
                        .upstream_model
                        .strip_prefix("models/")
                        .unwrap_or(&backend.upstream_model);
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
            }
            if total_weight == 0 {
                return Err(ConfigError::InvalidBackendWeight { model: id.clone() });
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
