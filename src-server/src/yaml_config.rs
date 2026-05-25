use indexmap::IndexMap;
use serde::Deserialize;
use std::collections::HashMap;

#[derive(Debug, Deserialize)]
pub struct YamlConfig {
    #[serde(default)]
    pub server: ServerSection,
    #[serde(default)]
    pub providers: Vec<YamlProvider>,
    #[serde(default, rename = "models", alias = "routes")]
    pub models: Vec<YamlModel>,
    #[serde(default)]
    pub settings: HashMap<String, String>,
}

#[derive(Debug, Deserialize)]
pub struct ServerSection {
    #[serde(default = "default_proxy_host")]
    pub proxy_host: String,
    #[serde(default = "default_proxy_port")]
    pub proxy_port: u16,
}

impl Default for ServerSection {
    fn default() -> Self {
        Self {
            proxy_host: default_proxy_host(),
            proxy_port: default_proxy_port(),
        }
    }
}

fn default_proxy_host() -> String {
    "127.0.0.1".to_string()
}
fn default_proxy_port() -> u16 {
    19530
}

#[derive(Debug, Deserialize)]
#[serde(try_from = "YamlProviderRaw")]
pub struct YamlProvider {
    pub name: String,
    pub vendor: Option<String>,
    pub default_protocol: Option<String>,
    pub endpoints: IndexMap<String, YamlEndpoint>,
    pub api_key: String,
    pub use_proxy: bool,
    pub models_source: Option<String>,
    pub static_models: Option<Vec<String>>,
}

#[derive(Debug, Deserialize)]
struct YamlProviderRaw {
    pub name: String,
    #[serde(default)]
    pub vendor: Option<String>,
    #[serde(default)]
    pub default_protocol: Option<String>,
    #[serde(default)]
    pub protocol: Option<String>,
    #[serde(default)]
    pub endpoints: IndexMap<String, YamlEndpoint>,
    #[serde(default)]
    pub api_key: Option<String>,
    #[serde(default)]
    pub apikey: Option<String>,
    #[serde(default)]
    pub use_proxy: bool,
    #[serde(default)]
    pub models_source: Option<String>,
    #[serde(default)]
    pub static_models: Option<Vec<String>>,
    // Deprecated: capabilities_source was removed; captured here only to emit a warning.
    #[serde(default)]
    pub capabilities_source: Option<serde_json::Value>,
}

impl TryFrom<YamlProviderRaw> for YamlProvider {
    type Error = String;

    fn try_from(r: YamlProviderRaw) -> Result<Self, Self::Error> {
        let default_protocol = match (r.default_protocol, r.protocol) {
            (Some(_), Some(_)) => {
                return Err(format!(
                    "provider '{}': 'default_protocol' and its alias 'protocol' cannot both be set",
                    r.name
                ));
            }
            (Some(v), None) | (None, Some(v)) => Some(v),
            (None, None) => None,
        };
        let api_key = match (r.api_key, r.apikey) {
            (Some(_), Some(_)) => {
                return Err(format!(
                    "provider '{}': 'api_key' and its alias 'apikey' cannot both be set",
                    r.name
                ));
            }
            (Some(v), None) | (None, Some(v)) => v,
            (None, None) => {
                return Err(format!("provider '{}': 'api_key' is required", r.name));
            }
        };
        if r.capabilities_source.is_some() {
            tracing::warn!(
                provider = %r.name,
                "YAML field 'capabilities_source' is no longer supported and will be ignored; \
                 remove it from your config file"
            );
        }
        Ok(YamlProvider {
            name: r.name,
            vendor: r.vendor,
            default_protocol,
            endpoints: r.endpoints,
            api_key,
            use_proxy: r.use_proxy,
            models_source: r.models_source,
            static_models: r.static_models,
        })
    }
}

impl YamlProvider {
    pub fn resolved_protocol(&self) -> Option<&str> {
        if let Some(p) = self.default_protocol.as_deref() {
            return Some(p);
        }
        self.endpoints.keys().next().map(String::as_str)
    }
}

#[derive(Debug, Deserialize)]
pub struct YamlEndpoint {
    pub base_url: String,
}

#[derive(Debug, Deserialize)]
pub struct YamlModel {
    pub name: String,
    #[serde(alias = "vmodel")]
    pub virtual_model: String,
    #[serde(default = "default_balance", alias = "strategy")]
    pub balance: String,
    #[serde(default, rename = "backends", alias = "targets")]
    pub backends: Vec<YamlModelBackend>,
    #[serde(default)]
    pub access_control: bool,
    // Deprecated: route_type / type was removed; captured here only to emit a warning.
    #[serde(default, alias = "type")]
    pub route_type: Option<String>,
}

fn default_balance() -> String {
    "weighted".to_string()
}

