//! Passive backend health shared explicitly across runtime generations.

use crate::config::{Backend, HealthConfig, OpenAiApi, Provider, ProviderKind};
use std::{
    collections::HashMap,
    sync::{Arc, Mutex, Weak},
    time::{Duration, Instant},
};

/// Shares health for unchanged backend bindings without retaining retired runtimes.
#[derive(Default)]
pub struct HealthRegistry {
    entries: Mutex<HashMap<HealthKey, Weak<BackendHealth>>>,
}

// Do not derive Debug: a binding contains provider credentials.
#[derive(Eq, PartialEq, Hash)]
struct HealthKey {
    model: String,
    backend: String,
    provider: String,
    kind: ProviderKind,
    api: OpenAiApi,
    native_chat: bool,
    base_url: String,
    api_key: Option<String>,
    upstream_model: String,
    policy: HealthConfig,
}

impl HealthRegistry {
    pub(crate) fn backend(
        &self,
        model: &str,
        backend: &Backend,
        provider: &Provider,
        policy: &HealthConfig,
    ) -> Arc<BackendHealth> {
        // Match Driver's effective endpoint and default API selection.
        let mut base = reqwest::Url::parse(&provider.base_url).expect("validated provider URL");
        base.set_path(&format!("{}/", base.path().trim_end_matches('/')));
        let key = HealthKey {
            model: model.into(),
            backend: backend.id.clone(),
            provider: backend.provider.clone(),
            kind: provider.kind,
            api: provider.api.unwrap_or_default(),
            native_chat: provider.native_chat,
            base_url: base.into(),
            api_key: provider.api_key.clone(),
            upstream_model: backend.upstream_model.clone(),
            policy: policy.clone(),
        };
        let mut entries = self.entries.lock().unwrap();
        entries.retain(|_, entry| entry.strong_count() > 0);
        if let Some(health) = entries.get(&key).and_then(Weak::upgrade) {
            return health;
        }
        let health = Arc::new(BackendHealth {
            policy: policy.clone(),
            state: Mutex::new(State::default()),
        });
        entries.insert(key, Arc::downgrade(&health));
        health
    }
}

pub(crate) struct BackendHealth {
    policy: HealthConfig,
    state: Mutex<State>,
}

#[derive(Default)]
struct State {
    epoch: u64,
    failures: u32,
    open_until: Option<Instant>,
    probing: bool,
}

impl BackendHealth {
    pub(crate) fn available(&self) -> bool {
        self.available_at(Instant::now())
    }

    fn available_at(&self, now: Instant) -> bool {
        let state = self.state.lock().unwrap();
        !state.probing && state.open_until.is_none_or(|until| now >= until)
    }

    pub(crate) fn try_acquire(self: &Arc<Self>) -> Option<Attempt> {
        self.try_acquire_at(Instant::now())
    }

    fn try_acquire_at(self: &Arc<Self>, now: Instant) -> Option<Attempt> {
        let mut state = self.state.lock().unwrap();
        if state.probing || state.open_until.is_some_and(|until| now < until) {
            return None;
        }
        if state.open_until.is_some() {
            state.probing = true;
            state.epoch = state.epoch.wrapping_add(1);
        }
        Some(Attempt {
            health: Arc::clone(self),
            epoch: state.epoch,
        })
    }
}

/// An observed attempt; dropping it is neutral and releases any recovery probe.
pub(crate) struct Attempt {
    health: Arc<BackendHealth>,
    epoch: u64,
}

impl Attempt {
    pub(crate) fn success(self) {
        let mut state = self.health.state.lock().unwrap();
        if state.epoch != self.epoch {
            return;
        }
        if state.probing {
            state.epoch = state.epoch.wrapping_add(1);
        }
        state.failures = 0;
        state.open_until = None;
        state.probing = false;
    }

    pub(crate) fn failure(self) {
        self.failure_at(Instant::now());
    }

