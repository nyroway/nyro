//! Subject request and token histories retained across immutable runtime generations.

use crate::{config::SubjectLimitConfig, runtime::BuildError};
use nyro_limit::{
    quota::QuotaSnapshot,
    rate::{RateExceeded, RateLimit},
    request::RequestLimit,
    window::WindowQuota,
};
use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
};

/// Retains active bindings, pending tokens and recent usage after subject/policy removal.
/// Changed live, pending or unexpired policies reject candidate construction. Empty retired
/// bindings are reclaimed on the next bind, including unused failed candidates.
#[derive(Default)]
pub struct SubjectLimitRegistry {
    entries: Mutex<HashMap<String, Arc<BoundSubjectLimit>>>,
}

impl SubjectLimitRegistry {
    /// TPM then TPD snapshots, omitting unconfigured windows.
    pub fn token_snapshots(&self, subject: &str) -> Option<Vec<QuotaSnapshot>> {
        self.entries
            .lock()
            .unwrap()
            .get(subject)
            .and_then(|bound| bound.tokens.as_ref().map(WindowQuota::snapshots))
    }

    pub(crate) fn bind(
        &self,
        subject: &str,
        policy: &SubjectLimitConfig,
    ) -> Result<Arc<BoundSubjectLimit>, BuildError> {
        if subject.trim().is_empty() || !policy.valid() {
            return Err(BuildError);
        }
        let mut entries = self.entries.lock().unwrap();
        entries.retain(|_, bound| Arc::strong_count(bound) > 1 || !bound.is_empty());
        if let Some(bound) = entries.get(subject) {
            return if bound.policy == *policy {
                Ok(Arc::clone(bound))
            } else {
                Err(BuildError)
            };
        }
        let bound = Arc::new(BoundSubjectLimit {
            policy: policy.clone(),
            requests: if policy.rpm.is_some() || policy.rpd.is_some() {
                Some(RequestLimit::new(policy.windows()).map_err(|_| BuildError)?)
            } else {
                None
            },
            tokens: if policy.reserve_tokens.is_some() {
                Some(WindowQuota::new(policy.token_windows()).map_err(|_| BuildError)?)
            } else {
                None
            },
        });
        entries.insert(subject.into(), Arc::clone(&bound));
        Ok(bound)
    }
}

pub(crate) struct BoundSubjectLimit {
    policy: SubjectLimitConfig,
    requests: Option<RequestLimit>,
    pub(crate) tokens: Option<WindowQuota>,
}

impl BoundSubjectLimit {
    fn is_empty(&self) -> bool {
        self.requests.as_ref().is_none_or(RequestLimit::is_empty)
            && self.tokens.as_ref().is_none_or(WindowQuota::is_empty)
    }

    pub(crate) fn admit_request(&self, rate: Option<&RateLimit>) -> Result<(), RateExceeded> {
        match &self.requests {
            Some(requests) => requests.try_acquire(rate),
            None => rate.map_or(Ok(()), RateLimit::try_acquire),
        }
    }
}

/// Token admission follows logical request admission. Only the token budgets are
/// reserved atomically; a rejection never refunds already admitted request counts.
pub(crate) fn reserve_attempt(
    model: Option<&crate::quota::BoundQuota>,
    subject: Option<&BoundSubjectLimit>,
) -> Result<
    (
        Option<crate::quota::AttemptQuota>,
        Option<crate::quota::AttemptQuota>,
    ),
    nyro_limit::window::WindowQuotaError,
