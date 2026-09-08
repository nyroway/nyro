//! Cumulative model token accounting shared across runtime generations.

use crate::{EmbeddingUsage, Usage, codec::CodecError, config::QuotaConfig, runtime::BuildError};
use nyro_limit::quota::{Quota, QuotaError, QuotaSnapshot, Reservation};
use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
};

/// Retains consumed and pending balances even after a model is removed.
/// A live or consumed policy can only be changed by replacing the registry.
#[derive(Default)]
pub struct QuotaRegistry {
    entries: Mutex<HashMap<String, Arc<BoundQuota>>>,
}

impl QuotaRegistry {
    pub fn snapshot(&self, model: &str) -> Option<QuotaSnapshot> {
        self.entries
            .lock()
            .unwrap()
            .get(model)
            .map(|bound| bound.quota.snapshot())
    }

    pub(crate) fn bind(
        &self,
        model: &str,
        policy: &QuotaConfig,
    ) -> Result<Arc<BoundQuota>, BuildError> {
        if policy.total_tokens == 0
            || policy.reserve_tokens == 0
            || policy.reserve_tokens > policy.total_tokens
        {
            return Err(BuildError);
        }
        let mut entries = self.entries.lock().unwrap();
        entries.retain(|_, bound| {
            let balance = bound.quota.snapshot();
            Arc::strong_count(bound) > 1 || balance.used > 0 || balance.reserved > 0
        });
        if let Some(bound) = entries.get(model) {
            return if bound.policy == *policy {
                Ok(Arc::clone(bound))
            } else {
                Err(BuildError)
            };
        }
        let bound = Arc::new(BoundQuota {
            model: model.into(),
            policy: policy.clone(),
            quota: Quota::new(policy.total_tokens).map_err(|_| BuildError)?,
        });
        entries.insert(model.into(), Arc::clone(&bound));
        Ok(bound)
    }
}

pub(crate) struct BoundQuota {
    model: String,
    policy: QuotaConfig,
    quota: Quota,
}
impl BoundQuota {
    pub(crate) fn reserve(&self, backend: &str) -> Result<AttemptQuota, QuotaError> {
        Ok(AttemptQuota {
            reservation: Some(self.quota.reserve(self.policy.reserve_tokens)?),
            model: self.model.clone(),
            backend: backend.into(),
            observed: None,
        })
    }
}

pub(crate) struct AttemptQuota {
    reservation: Option<Reservation>,
    model: String,
    backend: String,
    observed: Option<u64>,
}
impl AttemptQuota {
    pub(crate) fn observe(&mut self, usage: &Usage) -> Result<(), CodecError> {
        if usage.prompt_tokens.checked_add(usage.completion_tokens) != Some(usage.total_tokens) {
            return Err(CodecError("invalid token usage total".into()));
        }
        self.observe_total(usage.total_tokens)
    }

    pub(crate) fn observe_embedding(&mut self, usage: &EmbeddingUsage) -> Result<(), CodecError> {
        if usage.prompt_tokens != usage.total_tokens {
            return Err(CodecError("invalid embedding token usage total".into()));
        }
        self.observe_total(usage.total_tokens)
    }

    fn observe_total(&mut self, total: u64) -> Result<(), CodecError> {
        if self.observed.is_some_and(|previous| total < previous) {
            return Err(CodecError("decreasing cumulative token usage".into()));
        }
        self.observed = Some(total);
        Ok(())
    }

    pub(crate) fn complete(mut self) {
        let actual = self.observed.unwrap_or_else(|| self.fallback());
        self.settle(actual, "complete");
    }

    pub(crate) fn release(mut self) {
        self.settle(0, "released");
    }

    fn fallback(&self) -> u64 {
        self.reservation
            .as_ref()
            .map_or(0, Reservation::amount)
            .max(self.observed.unwrap_or(0))
    }

    fn settle(&mut self, charged_units: u64, outcome: &'static str) {
        if let Some(reservation) = self.reservation.take() {
            reservation.settle(charged_units);
            tracing::debug!(
                model = %self.model,
                backend = %self.backend,
                charged_units,
                known_usage = self.observed.is_some(),
                outcome,
                "upstream attempt quota settled"
            );
        }
    }
}

