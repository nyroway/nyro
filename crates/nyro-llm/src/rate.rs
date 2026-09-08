//! Model request rate state shared explicitly across runtime generations.

use crate::{config::RateConfig, runtime::BuildError};
use nyro_limit::rate::{RateExceeded, RateLimit};
use std::{
    collections::HashMap,
    sync::{Arc, Mutex, Weak},
    time::Duration,
};

/// Reuses active model buckets without retaining retired runtime generations.
/// Changing an active model's rate policy requires restarting the process.
#[derive(Default)]
pub struct RateRegistry {
    entries: Mutex<HashMap<String, Weak<BoundRate>>>,
}

impl RateRegistry {
    pub(crate) fn bind(
        &self,
        model: &str,
        policy: &RateConfig,
    ) -> Result<Arc<BoundRate>, BuildError> {
        let mut entries = self.entries.lock().unwrap();
        entries.retain(|_, entry| entry.strong_count() > 0);
        if let Some(bound) = entries.get(model).and_then(Weak::upgrade) {
            // Never reset admitted credit during a candidate build, including a
            // candidate that fails later. Provider/routing changes do not rekey it.
            return if bound.policy == *policy {
                Ok(bound)
            } else {
                Err(BuildError)
            };
        }
        let limit = RateLimit::new(
            policy.requests,
            Duration::from_millis(policy.period_ms),
            policy.burst,
        )
        .map_err(|_| BuildError)?;
        let bound = Arc::new(BoundRate {
            policy: policy.clone(),
            limit,
        });
        entries.insert(model.into(), Arc::downgrade(&bound));
        Ok(bound)
    }
}

pub(crate) struct BoundRate {
    policy: RateConfig,
    limit: RateLimit,
}

impl BoundRate {
    pub(crate) fn try_acquire(&self) -> Result<(), RateExceeded> {
        self.limit.try_acquire()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn policy() -> RateConfig {
        RateConfig {
            requests: 1,
            period_ms: 3_600_000,
            burst: 1,
        }
    }

    #[test]
    fn unchanged_model_binding_reuses_depleted_bucket() {
        let registry = RateRegistry::default();
        let first = registry.bind("chat", &policy()).unwrap();
        first.try_acquire().unwrap();
        let candidate = registry.bind("chat", &policy()).unwrap();
        assert!(Arc::ptr_eq(&first, &candidate));
        assert!(candidate.try_acquire().is_err());
        // Dropping a failed candidate cannot reset the active generation.
        drop(candidate);
        assert!(
            registry
                .bind("chat", &policy())
                .unwrap()
                .try_acquire()
                .is_err()
        );
    }

    #[test]
    fn changed_active_policy_is_rejected_without_resetting_old_bucket() {
        let registry = RateRegistry::default();
        let original = policy();
        let first = registry.bind("chat", &original).unwrap();
        first.try_acquire().unwrap();
        for changed in [
            RateConfig {
                requests: 2,
                ..original.clone()
            },
            RateConfig {
                period_ms: 7_200_000,
                ..original.clone()
            },
            RateConfig {
                burst: 2,
                ..original.clone()
            },
        ] {
            assert!(registry.bind("chat", &changed).is_err());
            assert!(first.try_acquire().is_err());
            assert!(
                registry
                    .bind("chat", &original)
                    .unwrap()
                    .try_acquire()
                    .is_err()
            );
        }
    }

    #[test]
    fn aliases_and_independent_registries_have_separate_buckets() {
        let registry = RateRegistry::default();
        let first = registry.bind("chat", &policy()).unwrap();
        first.try_acquire().unwrap();
        let alias = registry.bind("alias", &policy()).unwrap();
        alias.try_acquire().unwrap();
        RateRegistry::default()
            .bind("chat", &policy())
            .unwrap()
            .try_acquire()
            .unwrap();
        assert!(first.try_acquire().is_err());
    }

    #[test]
    fn retired_bindings_are_pruned_and_allow_a_new_policy() {
        let registry = RateRegistry::default();
        let first = registry.bind("chat", &policy()).unwrap();
        let retired = Arc::downgrade(&first);
        first.try_acquire().unwrap();
        drop(first);
        assert!(retired.upgrade().is_none());
        let alias = registry.bind("alias", &policy()).unwrap();
        assert_eq!(registry.entries.lock().unwrap().len(), 1);
        let changed = RateConfig {
            burst: 2,
            ..policy()
        };
        let renewed = registry.bind("chat", &changed).unwrap();
        renewed.try_acquire().unwrap();
        renewed.try_acquire().unwrap();
        assert!(renewed.try_acquire().is_err());
        drop(alias);
    }

    #[test]
    fn invalid_policy_does_not_create_a_binding() {
        let registry = RateRegistry::default();
        assert!(
            registry
                .bind(
                    "chat",
                    &RateConfig {
                        requests: 0,
                        ..policy()
                    }
                )
                .is_err()
        );
        let valid = registry.bind("chat", &policy()).unwrap();
        valid.try_acquire().unwrap();
    }
}
