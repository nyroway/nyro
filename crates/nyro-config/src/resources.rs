//! Public runtime resources, independent of process startup settings.
use nyro_balance::Strategy;
use serde::{Deserialize, Serialize};
use std::{collections::BTreeSet, fmt, time::Duration};

#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Resources {
    #[serde(deserialize_with = "number")]
    pub version: u32,
    #[serde(default)]
    pub upstreams: Vec<Upstream>,
    #[serde(default)]
    pub models: Vec<Model>,
    #[serde(default)]
    pub mcps: Vec<Mcp>,
    #[serde(default)]
    pub consumers: Vec<Consumer>,
}
impl Default for Resources {
    fn default() -> Self {
        Self {
            version: 1,
            upstreams: vec![],
            models: vec![],
            mcps: vec![],
            consumers: vec![],
        }
    }
}
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Upstream {
    pub id: String,
    #[serde(default)]
    pub name: String,
    pub kind: Kind,
    #[serde(default)]
    pub balance: Strategy,
    pub targets: Vec<Target>,
}
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Kind {
    Llm,
    Mcp,
}

// Targets share identity/weight/auth, while validation enforces kind-specific fields.
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Target {
    pub id: String,
    #[serde(
        default,
        deserialize_with = "optional_number",
        skip_serializing_if = "Option::is_none"
    )]
    pub weight: Option<u32>,
    #[serde(default, deserialize_with = "number")]
    pub priority: u32,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub protocol: Option<Protocol>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub base_url: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub model: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub url: Option<String>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub transport: Option<McpTransport>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub auth: Option<nyro_authn::outbound::KeyAuth>,
    #[serde(default)]
    pub egress: nyro_llm::config::Transport,
}
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
pub enum Protocol {
    #[serde(rename = "openai/chat-completions")]
    OpenAiChat,
    #[serde(rename = "openai/responses")]
    OpenAiResponses,
    #[serde(rename = "openai/embeddings")]
    OpenAiEmbeddings,
    #[serde(rename = "anthropic/messages")]
    AnthropicMessages,
    #[serde(rename = "gemini/generate-content")]
    GeminiGenerateContent,
}
#[derive(Clone, Copy, Debug, Serialize, Deserialize)]
pub enum McpTransport {
    #[serde(rename = "streamable-http")]
    StreamableHttp,
}
#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Capability {
    Chat,
    Embedding,
}
#[derive(Clone, Copy, Debug, Default, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum AccessMode {
    Anonymous,
    Authenticated,
    #[default]
    Restricted,
}
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Access {
    #[serde(default)]
    pub mode: AccessMode,
}
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Execution {
    #[serde(
        default,
        deserialize_with = "optional_number",
        skip_serializing_if = "Option::is_none"
    )]
    pub request_timeout: Option<f64>,
    #[serde(
        default,
        deserialize_with = "optional_number",
        skip_serializing_if = "Option::is_none"
    )]
    pub max_body_bytes: Option<usize>,
    #[serde(
        default,
        deserialize_with = "optional_number",
        skip_serializing_if = "Option::is_none"
    )]
    pub max_response_bytes: Option<usize>,
    #[serde(
        default,
        deserialize_with = "optional_number",
        skip_serializing_if = "Option::is_none"
    )]
    pub max_frame_bytes: Option<usize>,
    #[serde(
        default,
        deserialize_with = "optional_number",
        skip_serializing_if = "Option::is_none"
    )]
    pub max_attempts: Option<u32>,
}
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Limits {
    #[serde(default)]
    pub request: Vec<Window>,
    #[serde(default)]
    pub token: Vec<Window>,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Window {
    #[serde(deserialize_with = "number")]
    pub limit: u64,
    pub window: String,
}
impl Window {
    pub fn period(&self) -> Result<Duration, Error> {
        let text = &self.window;
        let (number, factor) = if let Some(n) = text.strip_suffix("ms") {
            (n, 0.001)
        } else if let Some(n) = text.strip_suffix('s') {
            (n, 1.)
        } else if let Some(n) = text.strip_suffix('m') {
            (n, 60.)
        } else if let Some(n) = text.strip_suffix('h') {
            (n, 3600.)
        } else if let Some(n) = text.strip_suffix('d') {
            (n, 86400.)
        } else {
            return Err(Error("window requires ms, s, m, h or d"));
        };
        seconds(number.parse::<f64>().map_err(|_| Error("invalid window"))? * factor)
    }
}
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Model {
    pub id: String,
    #[serde(default)]
    pub name: String,
    pub upstream: String,
    pub capability: Capability,
    #[serde(default)]
    pub access: Access,
    #[serde(default)]
    pub execution: Execution,
    #[serde(default)]
    pub limits: Limits,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Mcp {
    pub id: String,
    #[serde(default)]
    pub name: String,
    pub upstream: String,
    pub allowed_tools: Vec<String>,
    #[serde(default)]
    pub access: Access,
    #[serde(default)]
    pub execution: Execution,
    #[serde(default)]
    pub limits: Limits,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Consumer {
    pub id: String,
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub credentials: Vec<Credential>,
    #[serde(default)]
    pub grants: Grants,
    #[serde(default)]
    pub limits: ConsumerLimits,
}
#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Credential {
    pub id: String,
    #[serde(rename = "type")]
    pub kind: CredentialKind,
    pub secret: String,
}
impl fmt::Debug for Credential {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Credential")
            .field("id", &self.id)
            .field("secret", &"[REDACTED]")
            .finish()
    }
}
#[derive(Clone, Debug, Serialize, Deserialize)]
pub enum CredentialKind {
    #[serde(rename = "key-auth")]
    KeyAuth,
}
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Grants {
    #[serde(default)]
    pub models: Vec<String>,
    #[serde(default)]
    pub mcps: Vec<String>,
}
#[derive(Clone, Debug, Default, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ConsumerLimits {
    #[serde(default)]
    pub llm: Limits,
    #[serde(default)]
    pub mcp: Limits,
}
#[derive(Debug, thiserror::Error)]
#[error("invalid resources: {0}")]
pub struct Error(pub &'static str);

fn number<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Deserialize<'de> + std::str::FromStr,
{
    #[derive(Deserialize)]
    #[serde(untagged)]
    enum Input<T> {
        Number(T),
        Text(String),
    }
    match Input::<T>::deserialize(d)? {
        Input::Number(n) => Ok(n),
        Input::Text(s) => s
            .parse()
            .map_err(|_| serde::de::Error::custom("invalid numeric value")),
    }
}
fn optional_number<'de, D, T>(d: D) -> Result<Option<T>, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Deserialize<'de> + std::str::FromStr,
{
    number(d).map(Some)
}
fn seconds(n: f64) -> Result<Duration, Error> {
    let d = Duration::try_from_secs_f64(n).map_err(|_| Error("invalid duration"))?;
    if d.is_zero() || std::time::Instant::now().checked_add(d).is_none() {
        return Err(Error("invalid duration"));
    }
    Ok(d)
}
fn ids<'a>(values: impl Iterator<Item = &'a str>) -> Result<BTreeSet<&'a str>, Error> {
    let mut ids = BTreeSet::new();
    for id in values {
        if id.is_empty()
            || id.len() > 128
            || id.chars().any(char::is_control)
            || id.trim() != id
            || !ids.insert(id)
        {
            return Err(Error("IDs must be nonempty and unique"));
        }
    }
    Ok(ids)
}
impl Execution {
    fn validate(&self, mcp: bool) -> Result<(), Error> {
        if let Some(n) = self.request_timeout {
            seconds(n)?;
        }
        if [
            self.max_body_bytes,
            self.max_response_bytes,
            self.max_frame_bytes,
        ]
        .contains(&Some(0))
            || self.max_attempts == Some(0)
            || (mcp && self.max_attempts.is_some())
        {
            return Err(Error("invalid execution limits"));
        }
        Ok(())
    }
}
impl Limits {
    fn validate(&self, mcp: bool) -> Result<(), Error> {
        if mcp && !self.token.is_empty() {
            return Err(Error("MCP does not report token usage"));
        }
        for rules in [&self.request, &self.token] {
            let mut periods = BTreeSet::new();
            for rule in rules {
                if rule.limit == 0 || !periods.insert(rule.period()?) {
                    return Err(Error("limits must be positive with unique windows"));
                }
            }
        }
        Ok(())
    }
}
impl Resources {
    pub fn from_yaml(yaml: &str) -> Result<Self, Error> {
        Self::from_yaml_with(yaml, |name| std::env::var(name).ok())
    }
    pub fn from_yaml_with(
        yaml: &str,
        lookup: impl Fn(&str) -> Option<String>,
    ) -> Result<Self, Error> {
        if yaml.len() > 1_048_576 {
            return Err(Error("configuration exceeds size limit"));
        }
        let mut ast: serde_yaml::Value =
            serde_yaml::from_str(yaml).map_err(|_| Error("invalid YAML"))?;
        expand(&mut ast, &lookup, &mut 0)?;
        let config: Self = serde_yaml::from_value(ast)
            .map_err(|_| Error("unknown field or invalid resource value"))?;
        config.validate()?;
        Ok(config)
    }
    pub fn validate(&self) -> Result<(), Error> {
        if self.version != 1 {
            return Err(Error("unsupported version"));
        }
        ids(self.upstreams.iter().map(|r| r.id.as_str()))?;
        let models = ids(self.models.iter().map(|r| r.id.as_str()))?;
        let mcps = ids(self.mcps.iter().map(|r| r.id.as_str()))?;
        ids(self.consumers.iter().map(|r| r.id.as_str()))?;
        for pool in &self.upstreams {
            ids(pool.targets.iter().map(|t| t.id.as_str()))?;
            if pool.targets.is_empty() || !pool.targets.iter().any(|t| t.weight.unwrap_or(1) > 0) {
                return Err(Error("upstream needs a positive-weight target"));
            }
            if pool.kind == Kind::Mcp
                && !matches!(
                    pool.balance,
                    Strategy::WeightedRoundrobin | Strategy::WeightedRandom
                )
            {
                return Err(Error("unsupported MCP balance strategy"));
            }
            for target in &pool.targets {
                if let Some(auth) = &target.auth {
                    auth.validate()
                        .map_err(|_| Error("invalid upstream authentication"))?;
                }
                match pool.kind {
                    Kind::Llm => {
                        crate::compile::validate_llm_target(target)?;
                        if target.protocol.is_none()
                            || target.model.as_deref().is_none_or(|s| s.trim().is_empty())
                            || target.url.is_some()
                            || target.transport.is_some()
                        {
                            return Err(Error("invalid LLM target"));
                        }
                        valid_url(
                            target
                                .base_url
                                .as_deref()
                                .ok_or(Error("LLM target requires base_url"))?,
                        )?;
                    }
                    Kind::Mcp => {
                        if target.transport.is_none()
                            || target.protocol.is_some()
                            || target.base_url.is_some()
                            || target.model.is_some()
                            || target.priority != 0
                            || target.egress.proxy_url.is_some()
                            || target.egress.http1_only
                        {
                            return Err(Error("invalid MCP target"));
                        }
                        valid_url(
                            target
                                .url
                                .as_deref()
                                .ok_or(Error("MCP target requires url"))?,
                        )?;
                    }
                }
            }
        }
        for model in &self.models {
            let pool = self.pool(&model.upstream, Kind::Llm)?;
            if pool.targets.iter().any(|t| {
                (t.protocol == Some(Protocol::OpenAiEmbeddings))
                    != (model.capability == Capability::Embedding)
            }) {
                return Err(Error("target protocol does not support model capability"));
            }
            model.execution.validate(false)?;
            model.limits.validate(false)?;
        }
        for mcp in &self.mcps {
            self.pool(&mcp.upstream, Kind::Mcp)?;
            if mcp.id.len() > 64
                || !mcp
                    .id
                    .bytes()
                    .all(|b| b.is_ascii_alphanumeric() || b"-_".contains(&b))
            {
                return Err(Error("MCP ID must be a safe path segment"));
            }
            let tools = ids(mcp.allowed_tools.iter().map(String::as_str))?;
            if tools.is_empty() || tools.contains("*") {
                return Err(Error("allowed_tools must list exact names"));
            }
            mcp.execution.validate(true)?;
            mcp.limits.validate(true)?;
        }
        let mut secrets = BTreeSet::new();
        for consumer in &self.consumers {
            ids(consumer.credentials.iter().map(|r| r.id.as_str()))?;
            for credential in &consumer.credentials {
                if credential.secret.is_empty()
                    || !credential.secret.bytes().all(|b| b.is_ascii_graphic())
                    || !secrets.insert(&credential.secret)
                {
                    return Err(Error("invalid or duplicate consumer credential"));
                }
            }
            for (grants, known) in [
                (&consumer.grants.models, &models),
                (&consumer.grants.mcps, &mcps),
            ] {
                for id in ids(grants.iter().map(String::as_str))? {
                    if !known.contains(id) {
                        return Err(Error("grant references an unknown resource"));
                    }
                }
            }
            consumer.limits.llm.validate(false)?;
            consumer.limits.mcp.validate(true)?;
        }
        Ok(())
    }
    pub fn pool(&self, id: &str, kind: Kind) -> Result<&Upstream, Error> {
        self.upstreams
            .iter()
            .find(|r| r.id == id && r.kind == kind)
            .ok_or(Error("unknown upstream or incompatible kind"))
    }
}
fn valid_url(raw: &str) -> Result<(), Error> {
    let url = url::Url::parse(raw).map_err(|_| Error("invalid upstream URL"))?;
    if !matches!(url.scheme(), "http" | "https")
        || url.host_str().is_none()
        || !url.username().is_empty()
        || url.password().is_some()
        || url.query().is_some()
        || url.fragment().is_some()
        || raw.chars().any(char::is_whitespace)
        || raw
            .split_once("://")
            .is_none_or(|(_, r)| r.split('/').next().unwrap_or("").contains('@'))
    {
        return Err(Error("invalid upstream URL"));
    }
    Ok(())
}
fn expand(
    value: &mut serde_yaml::Value,
    lookup: &impl Fn(&str) -> Option<String>,
    total: &mut usize,
) -> Result<(), Error> {
    use serde_yaml::Value;
    match value {
        Value::String(text) => {
            let mut output = String::new();
            let mut rest = text.as_str();
            while let Some(pos) = rest.find('$') {
                output.push_str(&rest[..pos]);
                rest = &rest[pos..];
                if let Some(escaped) = rest.strip_prefix("$${") {
                    let end = escaped
                        .find('}')
                        .ok_or(Error("unterminated environment placeholder"))?;
                    output.push_str("${");
                    output.push_str(&escaped[..=end]);
                    rest = &escaped[end + 1..];
                } else if let Some(variable) = rest.strip_prefix("${") {
                    let end = variable
                        .find('}')
                        .ok_or(Error("unterminated environment placeholder"))?;
                    let name = &variable[..end];
                    if name.is_empty()
                        || !name.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_')
                    {
                        return Err(Error("invalid environment name"));
                    }
                    output.push_str(&lookup(name).ok_or(Error("missing environment variable"))?);
                    rest = &variable[end + 1..];
                } else {
                    output.push('$');
                    rest = &rest[1..];
                }
                if output.len() > 1_048_576 {
                    return Err(Error("expanded value exceeds size limit"));
                }
            }
            output.push_str(rest);
            *total = total
                .checked_add(output.len())
                .ok_or(Error("expanded configuration exceeds size limit"))?;
            if *total > 1_048_576 {
                return Err(Error("expanded configuration exceeds size limit"));
            }
            *text = output;
        }
        Value::Sequence(items) => {
            for item in items {
                expand(item, lookup, total)?;
            }
        }
        Value::Mapping(items) => {
            for (_, item) in items {
                expand(item, lookup, total)?;
            }
        }
        Value::Tagged(_) => return Err(Error("YAML tags are not supported")),
        _ => {}
    }
    Ok(())
}
