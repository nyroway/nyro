//! Validated draft edits and credential-free projections for the control API.

use crate::{Error, Snapshot, Store};
use nyro_config::Config;
use nyro_llm::config::{Model, OpenAiApi, Provider, ProviderKind, Transport};
use nyro_security::ApiKey;
use serde::Deserialize;
use serde_json::{Map, Value, json};

#[derive(Clone, Copy)]
pub enum EntityKind {
    Provider,
    Model,
    ApiKey,
}

/// Omission preserves a credential. Clearing must always be explicit.
#[derive(Default, Deserialize)]
#[serde(
    tag = "action",
    content = "value",
    rename_all = "snake_case",
    deny_unknown_fields
)]
pub enum CredentialChange {
    #[default]
    Keep,
    Set(String),
    Clear,
}

impl CredentialChange {
    fn optional(self, previous: Option<String>) -> Option<String> {
        match self {
            Self::Keep => previous,
            Self::Set(value) => Some(value),
            Self::Clear => None,
        }
    }
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ProviderInput {
    pub kind: ProviderKind,
    pub base_url: String,
    #[serde(default)]
    pub native_chat: bool,
    #[serde(default)]
    pub api: Option<OpenAiApi>,
    #[serde(default)]
    pub api_key: CredentialChange,
    #[serde(default)]
    pub transport: TransportInput,
}

#[derive(Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct TransportInput {
    #[serde(default)]
    pub proxy_url: CredentialChange,
    #[serde(default)]
    pub http1_only: bool,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ApiKeyInput {
    #[serde(default)]
    pub secret: CredentialChange,
    #[serde(default = "default_enabled")]
    pub enabled: bool,
    #[serde(default)]
    pub expires_at: Option<u64>,
}

const fn default_enabled() -> bool {
    true
}

pub enum EntityValue {
    Provider(ProviderInput),
    Model(Model),
    ApiKey(ApiKeyInput),
}

impl EntityValue {
    pub fn from_json(kind: EntityKind, value: Value) -> Result<Self, Error> {
        match kind {
            EntityKind::Provider => serde_json::from_value(value).map(Self::Provider),
            EntityKind::Model => serde_json::from_value(value).map(Self::Model),
            EntityKind::ApiKey => serde_json::from_value(value).map(Self::ApiKey),
        }
        .map_err(|_| Error::Invalid)
    }

