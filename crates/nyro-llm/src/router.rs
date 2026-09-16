//! Model-scoped selection history; request preparation owns eligibility.
use rand::{
    Rng,
    distributions::{Distribution, WeightedIndex},
};

fn choose_with(weights: impl IntoIterator<Item = u32>, rng: &mut impl Rng) -> Option<usize> {
    WeightedIndex::new(weights.into_iter().map(u64::from))
        .ok()
        .map(|distribution| distribution.sample(rng))
}

use crate::{
    binding::BackendKey,
    config::{Model, Provider, Strategy},
};
use std::{
    collections::{BTreeMap, HashMap},
    sync::{Arc, Mutex, MutexGuard, Weak},
    time::Duration,
};

/// Explicitly shared across generations; retired models and bindings are weakly held.
#[derive(Default)]
pub struct RoutingRegistry {
    models: Mutex<HashMap<String, Weak<ModelState>>>,
}

#[derive(Default)]
struct ModelState {
    // Serializes choice through admission/dispatch recording, never across network I/O.
    sequence: Mutex<u64>,
    entries: Mutex<HashMap<BackendKey, Weak<Mutex<History>>>>,
}

#[derive(Default, Clone, Copy)]
struct History {
    last_started: Option<u64>,
    latency: Option<f64>,
    pending: usize,
}

pub(crate) struct BoundRouting {
    model: Arc<ModelState>,
    backends: BTreeMap<String, Arc<Mutex<History>>>,
}

impl RoutingRegistry {
    pub(crate) fn bind(
        &self,
        id: &str,
        model: &Model,
        providers: &BTreeMap<String, Provider>,
    ) -> BoundRouting {
        let mut models = self.models.lock().unwrap();
        models.retain(|_, state| state.strong_count() > 0);
        let state = models.get(id).and_then(Weak::upgrade).unwrap_or_else(|| {
            let state = Arc::new(ModelState::default());
            models.insert(id.into(), Arc::downgrade(&state));
            state
        });
        drop(models);
        let mut entries = state.entries.lock().unwrap();
        entries.retain(|_, history| history.strong_count() > 0);
        let backends = model
            .backends
            .iter()
            .map(|backend| {
                let key = BackendKey::new(id, backend, &providers[&backend.provider]);
                let history = entries
                    .get(&key)
                    .and_then(Weak::upgrade)
                    .unwrap_or_else(|| {
                        let history = Arc::new(Mutex::new(History::default()));
                        entries.insert(key, Arc::downgrade(&history));
                        history
                    });
                (backend.id.clone(), history)
            })
            .collect();
        drop(entries);
        BoundRouting {
            model: state,
            backends,
        }
    }
}

impl BoundRouting {
    pub(crate) fn selection(&self) -> MutexGuard<'_, u64> {
        self.model.sequence.lock().unwrap()
    }

    // Caller holds selection() until started(), so concurrent cold requests see pending claims.
    pub(crate) fn choose(&self, strategy: Strategy, candidates: &[(&str, u32)]) -> Option<usize> {
        if strategy == Strategy::Weighted {
            return choose_with(
                candidates.iter().map(|(_, weight)| *weight),
                &mut rand::thread_rng(),
            );
        }
        let histories: Vec<_> = candidates
            .iter()
            .map(|(id, _)| *self.backends[*id].lock().unwrap())
            .collect();
        select(
            strategy,
            candidates.iter().map(|(_, weight)| *weight).collect(),
            &histories,
            &mut rand::thread_rng(),
        )
    }

    pub(crate) fn started(&self, id: &str, sequence: &mut u64) -> Attempt {
        let history = self.backends[id].clone();
        let mut current = history.lock().unwrap();
        *sequence = sequence.saturating_add(1);
        current.last_started = Some(*sequence);
        current.pending += 1;
        drop(current);
        Attempt {
            history,
            _model: self.model.clone(),
        }
    }
}

