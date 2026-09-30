//! Compile public resources into application-local settings. No runtime or database is created here.
use crate::resources::*;
use nyro_llm::{config as llm, runtime as llm_runtime};
use nyro_mcp::{config as mcp, runtime as mcp_runtime};
use serde::{Deserialize, Serialize};
use std::{collections::BTreeMap, sync::Arc};

#[derive(Clone, Default, Debug, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Identities {
    pub upstreams: BTreeMap<String, String>,
    pub models: BTreeMap<String, String>,
    pub mcps: BTreeMap<String, String>,
    pub consumers: BTreeMap<String, String>,
}
/// Trusted effective resources, with stable control-plane identities separate from public IDs.
#[derive(Clone, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Snapshot {
    pub resources: Resources,
    pub identities: Identities,
}
impl Snapshot {
    pub fn file(resources: Resources) -> Self {
        fn map<'a>(kind: &str, ids: impl Iterator<Item = &'a str>) -> BTreeMap<String, String> {
            ids.map(|id| (id.into(), format!("{kind}:{id}"))).collect()
        }
        let identities = Identities {
            upstreams: map(
                "upstreams",
                resources.upstreams.iter().map(|r| r.id.as_str()),
            ),
            models: map("models", resources.models.iter().map(|r| r.id.as_str())),
            mcps: map("mcps", resources.mcps.iter().map(|r| r.id.as_str())),
            consumers: map(
                "consumers",
                resources.consumers.iter().map(|r| r.id.as_str()),
            ),
        };
        Self {
            resources,
            identities,
        }
    }
    pub fn validate(&self) -> Result<(), Error> {
        self.resources.validate()?;
        let expected = Self::file(self.resources.clone()).identities;
        let mut unique = std::collections::BTreeSet::new();
        for (actual, expected) in [
            (&self.identities.upstreams, &expected.upstreams),
            (&self.identities.models, &expected.models),
            (&self.identities.mcps, &expected.mcps),
            (&self.identities.consumers, &expected.consumers),
        ] {
            if !actual.keys().eq(expected.keys())
                || actual
                    .values()
                    .any(|uid| uid.is_empty() || uid.len() > 256 || !unique.insert(uid))
            {
                return Err(Error("invalid resource identities"));
            }
        }
        Ok(())
    }
    pub fn compile(
        &self,
        limits: &nyro_limit::token::Registry,
        pools: mcp_runtime::Pools,
    ) -> Result<Compiled, Error> {
        self.validate()?;
        let resources = &self.resources;
        let mut config = llm::Config::default();
        let mut policies = llm_runtime::Policies {
            registry: limits.clone(),
            ..Default::default()
        };
        let mut mcp_config = mcp::Config::default();
        let mut mcp_policies = mcp_runtime::Policies {
            registry: limits.clone(),
            pools,
            ..Default::default()
        };
        let mut credentials = Vec::new();
        for consumer in &resources.consumers {
            let uid = &self.identities.consumers[&consumer.id];
            for credential in &consumer.credentials {
                credentials.push((
                    uid.clone(),
                    nyro_authn::KeyCredential {
                        id: serde_json::to_string(&(uid, &credential.id)).unwrap(),
                        secret: credential.secret.clone(),
                        enabled: true,
                        expires_at: None,
                    },
                ));
            }
            policies.consumers.insert(
                uid.clone(),
                bind(limits, &format!("{uid}/llm"), &consumer.limits.llm)?,
            );
            mcp_policies.consumers.insert(
                uid.clone(),
                bind(limits, &format!("{uid}/mcp"), &consumer.limits.mcp)?,
            );
        }
        let subjects = |id: &str, mode: AccessMode, mcp: bool| -> Vec<String> {
            resources
                .consumers
                .iter()
                .filter(|consumer| {
                    mode == AccessMode::Authenticated
                        || (mode == AccessMode::Restricted
                            && if mcp {
                                &consumer.grants.mcps
                            } else {
                                &consumer.grants.models
                            }
                            .iter()
                            .any(|v| v == id))
                })
                .map(|c| self.identities.consumers[&c.id].clone())
                .collect()
        };
        let mut options = llm_runtime::Options::default();
        for model in &resources.models {
            let pool = resources.pool(&model.upstream, Kind::Llm)?;
            let pool_uid = &self.identities.upstreams[&pool.id];
            let mut backends = Vec::new();
            for target in &pool.targets {
                let provider_id = serde_json::to_string(&(pool_uid, &target.id)).unwrap();
                config
                    .providers
                    .insert(provider_id.clone(), provider(target)?);
                backends.push(llm::Backend {
                    id: target.id.clone(),
                    provider: provider_id,
                    upstream_model: target.model.clone().unwrap(),
                    weight: target.weight.unwrap_or(1),
                    priority: target.priority,
                });
            }
            let execution = execution(&model.execution, llm_runtime::Options::default())?;
            options.request_timeout = options.request_timeout.max(execution.request_timeout);
            options.max_body_bytes = options.max_body_bytes.max(execution.max_body_bytes);
            options.max_response_bytes =
                options.max_response_bytes.max(execution.max_response_bytes);
            options.max_frame_bytes = options.max_frame_bytes.max(execution.max_frame_bytes);
            policies.execution.insert(model.id.clone(), execution);
            policies
                .routing_scopes
                .insert(model.id.clone(), pool_uid.clone());
            policies.models.insert(
                model.id.clone(),
                bind(limits, &self.identities.models[&model.id], &model.limits)?,
            );
            config.models.insert(
                model.id.clone(),
                llm::Model {
                    strategy: match pool.balance {
                        nyro_balance::Strategy::WeightedRoundrobin => {
                            llm::Strategy::WeightedRoundrobin
                        }
                        nyro_balance::Strategy::WeightedRandom => llm::Strategy::Weighted,
                        nyro_balance::Strategy::LeastRecent => llm::Strategy::LeastRecent,
                        nyro_balance::Strategy::LatencyAware => llm::Strategy::Latency,
                    },
                    backends,
                    max_attempts: model.execution.max_attempts.unwrap_or(1),
                    health: None,
                    workloads: vec![match model.capability {
                        Capability::Chat => nyro_llm::Workload::Chat,
                        Capability::Embedding => nyro_llm::Workload::Embedding,
                    }],
                    allow_anonymous: model.access.mode == AccessMode::Anonymous,
                    subjects: subjects(&model.id, model.access.mode, false)
                        .into_iter()
                        .collect(),
                },
            );
        }
        for service in &resources.mcps {
            let pool = resources.pool(&service.upstream, Kind::Mcp)?;
            let execution = execution(
                &service.execution,
                llm_runtime::Options {
                    request_timeout: std::time::Duration::from_secs(30),
                    ..Default::default()
                },
            )?;
            let mut local = mcp::Config {
                request_timeout_ms: u64::try_from(execution.request_timeout.as_millis().max(1))
                    .map_err(|_| Error("MCP timeout exceeds supported range"))?,
                max_body_bytes: execution.max_body_bytes,
                max_response_bytes: execution.max_response_bytes,
                max_frame_bytes: execution.max_frame_bytes,
                ..Default::default()
            };
            let targets: Vec<_> = pool
                .targets
                .iter()
                .map(|target| mcp_runtime::Target {
                    id: target.id.clone(),
                    weight: target.weight.unwrap_or(1),
                    server: mcp::Server {
                        transport: mcp::Transport::Http,
                        url: target.url.clone().unwrap(),
                        bearer_token: None,
                        auth: target.auth.clone(),
                        subjects: subjects(&service.id, service.access.mode, true),
                        allowed_tools: service.allowed_tools.clone(),
                    },
                })
                .collect();
            local
                .servers
                .insert(service.id.clone(), targets[0].server.clone());
            local
                .validate()
                .map_err(|_| Error("invalid MCP runtime settings"))?;
            mcp_config.request_timeout_ms =
                mcp_config.request_timeout_ms.max(local.request_timeout_ms);
            mcp_config
                .servers
                .insert(service.id.clone(), targets[0].server.clone());
            mcp_policies.services.insert(
                service.id.clone(),
                mcp_runtime::ServicePolicy {
                    anonymous: service.access.mode == AccessMode::Anonymous,
                    pool: self.identities.upstreams[&pool.id].clone(),
                    strategy: pool.balance,
                    targets,
                    limits: bind(limits, &self.identities.mcps[&service.id], &service.limits)?,
                    execution: Arc::new(local),
                },
            );
        }
        config
            .validate()
            .map_err(|_| Error("invalid LLM runtime settings"))?;
        let keys = nyro_authn::KeyAuth::for_subjects(credentials)
            .map_err(|_| Error("invalid consumer credentials"))?;
        Ok(Compiled {
            llm: config,
            llm_policies: policies,
            options,
            mcp: mcp_config,
            mcp_policies,
            keys,
        })
    }
}
pub struct Compiled {
    pub llm: llm::Config,
    pub llm_policies: llm_runtime::Policies,
    pub options: llm_runtime::Options,
    pub mcp: mcp::Config,
    pub mcp_policies: mcp_runtime::Policies,
    pub keys: nyro_authn::KeyAuth,
}
fn bind(
    registry: &nyro_limit::token::Registry,
    scope: &str,
    limits: &Limits,
) -> Result<nyro_limit::token::Policy, Error> {
    let rules = |rules: &[Window]| {
        rules
            .iter()
            .map(|r| {
                Ok(nyro_limit::token::Rule {
                    limit: r.limit,
                    window: r.period()?,
                })
            })
            .collect::<Result<Vec<_>, Error>>()
    };
    registry
        .bind(scope, rules(&limits.request)?, rules(&limits.token)?)
        .map_err(|_| Error("invalid limit settings"))
}
fn execution(
    value: &Execution,
    mut base: llm_runtime::Options,
) -> Result<llm_runtime::Options, Error> {
    if let Some(seconds) = value.request_timeout {
        base.request_timeout = std::time::Duration::try_from_secs_f64(seconds)
            .map_err(|_| Error("invalid timeout"))?;
    }
    if let Some(n) = value.max_body_bytes {
        base.max_body_bytes = n;
    }
    if let Some(n) = value.max_response_bytes {
        base.max_response_bytes = n;
    }
    if let Some(n) = value.max_frame_bytes {
        base.max_frame_bytes = n;
    }
    Ok(base)
}

