//! Typed configuration for the standalone Nyro data plane.

use nyro_limit::ConcurrencyLimit;
use nyro_llm::config::Config as LlmConfig;
use nyro_security::{ApiKey, ApiKeys};
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::{
    collections::BTreeSet,
    fs,
    net::{IpAddr, Ipv4Addr, SocketAddr},
    path::Path,
};
use thiserror::Error;

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Config {
    #[serde(default)]
    pub server: ServerConfig,
    pub llm: LlmConfig,
    #[serde(default)]
    pub security: SecurityConfig,
    #[serde(default)]
    pub limit: LimitConfig,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ServerConfig {
    #[serde(default = "default_listen")]
    pub listen: SocketAddr,
    #[serde(default = "default_request_timeout_ms")]
    pub request_timeout_ms: u64,
    #[serde(default = "default_max_body_bytes")]
    pub max_body_bytes: usize,
    #[serde(default = "default_max_response_bytes")]
    pub max_response_bytes: usize,
    #[serde(default = "default_max_frame_bytes")]
    pub max_frame_bytes: usize,
}

impl Default for ServerConfig {
    fn default() -> Self {
        Self {
            listen: default_listen(),
            request_timeout_ms: default_request_timeout_ms(),
            max_body_bytes: default_max_body_bytes(),
            max_response_bytes: default_max_response_bytes(),
            max_frame_bytes: default_max_frame_bytes(),
        }
    }
}

#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct SecurityConfig {
    #[serde(default)]
    pub api_keys: Vec<ApiKey>,
}

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct LimitConfig {
    #[serde(default = "default_concurrency")]
    pub concurrency: usize,
}

impl Default for LimitConfig {
    fn default() -> Self {
        Self {
            concurrency: default_concurrency(),
        }
    }
}

#[derive(Debug, Error)]
pub enum ConfigError {
    #[error("could not read configuration file")]
    Read(#[source] std::io::Error),
    #[error("invalid YAML configuration")]
    InvalidYaml,
    #[error("invalid YAML configuration at line {line}, column {column}")]
    InvalidYamlAt { line: usize, column: usize },
    #[error(transparent)]
    Llm(#[from] nyro_llm::config::ConfigError),
    #[error("invalid API key configuration")]
    InvalidApiKeys,
    #[error("configuration value `{field}` must be greater than zero")]
    ZeroLimit { field: &'static str },
    #[error("invalid concurrency limit")]
    InvalidConcurrency,
    #[error("model `{model}` must list at least one subject when anonymous access is disabled")]
    MissingSubjects { model: String },
    #[error("model `{model}` references unknown subject `{subject}`")]
    UnknownSubject { model: String, subject: String },
    #[error("could not serialize effective configuration")]
    Fingerprint,
}

impl Config {
    pub fn load(path: impl AsRef<Path>) -> Result<Self, ConfigError> {
        let yaml = fs::read_to_string(path).map_err(ConfigError::Read)?;
        Self::from_yaml(&yaml)
    }

    pub fn from_yaml(yaml: &str) -> Result<Self, ConfigError> {
        let config: Self = serde_yaml::from_str(yaml).map_err(|error| {
            error
                .location()
                .map_or(ConfigError::InvalidYaml, |location| {
                    ConfigError::InvalidYamlAt {
                        line: location.line(),
                        column: location.column(),
                    }
                })
        })?;
        config.validate()?;
        Ok(config)
    }

    pub fn validate(&self) -> Result<(), ConfigError> {
        self.llm.validate()?;
        if self.security.api_keys.iter().any(|key| {
            key.id.trim().is_empty() || !key.secret.bytes().all(|byte| byte.is_ascii_graphic())
        }) {
            return Err(ConfigError::InvalidApiKeys);
        }
        ApiKeys::new(self.security.api_keys.clone()).map_err(|_| ConfigError::InvalidApiKeys)?;

        for (field, is_zero) in [
            (
                "server.request_timeout_ms",
                self.server.request_timeout_ms == 0,
            ),
            ("server.max_body_bytes", self.server.max_body_bytes == 0),
            (
                "server.max_response_bytes",
                self.server.max_response_bytes == 0,
            ),
            ("server.max_frame_bytes", self.server.max_frame_bytes == 0),
        ] {
            if is_zero {
                return Err(ConfigError::ZeroLimit { field });
            }
        }
        ConcurrencyLimit::new(self.limit.concurrency)
            .map_err(|_| ConfigError::InvalidConcurrency)?;

        let subjects: BTreeSet<&str> = self
            .security
            .api_keys
            .iter()
            .map(|key| key.id.as_str())
            .collect();
        for (model_id, model) in &self.llm.models {
            if !model.allow_anonymous && model.subjects.is_empty() {
                return Err(ConfigError::MissingSubjects {
                    model: model_id.clone(),
                });
            }
            for subject in &model.subjects {
                if !subjects.contains(subject.as_str()) {
                    return Err(ConfigError::UnknownSubject {
                        model: model_id.clone(),
                        subject: subject.clone(),
                    });
                }
            }
        }

        Ok(())
    }

    pub fn fingerprint(&self) -> Result<String, ConfigError> {
        self.validate()?;
        let mut canonical = self.clone();
        canonical.server.listen = default_listen();
        canonical
            .security
            .api_keys
            .sort_by(|left, right| (&left.id, &left.secret).cmp(&(&right.id, &right.secret)));
        for model in canonical.llm.models.values_mut() {
            model.workloads.sort();
        }
        for provider in canonical.llm.providers.values_mut() {
            if provider.api == Some(nyro_llm::config::OpenAiApi::ChatCompletions) {
                provider.api = None;
            }
        }
        let encoded = serde_json::to_vec(&canonical).map_err(|_| ConfigError::Fingerprint)?;
        Ok(format!("{:x}", Sha256::digest(encoded)))
    }
}

const fn default_listen() -> SocketAddr {
    SocketAddr::new(IpAddr::V4(Ipv4Addr::LOCALHOST), 19_530)
}

const fn default_request_timeout_ms() -> u64 {
    120_000
}

const fn default_max_body_bytes() -> usize {
    1_048_576
}

const fn default_max_response_bytes() -> usize {
    16_777_216
}

const fn default_max_frame_bytes() -> usize {
    1_048_576
}

const fn default_concurrency() -> usize {
    64
}