// Only enabled, compatible, healthy candidates at the lowest priority reach here.
fn select(
    strategy: Strategy,
    weights: Vec<u32>,
    histories: &[History],
    rng: &mut impl Rng,
) -> Option<usize> {
    let enabled: Vec<_> = weights
        .iter()
        .enumerate()
        .filter_map(|(i, w)| (*w > 0).then_some(i))
        .collect();
    if enabled.is_empty() {
        return None;
    }
    let mut preferred = enabled.clone();
    match strategy {
        Strategy::Weighted => {}
        Strategy::LeastRecent => {
            let oldest = enabled
                .iter()
                .map(|i| histories[*i].last_started)
                .min()
                .unwrap();
            preferred.retain(|i| histories[*i].last_started == oldest);
        }
        Strategy::Latency => {
            let unknown: Vec<_> = enabled
                .iter()
                .copied()
                .filter(|i| histories[*i].last_started.is_none())
                .collect();
            let known: Vec<_> = enabled
                .iter()
                .copied()
                .filter(|i| histories[*i].latency.is_some())
                .collect();
            if !unknown.is_empty() {
                // Give each never-attempted backend one initial sample.
                preferred = unknown;
            } else if known.is_empty() {
                let pending = enabled.iter().map(|i| histories[*i].pending).min().unwrap();
                preferred.retain(|i| histories[*i].pending == pending);
                let oldest = preferred
                    .iter()
                    .map(|i| histories[*i].last_started)
                    .min()
                    .unwrap();
                preferred.retain(|i| histories[*i].last_started == oldest);
            } else if rng.gen_ratio(1, 20) {
                // Explore the stalest idle/known backend (5%), including prior attempts without 2xx.
                preferred.retain(|i| histories[*i].pending == 0 || histories[*i].latency.is_some());
                let oldest = preferred
                    .iter()
                    .map(|i| histories[*i].last_started)
                    .min()
                    .unwrap();
                preferred.retain(|i| histories[*i].last_started == oldest);
            } else {
                let fastest = known
                    .iter()
                    .map(|i| histories[*i].latency.unwrap())
                    .reduce(f64::min)
                    .unwrap();
                preferred = known
                    .into_iter()
                    .filter(|i| histories[*i].latency == Some(fastest))
                    .collect();
            }
        }
    }
    let mut filtered = vec![0; weights.len()];
    for i in preferred {
        filtered[i] = weights[i];
    }
    choose_with(filtered, rng)
}

pub(crate) struct Attempt {
    history: Arc<Mutex<History>>,
    // Keep the model's selection sequence alive even if a host drops its generation.
    _model: Arc<ModelState>,
}

impl Attempt {
    /// A 2xx response-header sample only; body validation and health are independent.
    pub(crate) fn headers(self, elapsed: Duration) {
        let mut history = self.history.lock().unwrap();
        let sample = elapsed.as_secs_f64();
        history.latency = Some(
            history
                .latency
                .map_or(sample, |old| old * 0.8 + sample * 0.2),
        );
    }
}

