//! Shared rolling request/token histories. Only observed usage is charged.
use std::{
    collections::{HashMap, VecDeque},
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

#[derive(Clone, Copy, Debug)]
pub struct Rule {
    pub limit: u64,
    pub window: Duration,
}
#[derive(Clone, Copy, Debug, Eq, PartialEq, thiserror::Error)]
#[error("invalid rolling limit")]
pub struct Invalid;
#[derive(Clone, Copy, Debug, Eq, PartialEq, thiserror::Error)]
#[error("rolling limit exceeded")]
pub struct Exceeded {
    pub retry_after: Duration,
}
#[derive(Clone, Debug, Hash, Eq, PartialEq)]
struct Key {
    scope: String,
    token: bool,
    window: Duration,
}
#[derive(Default)]
struct History {
    events: VecDeque<(Instant, u64)>,
    used: u128,
}
impl History {
    fn prune(&mut self, now: Instant, window: Duration) {
        while self
            .events
            .front()
            .is_some_and(|(at, _)| now.saturating_duration_since(*at) >= window)
        {
            self.used -= u128::from(self.events.pop_front().unwrap().1);
        }
    }
    fn add(&mut self, now: Instant, amount: u64) {
        if amount > 0 {
            self.used += u128::from(amount);
            self.events.push_back((now, amount));
        }
    }
    fn wait(&self, now: Instant, rule: Rule) -> Duration {
        let mut used = self.used;
        for (at, amount) in &self.events {
            used -= u128::from(*amount);
            if used < u128::from(rule.limit) {
                return rule
                    .window
                    .saturating_sub(now.saturating_duration_since(*at));
            }
        }
        Duration::ZERO
    }
}
/// One process-local registry. Its lock makes admission across resource and consumer atomic.
/// ponytail: one short lock per admission; shard only if profiling proves contention.
#[derive(Clone, Default)]
pub struct Registry(Arc<Mutex<HashMap<Key, History>>>);
#[derive(Clone)]
pub struct Policy {
    registry: Registry,
    rules: Vec<(Key, Rule)>,
}
impl Registry {
    /// Policies are immutable. Preparing a candidate never mutates a live policy's thresholds.
    pub fn bind(
        &self,
        scope: &str,
        request: Vec<Rule>,
        token: Vec<Rule>,
    ) -> Result<Policy, Invalid> {
        if scope.is_empty() {
            return Err(Invalid);
        }
        let mut rules = Vec::new();
        let mut unique = std::collections::HashSet::new();
        for (is_token, items) in [(false, request), (true, token)] {
            for rule in items {
                if rule.limit == 0
                    || rule.window.is_zero()
                    || Instant::now().checked_add(rule.window).is_none()
                    || !unique.insert((is_token, rule.window))
                {
                    return Err(Invalid);
                }
                rules.push((
                    Key {
                        scope: scope.into(),
                        token: is_token,
                        window: rule.window,
                    },
                    rule,
                ));
            }
        }
        // Expired, removed windows can be reclaimed; live handles recreate empty entries.
        let now = Instant::now();
        self.0.lock().unwrap().retain(|key, history| {
            history.prune(now, key.window);
            history.used > 0
        });
        Ok(Policy {
            registry: self.clone(),
            rules,
        })
    }
    /// Charge a logical request once after checking every request and actual-token window.
    /// In-flight work can exceed token bounds because no estimate/reservation is taken.
    pub fn admit(&self, policies: &[&Policy]) -> Result<(), Exceeded> {
        assert!(
            policies.iter().all(|p| Arc::ptr_eq(&self.0, &p.registry.0)),
            "policies must share the admission registry"
        );
        let mut state = self.0.lock().unwrap();
        let now = Instant::now();
        let mut wait = Duration::ZERO;
        for (key, rule) in policies.iter().flat_map(|p| &p.rules) {
            let history = state.entry(key.clone()).or_default();
            history.prune(now, key.window);
            if history.used >= u128::from(rule.limit) {
                wait = wait.max(history.wait(now, *rule));
            }
        }
        if !wait.is_zero() {
            return Err(Exceeded { retry_after: wait });
        }
        let mut charged = std::collections::HashSet::new();
        for (key, _) in policies.iter().flat_map(|p| &p.rules) {
            if !key.token && charged.insert(key) {
                state.get_mut(key).unwrap().add(now, 1);
            }
        }
        Ok(())
    }
}
/// One upstream attempt's cumulative usage. Missing usage charges zero; drop settles known usage.
/// Clone policies into the receipt so removal/replacement cannot discard in-flight accounting.
pub struct Receipt {
    policies: Vec<Policy>,
    observed: Option<u64>,
}
impl Receipt {
    pub fn new(policies: Vec<Policy>) -> Self {
        Self {
            policies,
            observed: None,
        }
    }
    pub fn observe(&mut self, total: u64) -> Result<(), Invalid> {
        if self.observed.is_some_and(|old| total < old) {
            return Err(Invalid);
        }
        self.observed = Some(total);
        Ok(())
    }
    pub fn observed(&self) -> Option<u64> {
        self.observed
    }
    pub fn settle(mut self) -> u64 {
        let amount = self.observed.unwrap_or(0);
        self.finish();
        amount
    }
    fn finish(&mut self) {
        let amount = self.observed.take().unwrap_or(0);
        if amount == 0 {
            return;
        }
        // Usually all policies share one registry. Keep the public receipt useful standalone too.
        let mut charged = std::collections::HashSet::new();
        for policy in &self.policies {
            let mut state = policy.registry.0.lock().unwrap();
            let now = Instant::now();
            for (key, _) in &policy.rules {
                if key.token && charged.insert((Arc::as_ptr(&policy.registry.0), key)) {
                    let history = state.entry(key.clone()).or_default();
                    history.prune(now, key.window);
                    history.add(now, amount);
                }
            }
        }
    }
}
impl Drop for Receipt {
    fn drop(&mut self) {
        self.finish();
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn expiry_boundary_and_wait_follow_the_blocking_usage_not_the_last_event() {
        let now = Instant::now();
        let mut history = History::default();
        history.add(now, 4);
        history.add(now + Duration::from_secs(10), 7);
        let rule = Rule {
            limit: 8,
            window: Duration::from_secs(60),
        };
        assert_eq!(
            history.wait(now + Duration::from_secs(20), rule),
            Duration::from_secs(40)
        );
        history.prune(now + Duration::from_secs(59), rule.window);
        assert_eq!(history.used, 11);
        history.prune(now + Duration::from_secs(60), rule.window);
        assert_eq!(history.used, 7);
        history.prune(now + Duration::from_secs(70), rule.window);
        assert_eq!(history.used, 0);
    }
}
