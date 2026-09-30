//! Existing wire fixtures share the new actual-usage limiter; no production compatibility parser.
#![allow(dead_code)]
use nyro_limit::{
    ConcurrencyLimit,
    token::{Registry, Rule},
};
use nyro_llm::runtime::{Options, Policies, Runtime};
use serde_json::Value;
use std::{sync::Arc, time::Duration};
#[derive(Clone, Default)]
pub struct SharedResources {
    pub runtime: nyro_llm::runtime::SharedResources,
    pub quotas: Ledger,
}
#[derive(Clone, Default)]
pub struct Ledger(pub Registry);
pub struct Balance {
    pub used: u128,
    pub reserved: u64,
}
impl Ledger {
    pub fn snapshot(&self, scope: &str) -> Option<Balance> {
        // Observe through public admission: token-only checks never charge another request.
        let mut low = 0u64;
        let mut high = u64::MAX;
        while low < high {
            let mid = low + (high - low) / 2 + 1;
            let policy = self
                .0
                .bind(
                    scope,
                    vec![],
                    vec![Rule {
                        limit: mid,
                        window: Duration::from_secs(3600),
                    }],
                )
                .unwrap();
            if self.0.admit(&[&policy]).is_err() {
                low = mid;
            } else {
                high = mid - 1;
            }
        }
        Some(Balance {
            used: low.into(),
            reserved: 0,
        })
    }
}
pub fn runtime(
    mut value: Value,
    keys: Arc<dyn nyro_authn::Authenticator>,
    limit: ConcurrencyLimit,
    options: Options,
    shared: SharedResources,
) -> Runtime {
    let mut policies = Policies {
        registry: shared.quotas.0.clone(),
        ..Default::default()
    };
    for (id, model) in value["models"].as_object_mut().unwrap() {
        if let Some(budget) = model.as_object_mut().unwrap().remove("quota") {
            let limit = budget["total_tokens"].as_u64().unwrap();
            policies.models.insert(
                id.clone(),
                policies
                    .registry
                    .bind(
                        id,
                        vec![],
                        vec![Rule {
                            limit,
                            window: Duration::from_secs(3600),
                        }],
                    )
                    .unwrap(),
            );
        }
    }
    Runtime::with_policies(
        serde_json::from_value(value).unwrap(),
        keys,
        limit,
        options,
        shared.runtime,
        policies,
    )
    .unwrap()
}