> {
    use crate::quota::AttemptQuota;
    if let Some(subject) = subject
        && let Some(tokens) = &subject.tokens
    {
        let amount = subject
            .policy
            .reserve_tokens
            .expect("validated token reservation");
        if let Some(model) = model {
            let (window, cumulative) =
                tokens.reserve_with_quota(amount, &model.quota, model.policy.reserve_tokens)?;
            Ok((
                Some(AttemptQuota::cumulative(cumulative)),
                Some(AttemptQuota::window(window)),
            ))
        } else {
            Ok((None, Some(AttemptQuota::window(tokens.reserve(amount)?))))
        }
    } else {
        Ok((
            model.map(crate::quota::BoundQuota::reserve).transpose()?,
            None,
        ))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn policy() -> SubjectLimitConfig {
        SubjectLimitConfig {
            rpm: Some(1),
            rpd: None,
            tpm: None,
            tpd: None,
            reserve_tokens: None,
        }
    }

    #[test]
    fn consumed_retired_binding_survives_and_changed_candidates_cannot_reset_it() {
        let registry = SubjectLimitRegistry::default();
        let original = registry.bind("alice", &policy()).unwrap();
        original.admit_request(None).unwrap();
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
                        rpd: None,
                        tpm: None,
                        tpd: None,
                        reserve_tokens: None,
                    }
                )
                .is_err()
        );
        assert!(
            registry
                .bind("alice", &policy())
                .unwrap()
                .admit_request(None)
                .is_err()
        );
        registry
            .bind("bob", &policy())
            .unwrap()
            .admit_request(None)
            .unwrap();
    }

    #[test]
    fn live_unused_binding_pins_policy_but_failed_candidate_without_admissions_does_not() {
        let registry = SubjectLimitRegistry::default();
        let original = registry.bind("alice", &policy()).unwrap();
        let changed = SubjectLimitConfig {
            rpm: Some(2),
            rpd: None,
            tpm: None,
            tpd: None,
            reserve_tokens: None,
        };
        assert!(registry.bind("alice", &changed).is_err());
        drop(original);
        let replacement = registry.bind("alice", &changed).unwrap();
        replacement.admit_request(None).unwrap();
        replacement.admit_request(None).unwrap();
        assert!(replacement.admit_request(None).is_err());
    }

    #[test]
    fn expired_retired_binding_is_reclaimed_on_next_bind() {
        let registry = SubjectLimitRegistry::default();
        // Seed real expired state without waiting a minute or adding a production clock API.
        let bound = Arc::new(BoundSubjectLimit {
            policy: policy(),
            requests: Some(RequestLimit::new([(1, std::time::Duration::from_nanos(1))]).unwrap()),
            tokens: None,
        });
        bound.admit_request(None).unwrap();
        registry
            .entries
            .lock()
            .unwrap()
            .insert("alice".into(), bound);
        let changed = SubjectLimitConfig {
            rpm: Some(2),
            rpd: None,
            tpm: None,
            tpd: None,
            reserve_tokens: None,
        };
        let replacement = registry.bind("alice", &changed).unwrap();
        replacement.admit_request(None).unwrap();
        replacement.admit_request(None).unwrap();
        assert!(replacement.admit_request(None).is_err());
    }
    #[test]
    fn pending_receipt_without_runtime_owner_retains_policy_and_failed_candidates_do_not_reset_it()
    {
        let registry = SubjectLimitRegistry::default();
        let policy: SubjectLimitConfig =
            serde_json::from_str(r#"{"tpm":10,"tpd":100,"reserve_tokens":5}"#).unwrap();
        let bound = registry.bind("alice", &policy).unwrap();
        let receipt = bound.tokens.as_ref().unwrap().reserve(5).unwrap();
        drop(bound);
        for change in [
            r#"{"tpm":20,"tpd":100,"reserve_tokens":5}"#,
            r#"{"tpm":10,"tpd":200,"reserve_tokens":5}"#,
            r#"{"tpm":10,"tpd":100,"reserve_tokens":6}"#,
        ] {
            assert!(
                registry
                    .bind("alice", &serde_json::from_str(change).unwrap())
                    .is_err()
            );
        }
        assert_eq!(registry.token_snapshots("alice").unwrap()[0].reserved, 5);
        receipt.release();
        let mut changed = policy;
        changed.tpm = Some(20);
        registry.bind("alice", &changed).unwrap();
    }

    #[test]
    fn expired_token_history_allows_rebinding_but_live_history_rejects_changes() {
        let registry = SubjectLimitRegistry::default();
        let policy: SubjectLimitConfig =
            serde_json::from_str(r#"{"tpm":10,"reserve_tokens":5}"#).unwrap();
        let bound = registry.bind("alice", &policy).unwrap();
        bound.tokens.as_ref().unwrap().reserve(5).unwrap().settle(5);
        drop(bound);
        let mut changed = policy.clone();
        changed.tpm = Some(20);
        assert!(registry.bind("alice", &changed).is_err());
        let expired = WindowQuota::new([(10, std::time::Duration::from_nanos(1))]).unwrap();
        expired.reserve(5).unwrap().settle(5);
        registry.entries.lock().unwrap().insert(
            "expired".into(),
            Arc::new(BoundSubjectLimit {
                policy,
                requests: None,
                tokens: Some(expired),
            }),
        );
        registry.bind("expired", &changed).unwrap();
    }
}