    fn kind(&self) -> EntityKind {
        match self {
            Self::Provider(_) => EntityKind::Provider,
            Self::Model(_) => EntityKind::Model,
            Self::ApiKey(_) => EntityKind::ApiKey,
        }
    }
}

pub enum EntityChange {
    Create { id: String, value: EntityValue },
    Replace { id: String, value: EntityValue },
    Delete { kind: EntityKind, id: String },
}

impl Store {
    /// Apply one edit to the draft, preserving publication and all state on failure.
    pub async fn edit(
        &mut self,
        expected_revision: u64,
        change: EntityChange,
    ) -> Result<u64, Error> {
        let mut draft = self.state().await?.draft;
        if draft.revision != expected_revision {
            return Err(Error::Conflict);
        }
        let (kind, id) = match &change {
            EntityChange::Create { id, value } | EntityChange::Replace { id, value } => {
                (value.kind(), id)
            }
            EntityChange::Delete { kind, id } => (*kind, id),
        };
        let config = &mut draft.config;
        let exists = match kind {
            EntityKind::Provider => config.llm.providers.contains_key(id),
            EntityKind::Model => config.llm.models.contains_key(id),
            EntityKind::ApiKey => config.security.api_keys.iter().any(|key| key.id == *id),
        };
        if matches!(change, EntityChange::Create { .. }) {
            if exists {
                return Err(Error::AlreadyExists);
            }
        } else if !exists {
            return Err(Error::NotFound);
        }
        match change {
            EntityChange::Create { id, value } | EntityChange::Replace { id, value } => {
                replace(config, id, value)?;
            }
            EntityChange::Delete { kind, id } => {
                match kind {
                    EntityKind::Provider => {
                        if config.llm.models.values().any(|model| {
                            model.backends.iter().any(|backend| backend.provider == id)
                        }) {
                            return Err(Error::Referenced);
                        }
                        config.llm.providers.remove(&id);
                    }
                    EntityKind::Model => {
                        config.llm.models.remove(&id);
                    }
                    EntityKind::ApiKey => {
                        if config.llm.subject_limits.contains_key(&id)
                            || config
                                .llm
                                .models
                                .values()
                                .any(|model| model.subjects.contains(&id))
                        {
                            return Err(Error::Referenced);
                        }
                        config.security.api_keys.retain(|key| key.id != id);
                    }
                }
            }
        }
        Ok(self.save(expected_revision, config).await?.revision)
    }
}

fn replace(config: &mut Config, id: String, value: EntityValue) -> Result<(), Error> {
    match value {
        EntityValue::Provider(input) => {
            let previous = config.llm.providers.remove(&id);
            let (api_key, proxy_url) = previous
                .map(|provider| (provider.api_key, provider.transport.proxy_url))
                .unwrap_or_default();
            config.llm.providers.insert(
                id,
                Provider {
                    kind: input.kind,
                    base_url: input.base_url,
                    native_chat: input.native_chat,
                    api: input.api,
                    api_key: input.api_key.optional(api_key),
                    transport: Transport {
                        proxy_url: input.transport.proxy_url.optional(proxy_url),
                        http1_only: input.transport.http1_only,
                    },
                },
            );
        }
        EntityValue::Model(model) => {
            config.llm.models.insert(id, model);
        }
        EntityValue::ApiKey(input) => {
            let previous = config.security.api_keys.iter_mut().find(|key| key.id == id);
            let secret = match input.secret {
                CredentialChange::Keep => previous.as_ref().ok_or(Error::Invalid)?.secret.clone(),
                CredentialChange::Set(secret) => secret,
                CredentialChange::Clear => return Err(Error::Invalid),
            };
            let key = ApiKey {
                id,
                secret,
                enabled: input.enabled,
                expires_at: input.expires_at,
            };
            match previous {
                Some(previous) => *previous = key,
                None => config.security.api_keys.push(key),
            }
        }
    }
    Ok(())
}

impl Snapshot {
    /// Read-only projection: credential presence flags cannot be written as configuration.
    pub fn redacted(&self) -> Value {
        let providers: Map<String, Value> = self
            .config
            .llm
            .providers
            .iter()
            .map(|(id, provider)| (id.clone(), provider_view(provider)))
            .collect();
        let mut keys: Vec<_> = self.config.security.api_keys.iter().collect();
        keys.sort_by(|left, right| left.id.cmp(&right.id));
        let keys: Vec<_> = keys
            .into_iter()
            .map(|key| {
                json!({
                    "id": key.id,
                    "enabled": key.enabled,
                    "expires_at": key.expires_at,
                    "has_secret": true,
                })
            })
            .collect();
        json!({
            "revision": self.revision,
            "config": {
                "server": self.config.server,
                "llm": {
                    "providers": providers,
                    "models": self.config.llm.models,
                    "subject_limits": self.config.llm.subject_limits,
                },
                "security": {"api_keys": keys},
                "limit": self.config.limit,
            },
        })
    }

    /// Return entities sorted by ID, or one entity. Credentials are never projected.
    pub fn entities(&self, kind: EntityKind, id: Option<&str>) -> Result<Value, Error> {
        let mut items: Vec<(String, Value)> = match kind {
            EntityKind::Provider => self
                .config
                .llm
                .providers
                .iter()
                .map(|(id, provider)| (id.clone(), provider_view(provider)))
                .collect(),
            EntityKind::Model => self
                .config
                .llm
                .models
                .iter()
                .map(|(id, model)| (id.clone(), json!(model)))
                .collect(),
            EntityKind::ApiKey => self
                .config
                .security
                .api_keys
                .iter()
                .map(|key| {
                    (
                        key.id.clone(),
                        json!({
                            "enabled": key.enabled,
                            "expires_at": key.expires_at,
                            "has_secret": true,
                        }),
                    )
                })
                .collect(),
        };
        if let Some(id) = id {
            let (_, value) = items
                .into_iter()
                .find(|(item_id, _)| item_id == id)
                .ok_or(Error::NotFound)?;
            Ok(json!({"draft_revision": self.revision, "item": {"id": id, "value": value}}))
        } else {
            items.sort_by(|left, right| left.0.cmp(&right.0));
            let items: Vec<_> = items
                .into_iter()
                .map(|(id, value)| json!({"id": id, "value": value}))
                .collect();
            Ok(json!({"draft_revision": self.revision, "items": items}))
        }
    }
}

fn provider_view(provider: &Provider) -> Value {
    json!({
        "kind": provider.kind,
        "base_url": provider.base_url,
        "native_chat": provider.native_chat,
        "api": provider.api,
        "has_api_key": provider.api_key.is_some(),
        "transport": {
            "has_proxy_url": provider.transport.proxy_url.is_some(),
            "http1_only": provider.transport.http1_only,
        },
    })
}
