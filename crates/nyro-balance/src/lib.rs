//! Workload-neutral selection algorithms. Callers own eligibility and dispatch.
use rand::{
    Rng,
    distributions::{Distribution, WeightedIndex},
};
use std::collections::HashMap;

#[derive(Clone, Copy, Debug, Default, Eq, PartialEq, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Strategy {
    #[default]
    WeightedRoundrobin,
    WeightedRandom,
    LeastRecent,
    LatencyAware,
}

#[derive(Default)]
pub struct Balancer {
    roundrobin: RoundRobin,
}

impl Balancer {
    /// Prune only when authoritative configuration changes, never for temporary eligibility.
    pub fn retain_targets(&mut self, targets: &[&str]) {
        self.roundrobin
            .current
            .retain(|id, _| targets.contains(&id.as_str()));
    }
    /// History is supplied by the caller, which owns dispatch and response timing.
    pub fn choose(
        &mut self,
        strategy: Strategy,
        targets: &[(&str, u32)],
        histories: &[History],
        rng: &mut impl Rng,
    ) -> Option<usize> {
        if strategy == Strategy::WeightedRoundrobin {
            return self.roundrobin.choose(targets);
        }
        if strategy == Strategy::WeightedRandom {
            return choose_weighted(targets.iter().map(|(_, weight)| *weight), rng);
        }
        if targets.len() != histories.len() {
            return None;
        }
        select(
            strategy,
            targets.iter().map(|(_, weight)| *weight).collect(),
            histories,
            rng,
        )
    }
}

/// Smooth weighted round robin. Keys must identify unique eligible targets.
#[derive(Default)]
pub struct RoundRobin {
    current: HashMap<String, (u32, i128)>,
}
impl RoundRobin {
    pub fn choose(&mut self, targets: &[(&str, u32)]) -> Option<usize> {
        let total: i128 = targets.iter().map(|(_, weight)| i128::from(*weight)).sum();
        let mut chosen = None;
        let mut best = i128::MIN;
        for (index, (id, weight)) in targets.iter().enumerate() {
            if *weight == 0 {
                continue;
            }
            let entry = self.current.entry((*id).into()).or_insert((*weight, 0));
            if entry.0 != *weight {
                *entry = (*weight, 0);
            }
            entry.1 += i128::from(*weight);
            if entry.1 > best {
                best = entry.1;
                chosen = Some(index);
            }
        }
        if let Some(index) = chosen {
            self.current.get_mut(targets[index].0).unwrap().1 -= total;
        }
        chosen
    }
}

pub fn choose_weighted(
    weights: impl IntoIterator<Item = u32>,
    rng: &mut impl Rng,
) -> Option<usize> {
    WeightedIndex::new(weights.into_iter().map(u64::from))
        .ok()
        .map(|distribution| distribution.sample(rng))
}
#[derive(Default, Clone, Copy)]
pub struct History {
    pub last_started: Option<u64>,
    pub latency: Option<f64>,
    pub pending: usize,
}
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
        Strategy::WeightedRandom | Strategy::WeightedRoundrobin => {}
        Strategy::LeastRecent => {
            let oldest = enabled
                .iter()
                .map(|i| histories[*i].last_started)
                .min()
                .unwrap();
            preferred.retain(|i| histories[*i].last_started == oldest);
        }
        Strategy::LatencyAware => {
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
    choose_weighted(filtered, rng)
}
#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn weighted_random_does_not_require_request_history() {
        let mut balance = Balancer::default();
        assert_eq!(
            balance.choose(
                Strategy::WeightedRandom,
                &[("zero", 0), ("active", 1)],
                &[],
                &mut rand::thread_rng()
            ),
            Some(1)
        );
    }
    #[test]
    fn alternating_eligible_sets_preserve_each_targets_turn() {
        let mut balance = RoundRobin::default();
        assert_eq!(balance.choose(&[("a", 1), ("b", 1)]), Some(0));
        assert_eq!(balance.choose(&[("c", 1), ("d", 1)]), Some(0));
        assert_eq!(balance.choose(&[("a", 1), ("b", 1)]), Some(1));
        assert_eq!(balance.choose(&[("c", 1), ("d", 1)]), Some(1));
    }
    #[test]
    fn smooth_weighted_roundrobin_is_deterministic_and_excludes_zero() {
        let mut balance = RoundRobin::default();
        let targets = [("a", 3), ("b", 1), ("disabled", 0)];
        let chosen: Vec<_> = (0..8).map(|_| balance.choose(&targets).unwrap()).collect();
        assert_eq!(chosen, vec![0, 0, 1, 0, 0, 0, 1, 0]);
        assert_eq!(balance.choose(&[]), None);
        assert_eq!(balance.choose(&[("disabled", 0)]), None);
    }
    #[test]
    fn target_identity_survives_order_changes_and_weights_do_not_overflow() {
        let mut balance = RoundRobin::default();
        assert_eq!(balance.choose(&[("a", u32::MAX), ("b", u32::MAX)]), Some(0));
        assert_eq!(balance.choose(&[("b", u32::MAX), ("a", u32::MAX)]), Some(0));
        for _ in 0..20 {
            assert_eq!(balance.choose(&[("b", 0), ("a", 1)]), Some(1));
        }
    }
}
