//! Target selection strategies for the proxy routing layer.
//!
//! # Architecture
//!
//! Each strategy implements the [`RoutingStrategy`] trait and returns an
//! ordered `Vec<SelectedTarget>` — the dispatcher tries them in order and
//! stops on the first successful upstream response.
//!
//! | Strategy   | Description                                             |
//! |------------|---------------------------------------------------------|
//! | `weighted` | Weighted reservoir sampling (default)                   |
//! | `priority` | Priority groups; random within a group                  |
//! | `cooldown` | Deprioritises recently-used targets (round-robin style) |
//! | `latency`  | Ascending EMA response-latency order                    |
//!
//! # Usage
//!
//! ```rust,ignore
//! // Dispatcher
//! let ordered = TargetSelector::select_ordered(&route.balance, &targets);
//! // After a successful call (for stateful strategies):
//! TargetSelector::record_selected(&route.balance, &target_key);
//! TargetSelector::record_latency(&route.balance, &target_key, elapsed_ms);
//! ```

use std::collections::{BTreeMap, HashMap};
use std::str::FromStr;
use std::sync::{OnceLock, RwLock};
use std::time::{Duration, Instant};

use rand::Rng;

use crate::db::models::{ModelBackend, ModelBalance};

// ── SelectedTarget ────────────────────────────────────────────────────────────

#[derive(Debug, Clone)]
pub struct SelectedTarget {
    pub provider_id: String,
    pub model: String,
}

// ── RoutingStrategy trait ─────────────────────────────────────────────────────

/// Produces an ordered list of targets to try, from most to least preferred.
pub trait RoutingStrategy: Send + Sync {
    fn select_ordered(&self, targets: &[ModelBackend]) -> Vec<SelectedTarget>;
}

// ── Weighted ──────────────────────────────────────────────────────────────────

pub struct WeightedStrategy;

impl RoutingStrategy for WeightedStrategy {
    fn select_ordered(&self, targets: &[ModelBackend]) -> Vec<SelectedTarget> {
        let refs: Vec<&ModelBackend> = targets.iter().filter(|t| t.weight > 0).collect();
        weighted_shuffle(&refs)
            .into_iter()
            .map(to_selected)
            .collect()
    }
}

// ── Priority ──────────────────────────────────────────────────────────────────

pub struct PriorityStrategy;

impl RoutingStrategy for PriorityStrategy {
    fn select_ordered(&self, targets: &[ModelBackend]) -> Vec<SelectedTarget> {
        let mut groups: BTreeMap<i32, Vec<&ModelBackend>> = BTreeMap::new();
        for t in targets {
            groups.entry(t.priority).or_default().push(t);
        }
        groups
            .into_values()
            .flat_map(|group| group.into_iter().map(to_selected))
            .collect()
    }
}

// ── Cooldown ──────────────────────────────────────────────────────────────────

/// Cooldown window: a target is fully "cooled" after this duration.
const COOLDOWN: Duration = Duration::from_secs(60);

/// Process-wide cooldown state. Tracks when each target was last selected.
pub struct CooldownStrategy {
    last_selected: RwLock<HashMap<String, Instant>>,
}

impl CooldownStrategy {
    pub fn global() -> &'static Self {
        static INSTANCE: OnceLock<CooldownStrategy> = OnceLock::new();
        INSTANCE.get_or_init(|| CooldownStrategy {
            last_selected: RwLock::new(HashMap::new()),
        })
    }

    /// Mark `target_key` as just selected.
    pub fn record_selected(&self, target_key: &str) {
        if let Ok(mut map) = self.last_selected.write() {
            map.insert(target_key.to_string(), Instant::now());
        }
    }
}

impl RoutingStrategy for CooldownStrategy {
    fn select_ordered(&self, targets: &[ModelBackend]) -> Vec<SelectedTarget> {
        let map = self.last_selected.read().unwrap_or_else(|p| p.into_inner());
        let mut scored: Vec<(&ModelBackend, Duration)> = targets
            .iter()
            .map(|t| {
                let key = target_key(t);
                // How long since this target was last selected (capped at COOLDOWN).
                // Targets never selected are treated as fully cooled.
                let cooled_for = map
                    .get(&key)
                    .map(|inst| inst.elapsed().min(COOLDOWN))
                    .unwrap_or(COOLDOWN);
                (t, cooled_for)
            })
            .collect();
        // Coolest (longest unused) first.
        scored.sort_by(|a, b| b.1.cmp(&a.1));
        scored.into_iter().map(|(t, _)| to_selected(t)).collect()
    }
}