impl Drop for Attempt {
    fn drop(&mut self) {
        self.history.lock().unwrap().pending -= 1;
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use rand::{SeedableRng, rngs::StdRng};

    #[test]
    fn excludes_disabled_backends_and_handles_empty_candidates() {
        let mut rng = StdRng::seed_from_u64(42);
        assert_eq!(choose_with([], &mut rng), None);
        assert_eq!(choose_with([0, 0], &mut rng), None);
        for _ in 0..100 {
            assert_eq!(choose_with([0, 100, 0], &mut rng), Some(1));
        }
    }

    #[test]
    fn weights_change_selection_probability_without_u32_sum_overflow() {
        let mut rng = StdRng::seed_from_u64(42);
        let mut selected = [0; 2];
        for _ in 0..10_000 {
            selected[choose_with([1, 9], &mut rng).unwrap()] += 1;
        }
        assert!((8500..9500).contains(&selected[1]), "{selected:?}");
        let mut seen = [false; 2];
        for _ in 0..100 {
            seen[choose_with([u32::MAX, u32::MAX], &mut rng).unwrap()] = true;
        }
        assert_eq!(seen, [true, true]);
    }

    fn configuration() -> crate::config::Config {
        serde_json::from_value(serde_json::json!({
            "providers":{"p":{"kind":"openai","base_url":"http://localhost/v1","api_key":"secret"}},
            "models":{"public":{"workloads":["chat"],"backends":[
                {"id":"a","provider":"p","upstream_model":"private"},
                {"id":"b","provider":"p","upstream_model":"private"}
            ]}}
        }))
        .unwrap()
    }

    #[test]
    fn latency_explores_stale_and_failed_targets_without_preferring_pending_unknowns() {
        let mut rng = StdRng::seed_from_u64(42);
        let histories = [
            History {
                last_started: Some(1),
                latency: Some(1.0),
                pending: 0,
            },
            History {
                last_started: Some(2),
                latency: Some(0.01),
                pending: 0,
            },
            History {
                last_started: None,
                latency: None,
                pending: 0,
            },
        ];
        // Zero-weight unknown must never bypass eligibility.
        let mut counts = [0; 3];
        for _ in 0..10_000 {
            counts[select(
                Strategy::Latency,
                vec![u32::MAX, 1, 0],
                &histories,
                &mut rng,
            )
            .unwrap()] += 1;
        }
        assert!((400..600).contains(&counts[0]), "{counts:?}");
        assert_eq!(counts[2], 0);
        let mut failed = histories;
        failed[0].latency = None;
        let mut counts = [0; 3];
        for _ in 0..10_000 {
            counts[select(Strategy::Latency, vec![u32::MAX, 1, 0], &failed, &mut rng).unwrap()] +=
                1;
        }
        assert!(
            (400..600).contains(&counts[0]),
            "failed target must get exploration only: {counts:?}"
        );
        failed[0].pending = 1;
        for _ in 0..100 {
            assert_eq!(
                select(Strategy::Latency, vec![u32::MAX, 1, 0], &failed, &mut rng),
                Some(1)
            );
        }
    }

    #[test]
    fn ties_use_weights_and_recent_selection_ignores_disabled_history() {
        let histories = [History::default(); 3];
        for strategy in [Strategy::LeastRecent, Strategy::Latency] {
            let mut rng = StdRng::seed_from_u64(42);
            let mut counts = [0; 3];
            for _ in 0..10_000 {
                counts[select(strategy, vec![1, 9, 0], &histories, &mut rng).unwrap()] += 1;
            }
            assert!((8500..9500).contains(&counts[1]), "{counts:?}");
            assert_eq!(counts[2], 0);
            assert_eq!(select(strategy, vec![0, 0, 0], &histories, &mut rng), None);
        }
    }

    #[test]
    fn dispatch_claims_rotate_pending_cold_requests_and_drop_releases_claims() {
        let config = configuration();
        let registry = RoutingRegistry::default();
        let bound = registry.bind("public", &config.models["public"], &config.providers);
        let mut guard = bound.selection();
        let candidates = [("a", 100), ("b", 100)];
        let first = bound.started("a", &mut guard);
        assert_eq!(bound.choose(Strategy::LeastRecent, &candidates), Some(1));
        assert_eq!(bound.choose(Strategy::Latency, &candidates), Some(1));
        let second = bound.started("b", &mut guard);
        let third = bound.started("a", &mut guard);
        assert_eq!(bound.choose(Strategy::Latency, &candidates), Some(1));
        drop(second);
        assert_eq!(bound.choose(Strategy::Latency, &candidates), Some(1));
        drop((first, third));
        assert_eq!(bound.backends["a"].lock().unwrap().pending, 0);
        assert_eq!(bound.backends["b"].lock().unwrap().pending, 0);
    }

    #[test]
    fn header_samples_use_ema_and_cancellation_does_not_invent_latency() {
        let config = configuration();
        let registry = RoutingRegistry::default();
        let bound = registry.bind("public", &config.models["public"], &config.providers);
        let mut guard = bound.selection();
        drop(bound.started("a", &mut guard));
        assert!(bound.backends["a"].lock().unwrap().latency.is_none());
        bound
            .started("a", &mut guard)
            .headers(Duration::from_millis(100));
        bound
            .started("a", &mut guard)
            .headers(Duration::from_millis(200));
        let history = *bound.backends["a"].lock().unwrap();
        assert!((history.latency.unwrap() - 0.12).abs() < 1e-12);
        assert_eq!(history.pending, 0);
    }

    #[test]
    fn reload_retains_history_but_changed_bindings_and_models_are_isolated() {
        let config = configuration();
        let registry = RoutingRegistry::default();
        let first = registry.bind("public", &config.models["public"], &config.providers);
        first
            .started("a", &mut first.selection())
            .headers(Duration::from_millis(50));
        let mut equivalent = config.clone();
        let model = equivalent.models.get_mut("public").unwrap();
        model.strategy = Strategy::Latency;
        model.backends.reverse();
        model.backends[0].weight = 1;
        model.backends[0].priority = 5;
        model.health = Some(Default::default());
        equivalent
            .providers
            .get_mut("p")
            .unwrap()
            .base_url
            .push('/');
        equivalent.providers.get_mut("p").unwrap().api =
            Some(crate::config::OpenAiApi::ChatCompletions);
        let reused = registry.bind(
            "public",
            &equivalent.models["public"],
            &equivalent.providers,
        );
        assert!(Arc::ptr_eq(&first.model, &reused.model));
        assert!(Arc::ptr_eq(&first.backends["a"], &reused.backends["a"]));
        assert_eq!(
            reused.choose(Strategy::LeastRecent, &[("a", 100), ("b", 100)]),
            Some(1)
        );
        for change in 0..10 {
            let mut config = config.clone();
            let model = config.models.get_mut("public").unwrap();
            let provider = config.providers.get_mut("p").unwrap();
            let mut public = "public";
            match change {
                0 => public = "other",
                1 => model.backends[0].id = "new".into(),
                2 => model.backends[0].upstream_model = "other".into(),
                3 => provider.base_url = "http://other/v1".into(),
                4 => provider.api_key = Some("rotated".into()),
                5 => provider.native_chat = true,
                6 => provider.api = Some(crate::config::OpenAiApi::Responses),
                7 => provider.kind = crate::config::ProviderKind::Anthropic,
                8 => provider.transport.proxy_url = Some("http://proxy.test:8080".into()),
                9 => provider.transport.http1_only = true,
                _ => unreachable!(),
            }
            let bound = registry.bind(public, model, &config.providers);
            assert!(
                bound.backends[&model.backends[0].id]
                    .lock()
                    .unwrap()
                    .last_started
                    .is_none(),
                "change {change}"
            );
        }
        // Same model continues to retain b even if a is removed; a's history retires independently.
        let weak = Arc::downgrade(&first.backends["a"]);
        let mut reduced = config.models["public"].clone();
        reduced.backends.remove(0);
        let reduced = registry.bind("public", &reduced, &config.providers);
        drop((first, reused));
        assert!(weak.upgrade().is_none());
        let fresh = registry.bind("public", &config.models["public"], &config.providers);
        assert!(fresh.backends["a"].lock().unwrap().last_started.is_none());
        assert!(Arc::ptr_eq(&reduced.backends["b"], &fresh.backends["b"]));
        let receipt = fresh.started("a", &mut fresh.selection());
        let model_weak = Arc::downgrade(&fresh.model);
        drop((reduced, fresh));
        assert!(model_weak.upgrade().is_some());
        drop(receipt);
        assert!(model_weak.upgrade().is_none());
    }
}