#[derive(Debug, Deserialize)]
pub struct YamlModelBackend {
    pub provider: String,
    pub model: String,
    #[serde(default = "default_weight")]
    pub weight: i32,
    #[serde(default = "default_priority")]
    pub priority: i32,
}

fn default_weight() -> i32 {
    100
}
fn default_priority() -> i32 {
    1
}

impl YamlConfig {
    pub fn load(path: &str) -> anyhow::Result<Self> {
        let raw = std::fs::read_to_string(path)
            .map_err(|e| anyhow::anyhow!("failed to read config file {path}: {e}"))?;
        let content =
            shellexpand::env_with_context_no_errors(&raw, |var: &str| match std::env::var(var) {
                Ok(val) => Some(val),
                Err(_) => {
                    tracing::warn!(
                        "config: env var '{}' is not set, placeholder left as-is",
                        var
                    );
                    None
                }
            })
            .into_owned();
        let config: Self = serde_yaml::from_str(&content)
            .map_err(|e| anyhow::anyhow!("failed to parse YAML config: {e}"))?;
        config.validate()?;
        Ok(config)
    }

    fn validate(&self) -> anyhow::Result<()> {
        let provider_names: Vec<&str> = self.providers.iter().map(|p| p.name.as_str()).collect();
        for (i, p) in self.providers.iter().enumerate() {
            if p.name.trim().is_empty() {
                anyhow::bail!("providers[{i}]: name is required");
            }
            if p.endpoints.is_empty() {
                anyhow::bail!(
                    "providers[{i}] ({}): at least one endpoint is required",
                    p.name
                );
            }
            let resolved = p.resolved_protocol().ok_or_else(|| {
                anyhow::anyhow!(
                    "providers[{i}] ({}): unable to determine protocol from endpoints",
                    p.name
                )
            })?;
            if !p.endpoints.contains_key(resolved) {
                anyhow::bail!(
                    "providers[{i}] ({}): protocol '{}' has no matching endpoint in 'endpoints'",
                    p.name,
                    resolved
                );
            }
            if p.default_protocol.is_none() && p.endpoints.len() > 1 {
                tracing::warn!(
                    "providers[{i}] ({}): 'protocol' not set and 'endpoints' has {} entries; inferring '{}' as default (set 'protocol' explicitly to silence this warning)",
                    p.name,
                    p.endpoints.len(),
                    resolved
                );
            }
        }
        for (i, m) in self.models.iter().enumerate() {
            if m.name.trim().is_empty() {
                anyhow::bail!("models[{i}]: name is required");
            }
            if m.route_type.is_some() {
                tracing::warn!(
                    model = %m.name,
                    "YAML field 'type' (route_type) is no longer supported and will be ignored; \
                     remove it from your config file"
                );
            }
            if m.virtual_model.trim().is_empty() {
                anyhow::bail!("models[{i}] ({}): virtual_model is required", m.name);
            }
            if m.backends.is_empty() {
                anyhow::bail!("models[{i}] ({}): at least one backend is required", m.name);
            }
            for (j, b) in m.backends.iter().enumerate() {
                if !provider_names.contains(&b.provider.as_str()) {
                    anyhow::bail!(
                        "models[{i}] ({}): backends[{j}].provider '{}' not found in providers",
                        m.name,
                        b.provider
                    );
                }
            }
        }
        Ok(())
    }
}

use nyro_core::db::models::{Model, ModelBackend, Provider};

pub fn build_providers(yaml: &YamlConfig) -> Vec<Provider> {
    use nyro_core::protocol::registry::ProtocolRegistry;
    let reg = ProtocolRegistry::global();

    yaml.providers
        .iter()
        .enumerate()
        .map(|(i, yp)| {
            let id = format!("yaml-provider-{i}");
            let raw_protocol = yp.resolved_protocol().unwrap_or_default().to_string();
            let resolved_protocol = reg
                .parse_protocol(&raw_protocol)
                .map(|protocol| protocol.as_str().to_string())
                .unwrap_or(raw_protocol);
            let default_ep = yp
                .endpoints
                .iter()
                .find(|(proto, _)| {
                    reg.parse_protocol(proto)
                        .map(|protocol| protocol.as_str().to_string())
                        .as_deref()
                        == Some(&resolved_protocol)
                })
                .map(|(_, ep)| ep);
            let base_url = default_ep.map(|e| e.base_url.clone()).unwrap_or_default();
            let now = chrono::Utc::now().to_rfc3339();
            Provider {
                id,
                name: yp.name.clone(),
                vendor: yp.vendor.clone(),
                protocol: resolved_protocol,
                base_url,
                preset_key: None,
                channel: None,
                models_source: yp.models_source.clone(),
                static_models: yp.static_models.as_ref().map(|v| v.join("\n")),
                api_key: yp.api_key.clone(),
                auth_mode: "apikey".to_string(),
                use_proxy: yp.use_proxy,
                last_test_success: None,
                last_test_at: None,
                is_enabled: true,
                created_at: now.clone(),
                updated_at: now,
            }
        })
        .collect()
}