// ── Latency ───────────────────────────────────────────────────────────────────

/// EMA smoothing factor: 20% weight to new observations.
const LATENCY_ALPHA: f64 = 0.2;

/// Process-wide latency state. Tracks EMA response latency per target.
pub struct LatencyStrategy {
    ema: RwLock<HashMap<String, f64>>,
}

impl LatencyStrategy {
    pub fn global() -> &'static Self {
        static INSTANCE: OnceLock<LatencyStrategy> = OnceLock::new();
        INSTANCE.get_or_init(|| LatencyStrategy {
            ema: RwLock::new(HashMap::new()),
        })
    }

    /// Record a new latency observation for `target_key`.
    pub fn record_latency(&self, target_key: &str, latency_ms: f64) {
        if let Ok(mut map) = self.ema.write() {
            let entry = map.entry(target_key.to_string()).or_insert(latency_ms);
            *entry = LATENCY_ALPHA * latency_ms + (1.0 - LATENCY_ALPHA) * (*entry);
        }
    }
}

impl RoutingStrategy for LatencyStrategy {
    fn select_ordered(&self, targets: &[ModelBackend]) -> Vec<SelectedTarget> {
        let map = self.ema.read().unwrap_or_else(|p| p.into_inner());
        let mut scored: Vec<(&ModelBackend, f64)> = targets
            .iter()
            .map(|t| {
                let key = target_key(t);
                // No observation yet → 0 ms (unobserved targets go first).
                let ema = map.get(&key).copied().unwrap_or(0.0);
                (t, ema)
            })
            .collect();
        // Fastest first.
        scored.sort_by(|a, b| a.1.partial_cmp(&b.1).unwrap_or(std::cmp::Ordering::Equal));
        scored.into_iter().map(|(t, _)| to_selected(t)).collect()
    }
}

// ── TargetSelector (public entry point) ───────────────────────────────────────

pub struct TargetSelector;

impl TargetSelector {
    /// Return targets ordered by the named balance. Unrecognised balance
    /// strings fall back to `weighted`.
    pub fn select_ordered(balance: &str, targets: &[ModelBackend]) -> Vec<SelectedTarget> {
        match ModelBalance::from_str(balance).unwrap_or_default() {
            ModelBalance::Weighted => WeightedStrategy.select_ordered(targets),
            ModelBalance::Priority => PriorityStrategy.select_ordered(targets),
            ModelBalance::Cooldown => CooldownStrategy::global().select_ordered(targets),
            ModelBalance::Latency => LatencyStrategy::global().select_ordered(targets),
        }
    }

    /// Record that `target_key` was successfully selected.
    /// Only meaningful for the `cooldown` balance; a no-op for others.
    pub fn record_selected(balance: &str, target_key: &str) {
        if ModelBalance::from_str(balance).unwrap_or_default() == ModelBalance::Cooldown {
            CooldownStrategy::global().record_selected(target_key);
        }
    }

    /// Record observed response latency for `target_key`.
    /// Only meaningful for the `latency` balance; a no-op for others.
    pub fn record_latency(balance: &str, target_key: &str, latency_ms: f64) {
        if ModelBalance::from_str(balance).unwrap_or_default() == ModelBalance::Latency {
            LatencyStrategy::global().record_latency(target_key, latency_ms);
        }
    }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

#[inline]
fn target_key(t: &ModelBackend) -> String {
    format!("{}:{}", t.provider_id, t.model)
}

#[inline]
fn to_selected(t: &ModelBackend) -> SelectedTarget {
    SelectedTarget {
        provider_id: t.provider_id.clone(),
        model: t.model.clone(),
    }
}

fn weighted_shuffle<'a>(targets: &[&'a ModelBackend]) -> Vec<&'a ModelBackend> {
    if targets.is_empty() {
        return vec![];
    }
    let mut rng = rand::thread_rng();
    let mut items: Vec<(&ModelBackend, f64)> = targets
        .iter()
        .map(|t| {
            let weight = t.weight.max(1) as f64;
            let key = rng.r#gen::<f64>().powf(1.0 / weight);
            (*t, key)
        })
        .collect();
    items.sort_by(|a, b| b.1.partial_cmp(&a.1).unwrap());
    items.into_iter().map(|(t, _)| t).collect()
}