    fn failure_at(self, now: Instant) {
        let mut state = self.health.state.lock().unwrap();
        if state.epoch != self.epoch {
            return;
        }
        state.failures = state.failures.saturating_add(1);
        if state.probing || state.failures >= self.health.policy.failure_threshold {
            // Config validation checks representability; checked_add also avoids a
            // panic if a platform clock approaches its limit during a long run.
            state.open_until = Some(
                now.checked_add(Duration::from_millis(self.health.policy.cooldown_ms))
                    .unwrap_or(now),
            );
            state.probing = false;
            state.epoch = state.epoch.wrapping_add(1);
        }
    }
}

impl Drop for Attempt {
    fn drop(&mut self) {
        let mut state = self.health.state.lock().unwrap();
        if state.epoch == self.epoch && state.probing {
            state.probing = false;
            state.epoch = state.epoch.wrapping_add(1);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::config::{OpenAiApi, ProviderKind};

    fn binding() -> (Backend, Provider, HealthConfig) {
        (
            Backend {
                id: "primary".into(),
                provider: "provider".into(),
                upstream_model: "upstream".into(),
                weight: 100,
                priority: 0,
            },
            Provider {
                native_chat: false,
                kind: ProviderKind::Openai,
                api: None,
                base_url: "https://example.test/v1".into(),
                api_key: Some("secret".into()),
            },
            HealthConfig {
                failure_threshold: 2,
                cooldown_ms: 20,
            },
        )
    }

    fn health() -> Arc<BackendHealth> {
        let (backend, provider, policy) = binding();
        HealthRegistry::default().backend("chat", &backend, &provider, &policy)
    }

    fn open(health: &Arc<BackendHealth>, now: Instant) {
        health.try_acquire_at(now).unwrap().failure_at(now);
        health.try_acquire_at(now).unwrap().failure_at(now);
        assert!(!health.available_at(now));
    }

    #[test]
    fn threshold_counts_consecutive_failures_and_success_resets() {
        let health = health();
        let now = Instant::now();
        health.try_acquire_at(now).unwrap().failure_at(now);
        assert!(health.available());
        health.try_acquire().unwrap().success();
        health.try_acquire_at(now).unwrap().failure_at(now);
        assert!(health.available_at(now));
        health.try_acquire_at(now).unwrap().failure_at(now);
        assert!(!health.available_at(now));
        assert!(health.try_acquire_at(now).is_none());
    }

    #[test]
    fn cooldown_allows_one_probe_and_probe_failure_restarts_cooldown() {
        let health = health();
        let now = Instant::now();
        open(&health, now);
        assert!(!health.available_at(now + Duration::from_millis(19)));
        let ready = now + Duration::from_millis(20);
        assert!(health.available_at(ready));
        let probe = health.try_acquire_at(ready).unwrap();
        assert!(!health.available_at(ready));
        assert!(health.try_acquire_at(ready).is_none());
        probe.failure_at(ready);
        assert!(
            health
                .try_acquire_at(ready + Duration::from_millis(19))
                .is_none()
        );
        health
            .try_acquire_at(ready + Duration::from_millis(20))
            .unwrap()
            .success();
        assert!(health.available_at(ready + Duration::from_millis(20)));
    }

    #[test]
    fn cancellation_is_neutral_and_releases_the_probe() {
        let health = health();
        let now = Instant::now();
        drop(health.try_acquire_at(now).unwrap());
        open(&health, now);
        let ready = now + Duration::from_millis(20);
        drop(health.try_acquire_at(ready).unwrap());
        health.try_acquire_at(ready).unwrap().success();
        health.try_acquire_at(ready).unwrap().failure_at(ready);
        assert!(health.available_at(ready));
    }

    #[test]
    fn old_completions_cannot_close_open_state_or_poison_recovery() {
        let health = health();
        let now = Instant::now();
        let old_success = health.try_acquire_at(now).unwrap();
        let old_failure = health.try_acquire_at(now).unwrap();
        let old_drop = health.try_acquire_at(now).unwrap();
        open(&health, now);
        old_success.success();
        assert!(!health.available_at(now));
        let ready = now + Duration::from_millis(20);
        let probe = health.try_acquire_at(ready).unwrap();
        drop(old_drop);
        assert!(health.try_acquire_at(ready).is_none());
        probe.success();
        old_failure.failure_at(ready);
        health.try_acquire_at(ready).unwrap().failure_at(ready);
        assert!(health.available_at(ready));
    }

    #[test]
    fn concurrent_probe_acquisition_is_exclusive() {
        let health = health();
        let now = Instant::now();
        open(&health, now);
        let ready = now + Duration::from_millis(20);
        let barrier = std::sync::Barrier::new(8);
        let admitted = std::sync::atomic::AtomicUsize::new(0);
        std::thread::scope(|scope| {
            for _ in 0..8 {
                scope.spawn(|| {
                    barrier.wait();
                    let attempt = health.try_acquire_at(ready);
                    if attempt.is_some() {
                        admitted.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
                    }
                    barrier.wait();
                    drop(attempt);
                });
            }
        });
        assert_eq!(admitted.load(std::sync::atomic::Ordering::Relaxed), 1);
        assert!(health.try_acquire_at(ready).is_some());
    }

    #[test]
    fn registry_reuses_effective_bindings_and_isolates_changed_identity_or_policy() {
        let registry = HealthRegistry::default();
        let (backend, provider, policy) = binding();
        let first = registry.backend("chat", &backend, &provider, &policy);
        let now = Instant::now();
        open(&first, now);
        let mut weighted = backend.clone();
        weighted.weight = 42;
        weighted.priority = 3;
        let mut equivalent = provider.clone();
        equivalent.api = Some(OpenAiApi::ChatCompletions);
        equivalent.base_url.push('/');
        let reused = registry.backend("chat", &weighted, &equivalent, &policy);
        assert!(Arc::ptr_eq(&first, &reused));
        assert!(!reused.available_at(now));
        for change in 0..10 {
            let mut backend = backend.clone();
            let mut provider = provider.clone();
            let mut policy = policy.clone();
            let mut model = "chat";
            match change {
                0 => model = "other",
                1 => backend.id = "other".into(),
                2 => backend.provider = "other".into(),
                3 => backend.upstream_model = "other".into(),
                4 => provider.kind = ProviderKind::Anthropic,
                5 => provider.api = Some(OpenAiApi::Responses),
                6 => provider.base_url = "https://other.test/v1".into(),
                7 => provider.api_key = Some("changed-secret".into()),
                8 => policy.failure_threshold = 1,
                9 => provider.native_chat = true,
                _ => unreachable!(),
            }
            let changed = registry.backend(model, &backend, &provider, &policy);
            assert!(!Arc::ptr_eq(&first, &changed), "change {change}");
            assert!(changed.available());
        }
        let mut changed_policy = policy.clone();
        changed_policy.cooldown_ms += 1;
        assert!(!Arc::ptr_eq(
            &first,
            &registry.backend("chat", &backend, &provider, &changed_policy)
        ));
    }

    #[test]
    fn registry_does_not_retain_retired_state_but_inflight_attempts_do() {
        let registry = HealthRegistry::default();
        let (backend, provider, policy) = binding();
        let health = registry.backend("chat", &backend, &provider, &policy);
        let weak = Arc::downgrade(&health);
        let attempt = health.try_acquire().unwrap();
        drop(health);
        assert!(weak.upgrade().is_some());
        drop(attempt);
        assert!(weak.upgrade().is_none());
        let replacement = registry.backend("other", &backend, &provider, &policy);
        assert!(replacement.available());
        assert_eq!(registry.entries.lock().unwrap().len(), 1);
    }
}