impl Drop for AttemptQuota {
    fn drop(&mut self) {
        self.settle(self.fallback(), "fallback");
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn policy() -> QuotaConfig {
        QuotaConfig {
            total_tokens: 100,
            reserve_tokens: 20,
        }
    }

    fn usage(prompt_tokens: u64, completion_tokens: u64, total_tokens: u64) -> Usage {
        Usage {
            prompt_tokens,
            completion_tokens,
            total_tokens,
            prompt_tokens_details: None,
            completion_tokens_details: None,
        }
    }

    #[test]
    fn completed_actual_refunds_reservation_including_explicit_zero() {
        let registry = QuotaRegistry::default();
        let bound = registry.bind("chat", &policy()).unwrap();
        for (actual, expected) in [(8, 8), (0, 8), (30, 38)] {
            let mut attempt = bound.reserve("primary").unwrap();
            assert_eq!(registry.snapshot("chat").unwrap().reserved, 20);
            attempt.observe(&usage(actual, 0, actual)).unwrap();
            attempt.complete();
            assert_eq!(
                registry.snapshot("chat").unwrap(),
                QuotaSnapshot {
                    limit: 100,
                    used: expected,
                    reserved: 0
                }
            );
        }
    }

    #[test]
    fn missing_usage_and_abandoned_partial_usage_charge_conservative_fallback() {
        let registry = QuotaRegistry::default();
        let bound = registry.bind("chat", &policy()).unwrap();
        bound.reserve("primary").unwrap().complete();
        assert_eq!(registry.snapshot("chat").unwrap().used, 20);
        let mut partial = bound.reserve("primary").unwrap();
        partial.observe(&usage(3, 2, 5)).unwrap();
        drop(partial);
        assert_eq!(registry.snapshot("chat").unwrap().used, 40);
        let mut overage = bound.reserve("fallback").unwrap();
        overage.observe(&usage(10, 70, 80)).unwrap();
        drop(overage);
        assert_eq!(
            registry.snapshot("chat").unwrap(),
            QuotaSnapshot {
                limit: 100,
                used: 120,
                reserved: 0
            }
        );
        assert!(matches!(
            bound.reserve("primary"),
            Err(QuotaError::Exceeded)
        ));
    }

    #[test]
    fn known_connect_failure_releases_all_reserved_credit() {
        let registry = QuotaRegistry::default();
        let bound = registry.bind("chat", &policy()).unwrap();
        bound.reserve("primary").unwrap().release();
        assert_eq!(registry.snapshot("chat").unwrap().used, 0);
        assert_eq!(registry.snapshot("chat").unwrap().reserved, 0);
    }

    #[test]
    fn cumulative_snapshots_replace_prior_totals_and_reject_invalid_updates() {
        let registry = QuotaRegistry::default();
        let bound = registry.bind("chat", &policy()).unwrap();
        let mut attempt = bound.reserve("primary").unwrap();
        for snapshot in [usage(3, 2, 5), usage(3, 2, 5), usage(3, 7, 10)] {
            attempt.observe(&snapshot).unwrap();
        }
        for invalid in [usage(3, 6, 9), usage(10, 10, 100), usage(u64::MAX, 1, 0)] {
            assert!(attempt.observe(&invalid).is_err());
        }
        attempt.complete();
        assert_eq!(registry.snapshot("chat").unwrap().used, 10);
    }

    #[test]
    fn invalid_first_usage_does_not_turn_unknown_outcome_into_free_usage() {
        let registry = QuotaRegistry::default();
        let bound = registry.bind("chat", &policy()).unwrap();
        let mut attempt = bound.reserve("primary").unwrap();
        assert!(attempt.observe(&usage(1, 1, 0)).is_err());
        drop(attempt);
        assert_eq!(registry.snapshot("chat").unwrap().used, 20);
    }

    #[test]
    fn embeddings_validate_prompt_total_and_settle_actual() {
        let registry = QuotaRegistry::default();
        let bound = registry.bind("embedding", &policy()).unwrap();
        let mut attempt = bound.reserve("primary").unwrap();
        assert!(
            attempt
                .observe_embedding(&EmbeddingUsage {
                    prompt_tokens: 7,
                    total_tokens: 0
                })
                .is_err()
        );
        attempt
            .observe_embedding(&EmbeddingUsage {
                prompt_tokens: 7,
                total_tokens: 7,
            })
            .unwrap();
        attempt.complete();
        assert_eq!(registry.snapshot("embedding").unwrap().used, 7);
    }

    #[test]
    fn consumed_ledger_survives_removal_and_failed_candidate_drop() {
        let registry = QuotaRegistry::default();
        let original = registry.bind("chat", &policy()).unwrap();
        drop(original.reserve("primary").unwrap());
        let candidate = registry.bind("chat", &policy()).unwrap();
        assert!(Arc::ptr_eq(&original, &candidate));
        drop(candidate);
        drop(original);
        let other = registry.bind("alias", &policy()).unwrap();
        assert_eq!(registry.snapshot("chat").unwrap().used, 20);
        let readded = registry.bind("chat", &policy()).unwrap();
        drop(readded.reserve("new-backend").unwrap());
        assert_eq!(registry.snapshot("chat").unwrap().used, 40);
        assert_eq!(registry.snapshot("alias").unwrap().used, 0);
        drop(other);
    }

    #[test]
    fn pending_attempt_retains_ledger_without_runtime_owner() {
        let registry = QuotaRegistry::default();
        let original = registry.bind("chat", &policy()).unwrap();
        let attempt = original.reserve("primary").unwrap();
        drop(original);
        let changed = QuotaConfig {
            total_tokens: 200,
            ..policy()
        };
        assert!(registry.bind("chat", &changed).is_err());
        assert_eq!(registry.snapshot("chat").unwrap().reserved, 20);
        drop(attempt);
        assert_eq!(registry.snapshot("chat").unwrap().used, 20);
        assert!(registry.bind("chat", &changed).is_err());
    }

    #[test]
    fn policy_changes_reject_live_or_consumed_bindings_without_resetting_them() {
        let registry = QuotaRegistry::default();
        let live = registry.bind("chat", &policy()).unwrap();
        for changed in [
            QuotaConfig {
                total_tokens: 200,
                ..policy()
            },
            QuotaConfig {
                reserve_tokens: 30,
                ..policy()
            },
        ] {
            assert!(registry.bind("chat", &changed).is_err());
        }
        drop(live.reserve("primary").unwrap());
        drop(live);
        assert!(
            registry
                .bind(
                    "chat",
                    &QuotaConfig {
                        total_tokens: 200,
                        ..policy()
                    }
                )
                .is_err()
        );
        assert_eq!(registry.snapshot("chat").unwrap().used, 20);
    }

    #[test]
    fn unused_failed_candidates_are_pruned_and_do_not_pin_policy() {
        let registry = QuotaRegistry::default();
        let candidate = registry.bind("unused", &policy()).unwrap();
        drop(candidate);
        let changed = QuotaConfig {
            total_tokens: 200,
            ..policy()
        };
        let live = registry.bind("chat", &changed).unwrap();
        assert!(registry.snapshot("unused").is_none());
        let replacement = registry.bind("unused", &changed).unwrap();
        assert_eq!(registry.snapshot("unused").unwrap().limit, 200);
        drop(replacement);
        drop(live);
    }

    #[test]
    fn invalid_policies_do_not_create_ledgers() {
        let registry = QuotaRegistry::default();
        for invalid in [
            QuotaConfig {
                total_tokens: 0,
                ..policy()
            },
            QuotaConfig {
                reserve_tokens: 0,
                ..policy()
            },
            QuotaConfig {
                reserve_tokens: 101,
                ..policy()
            },
        ] {
            assert!(registry.bind("chat", &invalid).is_err());
            assert!(registry.snapshot("chat").is_none());
        }
    }
}