fn provider(target: &Target) -> Result<llm::Provider, Error> {
    let (kind, api) = match target.protocol.ok_or(Error("missing LLM protocol"))? {
        Protocol::OpenAiChat | Protocol::OpenAiEmbeddings => (llm::ProviderKind::Openai, None),
        Protocol::OpenAiResponses => (llm::ProviderKind::Openai, Some(llm::OpenAiApi::Responses)),
        Protocol::AnthropicMessages => (llm::ProviderKind::Anthropic, None),
        Protocol::GeminiGenerateContent => (llm::ProviderKind::Gemini, None),
    };
    Ok(llm::Provider {
        kind,
        api,
        native_chat: true,
        base_url: target
            .base_url
            .clone()
            .ok_or(Error("missing LLM base_url"))?,
        api_key: None,
        auth: target.auth.clone(),
        transport: target.egress.clone(),
    })
}

pub(crate) fn validate_llm_target(target: &Target) -> Result<(), Error> {
    // Use the application's own validation even when no model references this pool yet.
    let mut config = llm::Config::default();
    config.providers.insert("target".into(), provider(target)?);
    config.models.insert(
        "validation".into(),
        llm::Model {
            strategy: llm::Strategy::WeightedRoundrobin,
            backends: vec![llm::Backend {
                id: "target".into(),
                provider: "target".into(),
                upstream_model: target.model.clone().ok_or(Error("missing backend model"))?,
                weight: 1,
                priority: 0,
            }],
            max_attempts: 1,
            health: None,
            workloads: vec![if target.protocol == Some(Protocol::OpenAiEmbeddings) {
                nyro_llm::Workload::Embedding
            } else {
                nyro_llm::Workload::Chat
            }],
            allow_anonymous: false,
            subjects: Default::default(),
        },
    );
    config
        .validate()
        .map_err(|_| Error("invalid LLM target settings"))
}
