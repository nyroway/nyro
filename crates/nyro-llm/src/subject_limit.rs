//! Subject request histories retained across immutable runtime generations.

use crate::{config::SubjectLimitConfig, runtime::BuildError};
use nyro_limit::request::RequestLimit;
use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
};

/// Retains active bindings and recent admissions even after a subject/policy is removed.
/// Changed live or unexpired policies reject candidate construction. Empty retired
/// bindings are reclaimed on the next bind, including unused failed candidates.
#[derive(Default)]
pub struct SubjectLimitRegistry {
    entries: Mutex<HashMap<String, Arc<BoundSubjectLimit>>>,
}

impl SubjectLimitRegistry {
    pub(crate) fn bind(
        &self,
        subject: &str,
        policy: &SubjectLimitConfig,
    ) -> Result<Arc<BoundSubjectLimit>, BuildError> {
        if subject.trim().is_empty() || !policy.valid() {
            return Err(BuildError);
        }
        let mut entries = self.entries.lock().unwrap();
        entries.retain(|_, bound| Arc::strong_count(bound) > 1 || !bound.limit.is_empty());
        if let Some(bound) = entries.get(subject) {
            return if bound.policy == *policy {
                Ok(Arc::clone(bound))
            } else {
                Err(BuildError)
            };
        }
        let bound = Arc::new(BoundSubjectLimit {
            policy: policy.clone(),
            limit: RequestLimit::new(policy.windows()).map_err(|_| BuildError)?,
        });
        entries.insert(subject.into(), Arc::clone(&bound));
        Ok(bound)
    }
}

pub(crate) struct BoundSubjectLimit {
    policy: SubjectLimitConfig,
    pub(crate) limit: RequestLimit,
}

#[cfg(test)]
mod tests {
    use super::*;

    fn policy() -> SubjectLimitConfig {
        SubjectLimitConfig {
            rpm: Some(1),
            rpd: None,
        }
    }

    #[test]
    fn consumed_retired_binding_survives_and_changed_candidates_cannot_reset_it() {
        let registry = SubjectLimitRegistry::default();
        let original = registry.bind("alice", &policy()).unwrap();
        original.limit.try_acquire(None).unwrap();
        let candidate = registry.bind("alice", &policy()).unwrap();
        assert!(Arc::ptr_eq(&candidate, &original));
        drop(candidate);
        drop(original);
        assert!(
            registry
                .bind(
                    "alice",
                    &SubjectLimitConfig {
                        rpm: Some(2),
                        rpd: None
                    }
                )
                .is_err()
        );
        assert!(
            registry
                .bind("alice", &policy())
                .unwrap()
                .limit
                .try_acquire(None)
                .is_err()
        );
        registry
            .bind("bob", &policy())
            .unwrap()
            .limit
            .try_acquire(None)
            .unwrap();
    }

    #[test]
    fn live_unused_binding_pins_policy_but_failed_candidate_without_admissions_does_not() {
        let registry = SubjectLimitRegistry::default();
        let original = registry.bind("alice", &policy()).unwrap();
        let changed = SubjectLimitConfig {
            rpm: Some(2),
            rpd: None,
        };
        assert!(registry.bind("alice", &changed).is_err());
        drop(original);
        let replacement = registry.bind("alice", &changed).unwrap();
        replacement.limit.try_acquire(None).unwrap();
        replacement.limit.try_acquire(None).unwrap();
        assert!(replacement.limit.try_acquire(None).is_err());
    }

    #[test]
    fn expired_retired_binding_is_reclaimed_on_next_bind() {
        let registry = SubjectLimitRegistry::default();
        // Seed real expired state without waiting a minute or adding a production clock API.
        let bound = Arc::new(BoundSubjectLimit {
            policy: policy(),
            limit: RequestLimit::new([(1, std::time::Duration::from_nanos(1))]).unwrap(),
        });
        bound.limit.try_acquire(None).unwrap();
        registry
            .entries
            .lock()
            .unwrap()
            .insert("alice".into(), bound);
        let changed = SubjectLimitConfig {
            rpm: Some(2),
            rpd: None,
        };
        let replacement = registry.bind("alice", &changed).unwrap();
        replacement.limit.try_acquire(None).unwrap();
        replacement.limit.try_acquire(None).unwrap();
        assert!(replacement.limit.try_acquire(None).is_err());
    }
}