pub fn build_models(yaml: &YamlConfig, providers: &[Provider]) -> Vec<Model> {
    let name_to_id: HashMap<&str, &str> = providers
        .iter()
        .map(|p| (p.name.as_str(), p.id.as_str()))
        .collect();

    yaml.models
        .iter()
        .enumerate()
        .map(|(i, ym)| {
            let model_id = format!("yaml-model-{i}");
            let now = chrono::Utc::now().to_rfc3339();

            let backends: Vec<ModelBackend> = ym
                .backends
                .iter()
                .enumerate()
                .map(|(j, yb)| {
                    let provider_id = name_to_id
                        .get(yb.provider.as_str())
                        .unwrap_or(&"")
                        .to_string();
                    ModelBackend {
                        id: format!("{model_id}-backend-{j}"),
                        model_id: model_id.clone(),
                        provider_id,
                        model: yb.model.clone(),
                        weight: yb.weight,
                        priority: yb.priority,
                        created_at: now.clone(),
                    }
                })
                .collect();

            let primary = backends.first();
            Model {
                id: model_id,
                name: ym.name.clone(),
                virtual_model: ym.virtual_model.clone(),
                balance: ym.balance.clone(),
                target_provider: primary.map(|b| b.provider_id.clone()).unwrap_or_default(),
                target_model: primary.map(|b| b.model.clone()).unwrap_or_default(),
                access_control: ym.access_control,
                is_enabled: true,
                created_at: now,
                targets: backends,
            }
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn parse_provider(yaml: &str) -> Result<YamlProvider, serde_yaml::Error> {
        serde_yaml::from_str(yaml)
    }

    #[test]
    fn canonical_names_work() {
        let yaml = r#"
name: openai
default_protocol: openai
endpoints:
  openai:
    base_url: https://api.openai.com/v1
api_key: sk-canonical
"#;
        let p = parse_provider(yaml).expect("should parse");
        assert_eq!(p.default_protocol.as_deref(), Some("openai"));
        assert_eq!(p.api_key, "sk-canonical");
        assert_eq!(p.resolved_protocol(), Some("openai"));
    }

    #[test]
    fn alias_protocol_and_apikey_work() {
        let yaml = r#"
name: openai
protocol: openai
endpoints:
  openai:
    base_url: https://api.openai.com/v1
apikey: sk-alias
"#;
        let p = parse_provider(yaml).expect("should parse");
        assert_eq!(p.default_protocol.as_deref(), Some("openai"));
        assert_eq!(p.api_key, "sk-alias");
    }

    #[test]
    fn omitted_protocol_single_endpoint_is_inferred() {
        let yaml = r#"
name: openai
endpoints:
  openai:
    base_url: https://api.openai.com/v1
api_key: sk-x
"#;
        let p = parse_provider(yaml).expect("should parse");
        assert!(p.default_protocol.is_none());
        assert_eq!(p.resolved_protocol(), Some("openai"));
    }

    #[test]
    fn omitted_protocol_multi_endpoint_uses_first_declared() {
        let yaml = r#"
name: deepseek
endpoints:
  anthropic:
    base_url: https://api.deepseek.com/anthropic
  openai:
    base_url: https://api.deepseek.com/v1
apikey: sk-x
"#;
        let p = parse_provider(yaml).expect("should parse");
        assert!(p.default_protocol.is_none());
        assert_eq!(p.resolved_protocol(), Some("anthropic"));
    }

    #[test]
    fn conflict_default_protocol_and_protocol_rejects() {
        let yaml = r#"
name: openai
default_protocol: openai
protocol: anthropic
endpoints:
  openai:
    base_url: https://api.openai.com/v1
api_key: sk-x
"#;
        let err = parse_provider(yaml).expect_err("should reject").to_string();
        assert!(
            err.contains("default_protocol") && err.contains("protocol"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn conflict_api_key_and_apikey_rejects() {
        let yaml = r#"
name: openai
protocol: openai
endpoints:
  openai:
    base_url: https://api.openai.com/v1
api_key: sk-a
apikey: sk-b
"#;
        let err = parse_provider(yaml).expect_err("should reject").to_string();
        assert!(
            err.contains("api_key") && err.contains("apikey"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn missing_api_key_rejects() {
        let yaml = r#"
name: openai
protocol: openai
endpoints:
  openai:
    base_url: https://api.openai.com/v1
"#;
        let err = parse_provider(yaml).expect_err("should reject").to_string();
        assert!(err.contains("api_key"), "unexpected error: {err}");
    }

    #[test]
    fn validate_accepts_inferred_protocol() {
        let yaml = r#"
providers:
  - name: openai
    endpoints:
      openai:
        base_url: https://api.openai.com/v1
    apikey: sk-x
models:
  - name: gpt-4o
    vmodel: gpt-4o
    backends:
      - provider: openai
        model: gpt-4o
"#;
        let cfg: YamlConfig = serde_yaml::from_str(yaml).expect("parse");
        cfg.validate().expect("validate");
    }

    #[test]
    fn validate_accepts_legacy_routes_key() {
        let yaml = r#"
providers:
  - name: openai
    endpoints:
      openai:
        base_url: https://api.openai.com/v1
    apikey: sk-x
routes:
  - name: gpt-4o
    vmodel: gpt-4o
    targets:
      - provider: openai
        model: gpt-4o
"#;
        let cfg: YamlConfig = serde_yaml::from_str(yaml).expect("parse");
        cfg.validate().expect("validate");
    }

    #[test]
    fn build_providers_normalizes_legacy_protocol_keys_to_canonical_suite() {
        let yaml = r#"
providers:
  - name: vendor1
    protocol: openai
    endpoints:
      openai:
        base_url: https://a.example/v1
      anthropic:
        base_url: https://b.example/v1
    api_key: sk-x
"#;
        let cfg: YamlConfig = serde_yaml::from_str(yaml).expect("parse");
        cfg.validate().expect("validate");
        let providers = build_providers(&cfg);
        assert_eq!(providers.len(), 1);
        let p = &providers[0];
        assert_eq!(p.protocol, "openai-compatible");
        assert_eq!(p.base_url, "https://a.example/v1");
    }

    #[test]
    fn validate_rejects_unknown_protocol_without_matching_endpoint() {
        let yaml = r#"
providers:
  - name: openai
    protocol: gemini
    endpoints:
      openai:
        base_url: https://api.openai.com/v1
    api_key: sk-x
"#;
        let cfg: YamlConfig = serde_yaml::from_str(yaml).expect("parse");
        let err = cfg.validate().unwrap_err().to_string();
        assert!(
            err.contains("protocol 'gemini'") && err.contains("no matching endpoint"),
            "unexpected error: {err}"
        );
    }

    #[test]
    fn deprecated_route_type_field_is_silently_parsed() {
        let yaml = r#"
providers:
  - name: openai
    endpoints:
      openai:
        base_url: https://api.openai.com/v1
    apikey: sk-x
models:
  - name: embeddings
    vmodel: text-embedding-3-small
    type: embedding
    backends:
      - provider: openai
        model: text-embedding-3-small
  - name: chat
    vmodel: gpt-4o
    route_type: chat
    backends:
      - provider: openai
        model: gpt-4o
"#;
        let cfg: YamlConfig = serde_yaml::from_str(yaml).expect("parse");
        cfg.validate()
            .expect("validate must succeed for deprecated type field");
        assert_eq!(cfg.models.len(), 2);

        let providers = build_providers(&cfg);
        let models = build_models(&cfg, &providers);
        assert_eq!(models.len(), 2);
        assert_eq!(models[0].name, "embeddings");
        assert_eq!(models[1].name, "chat");
    }

    #[test]
    fn deprecated_capabilities_source_field_is_silently_parsed() {
        let yaml = r#"
providers:
  - name: openai
    endpoints:
      openai:
        base_url: https://api.openai.com/v1
    apikey: sk-x
    capabilities_source: models.dev
models:
  - name: chat
    vmodel: gpt-4o
    backends:
      - provider: openai
        model: gpt-4o
"#;
        let cfg: YamlConfig = serde_yaml::from_str(yaml).expect("parse");
        cfg.validate()
            .expect("validate must succeed for deprecated capabilities_source");

        let providers = build_providers(&cfg);
        assert_eq!(providers.len(), 1);
        assert_eq!(providers[0].name, "openai");
    }

    #[test]
    fn both_deprecated_fields_together_are_accepted() {
        let yaml = r#"
providers:
  - name: openai
    endpoints:
      openai:
        base_url: https://api.openai.com/v1
    apikey: sk-x
    capabilities_source: http
models:
  - name: embeddings
    vmodel: text-embedding-3-small
    type: embedding
    backends:
      - provider: openai
        model: text-embedding-3-small
"#;
        let cfg: YamlConfig = serde_yaml::from_str(yaml).expect("parse");
        cfg.validate().expect("validate");
        let providers = build_providers(&cfg);
        let models = build_models(&cfg, &providers);
        assert_eq!(providers.len(), 1);
        assert_eq!(models.len(), 1);
    }
}
