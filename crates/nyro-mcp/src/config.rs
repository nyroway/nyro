//! Strict, transport-local MCP configuration. Validation never contacts upstreams.
use serde::{Deserialize, Deserializer, Serialize};
use std::{
    collections::{BTreeMap, BTreeSet},
    fmt,
    time::{Duration, Instant},
};
use thiserror::Error;

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Config {
    #[serde(default = "timeout")]
    pub request_timeout_ms: u64,
    #[serde(default = "body_limit")]
    pub max_body_bytes: usize,
    #[serde(default = "response_limit")]
    pub max_response_bytes: usize,
    #[serde(default = "body_limit")]
    pub max_frame_bytes: usize,
    pub servers: BTreeMap<String, Server>,
}
#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Server {
    pub transport: Transport,
    pub url: String,
    #[serde(
        default,
        skip_serializing_if = "Option::is_none",
        deserialize_with = "present"
    )]
    pub bearer_token: Option<String>,
    pub subjects: Vec<String>,
    pub allowed_tools: Vec<String>,
}
impl fmt::Debug for Server {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Server")
            .field("transport", &self.transport)
            .field("url", &"[redacted]")
            .field("has_bearer_token", &self.bearer_token.is_some())
            .field("subjects", &self.subjects)
            .field("allowed_tools", &self.allowed_tools)
            .finish()
    }
}
#[derive(Clone, Copy, Debug, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum Transport {
    Http,
}
#[derive(Debug, Error)]
#[error("invalid MCP configuration: {0}")]
pub struct ConfigError(pub &'static str);

fn present<'de, D: Deserializer<'de>>(d: D) -> Result<Option<String>, D::Error> {
    String::deserialize(d).map(Some)
}
const fn timeout() -> u64 {
    30_000
}
const fn body_limit() -> usize {
    1_048_576
}
const fn response_limit() -> usize {
    16_777_216
}
pub(crate) fn valid_id(id: &str) -> bool {
    (1..=64).contains(&id.len())
        && id
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_')
}
fn valid_set(values: &[String]) -> bool {
    !values.is_empty()
        && values.iter().all(|s| !s.trim().is_empty() && s != "*")
        && values.iter().collect::<BTreeSet<_>>().len() == values.len()
}
impl Config {
    pub fn validate(&self) -> Result<(), ConfigError> {
        if self.request_timeout_ms == 0
            || self.max_body_bytes == 0
            || self.max_response_bytes == 0
            || self.max_frame_bytes == 0
            || Instant::now()
                .checked_add(Duration::from_millis(self.request_timeout_ms))
                .is_none()
        {
            return Err(ConfigError(
                "limits must be positive and timeout representable",
            ));
        }
        if self.servers.is_empty() {
            return Err(ConfigError("servers must not be empty"));
        }
        for (id, server) in &self.servers {
            if !valid_id(id) {
                return Err(ConfigError("invalid server ID"));
            }
            let url =
                url::Url::parse(&server.url).map_err(|_| ConfigError("invalid upstream URL"))?;
            if !matches!(url.scheme(), "http" | "https")
                || url.host_str().is_none()
                || !url.username().is_empty()
                || url.password().is_some()
                || url.query().is_some()
                || url.fragment().is_some()
                || server
                    .url
                    .split("://")
                    .nth(1)
                    .unwrap_or("")
                    .split('/')
                    .next()
                    .unwrap_or("")
                    .contains('@')
                || server.url.chars().any(char::is_whitespace)
            {
                return Err(ConfigError(
                    "upstream URL must be HTTP(S) without credentials, query, fragment or whitespace",
                ));
            }
            if let Some(token) = &server.bearer_token {
                let core = token.trim_end_matches('=');
                if core.is_empty()
                    || !core
                        .bytes()
                        .all(|b| b.is_ascii_alphanumeric() || b"-._~+/".contains(&b))
                {
                    return Err(ConfigError("invalid upstream bearer token"));
                }
            }
            if !valid_set(&server.subjects) || !valid_set(&server.allowed_tools) {
                return Err(ConfigError(
                    "subjects and allowed_tools must be nonempty unique exact names",
                ));
            }
        }
        Ok(())
    }
    pub fn canonicalize(&mut self) {
        for server in self.servers.values_mut() {
            server.subjects.sort();
            server.allowed_tools.sort();
        }
    }
}
