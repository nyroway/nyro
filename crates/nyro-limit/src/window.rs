//! Rolling usage budgets with pending reservations that survive window boundaries.

use crate::quota::{Quota, QuotaError, QuotaSnapshot, Reservation};
use std::{
    collections::VecDeque,
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

/// Generic units, not a token estimator. Clones share settled usage and pending units.
#[derive(Clone, Debug)]
pub struct WindowQuota {
    state: Arc<Mutex<State>>,
}

#[derive(Debug)]
struct State {
    windows: Vec<Window>,
    reserved: u64,
}
#[derive(Debug)]
struct Window {
    limit: u64,
    period: Duration,
    used: u128,
    settled: VecDeque<(Instant, u64)>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, thiserror::Error)]
pub enum WindowQuotaError {
    #[error("usage windows require positive limits, periods and reservations")]
    Invalid,
    #[error("rolling usage limit exceeded")]
    Exceeded {
        /// Advisory wait until settled usage expires. None means pending units
        /// alone prevent admission, or the requested amount exceeds the limit.
        retry_after: Option<Duration>,
    },
    #[error(transparent)]
    Cumulative(#[from] QuotaError),
}

/// Pending units do not expire. Drop settles the reserved amount at drop time.
#[must_use = "a dropped reservation charges its reserved amount"]
#[derive(Debug)]
pub struct WindowReservation {
    quota: WindowQuota,
    amount: u64,
}

impl WindowQuota {
    pub fn new(
        windows: impl IntoIterator<Item = (u64, Duration)>,
    ) -> Result<Self, WindowQuotaError> {
        let now = Instant::now();
        let windows = windows
            .into_iter()
            .map(|(limit, period)| {
                if limit == 0 || period.is_zero() || now.checked_add(period).is_none() {
                    return Err(WindowQuotaError::Invalid);
                }
                Ok(Window {
                    limit,
                    period,
                    used: 0,
                    settled: VecDeque::new(),
                })
            })
            .collect::<Result<Vec<_>, _>>()?;
        if windows.is_empty() {
            return Err(WindowQuotaError::Invalid);
        }
        Ok(Self {
            state: Arc::new(Mutex::new(State {
                windows,
                reserved: 0,
            })),
        })
    }

    pub fn reserve(&self, amount: u64) -> Result<WindowReservation, WindowQuotaError> {
        let mut state = self.state.lock().unwrap_or_else(|e| e.into_inner());
        state.check(amount, Instant::now())?;
        Ok(self.reserve_locked(amount, &mut state))
    }

    /// Reserve from all windows and a cumulative budget atomically. Neither budget
    /// keeps a reservation on rejection. Lock order is windows then cumulative.
    pub fn reserve_with_quota(
        &self,
        amount: u64,
        quota: &Quota,
        quota_amount: u64,
    ) -> Result<(WindowReservation, Reservation), WindowQuotaError> {
        let mut state = self.state.lock().unwrap_or_else(|e| e.into_inner());
        let mut cumulative = quota.state.lock().unwrap_or_else(|e| e.into_inner());
        state.check(amount, Instant::now())?;
        let receipt = quota.reserve_locked(quota_amount, &mut cumulative)?;
        Ok((self.reserve_locked(amount, &mut state), receipt))
    }

    fn reserve_locked(&self, amount: u64, state: &mut State) -> WindowReservation {
        // Successful admission bounds this sum by every window's u64 limit.
        state.reserved += amount;
        WindowReservation {
            quota: self.clone(),
            amount,
        }
    }

    /// Atomic snapshots in constructor window order, with expired usage removed.
    pub fn snapshots(&self) -> Vec<QuotaSnapshot> {
        let mut state = self.state.lock().unwrap_or_else(|e| e.into_inner());
        state.prune(Instant::now());
        state
            .windows
            .iter()
            .map(|window| QuotaSnapshot {
                limit: window.limit,
                used: window.used,
                reserved: state.reserved,
            })
            .collect()
    }

    /// True only when there are no pending units and all settled usage has expired.
    pub fn is_empty(&self) -> bool {
        let mut state = self.state.lock().unwrap_or_else(|e| e.into_inner());
        state.prune(Instant::now());
        state.reserved == 0 && state.windows.iter().all(|window| window.used == 0)
    }
}

impl State {
    fn prune(&mut self, now: Instant) {
        for window in &mut self.windows {
            while window
                .settled
                .front()
                .is_some_and(|(at, _)| now.saturating_duration_since(*at) >= window.period)
            {
                window.used -= u128::from(window.settled.pop_front().unwrap().1);
            }
        }
    }

    fn check(&mut self, amount: u64, now: Instant) -> Result<(), WindowQuotaError> {
        if amount == 0 {
            return Err(WindowQuotaError::Invalid);
        }
        self.prune(now);
        let pending = u128::from(self.reserved) + u128::from(amount);
        let mut retry = None;
        for window in &self.windows {
            let limit = u128::from(window.limit);
            if pending > limit {
                return Err(WindowQuotaError::Exceeded { retry_after: None });
            }
            let mut used = window.used;
            if used + pending <= limit {
                continue;
            }
            for (at, actual) in &window.settled {
                used -= u128::from(*actual);
                if used + pending <= limit {
                    let wait = window
                        .period
                        .saturating_sub(now.saturating_duration_since(*at));
                    retry = Some(retry.map_or(wait, |previous: Duration| previous.max(wait)));
                    break;
                }
            }
        }
        if retry.is_some() {
            Err(WindowQuotaError::Exceeded { retry_after: retry })
        } else {
            Ok(())
        }
    }

    fn settle(&mut self, reserved: u64, actual: u64, now: Instant) {
        self.prune(now);
        self.reserved -= reserved;
        if actual == 0 {
            return;
        }
        for window in &mut self.windows {
            // Admission stops at limit, and at most limit positive reservations can
            // remain in flight; their u64 settlements plus usage fit in u128.
            window.used += u128::from(actual);
            window.settled.push_back((now, actual));
        }
    }
}

impl WindowReservation {
    pub fn amount(&self) -> u64 {
        self.amount
    }
    pub fn settle(mut self, actual: u64) {
        self.finish(actual);
    }
    pub fn release(self) {
        self.settle(0);
    }
    fn finish(&mut self, actual: u64) {
        if self.amount == 0 {
            return;
        }
        let mut state = self.quota.state.lock().unwrap_or_else(|e| e.into_inner());
        state.settle(self.amount, actual, Instant::now());
        self.amount = 0;
    }
}
impl Drop for WindowReservation {
    fn drop(&mut self) {
        self.finish(self.amount);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{sync::Barrier, thread};

    fn reserve_at(
        quota: &WindowQuota,
        amount: u64,
        now: Instant,
    ) -> Result<WindowReservation, WindowQuotaError> {
        let mut state = quota.state.lock().unwrap();
        state.check(amount, now)?;
        Ok(quota.reserve_locked(amount, &mut state))
    }
    fn settle_at(mut receipt: WindowReservation, actual: u64, now: Instant) {
        receipt
            .quota
            .state
            .lock()
            .unwrap()
            .settle(receipt.amount, actual, now);
        receipt.amount = 0;
    }
    fn denied(quota: &WindowQuota, amount: u64, now: Instant, wait: Option<Duration>) {
        assert_eq!(
            reserve_at(quota, amount, now).unwrap_err(),
            WindowQuotaError::Exceeded { retry_after: wait }
        );
    }

    #[test]
    fn pending_does_not_expire_and_settlement_starts_each_rolling_window() {
        let quota = WindowQuota::new([
            (10, Duration::from_secs(60)),
            (20, Duration::from_secs(86400)),
        ])
        .unwrap();
        let now = Instant::now();
        let first = reserve_at(&quota, 10, now).unwrap();
        denied(&quota, 1, now + Duration::from_secs(86400), None);
        settle_at(first, 7, now + Duration::from_secs(86400));
        denied(
            &quota,
            4,
            now + Duration::from_secs(86459),
            Some(Duration::from_secs(1)),
        );
        let second = reserve_at(&quota, 10, now + Duration::from_secs(86460)).unwrap();
        settle_at(second, 10, now + Duration::from_secs(86460));
        denied(
            &quota,
            4,
            now + Duration::from_secs(86520),
            Some(Duration::from_secs(86280)),
        );
        reserve_at(&quota, 10, now + Duration::from_secs(172800))
            .unwrap()
            .release();
    }

    #[test]
    fn retry_wait_accounts_for_multiple_expirations_and_pending_units() {
        let quota = WindowQuota::new([(10, Duration::from_secs(60))]).unwrap();
        let now = Instant::now();
        settle_at(reserve_at(&quota, 1, now).unwrap(), 3, now);
        settle_at(
            reserve_at(&quota, 1, now).unwrap(),
            4,
            now + Duration::from_secs(10),
        );
        let pending = reserve_at(&quota, 2, now + Duration::from_secs(10)).unwrap();
        denied(
            &quota,
            8,
            now + Duration::from_secs(20),
            Some(Duration::from_secs(50)),
        );
        denied(&quota, 9, now + Duration::from_secs(20), None);
        pending.release();
    }

    #[test]
    fn atomic_rejection_never_keeps_a_partial_model_or_window_reservation() {
        let window = WindowQuota::new([(10, Duration::from_secs(86400))]).unwrap();
        let model = Quota::new(4).unwrap();
        let held = window.reserve(10).unwrap();
        assert!(window.reserve_with_quota(1, &model, 4).is_err());
        assert_eq!(model.snapshot().reserved, 0);
        held.release();
        let held_model = model.reserve(4).unwrap();
        assert_eq!(
            window.reserve_with_quota(10, &model, 1).unwrap_err(),
            WindowQuotaError::Cumulative(QuotaError::Exceeded)
        );
        assert!(window.is_empty());
        held_model.release();
        let (w, m) = window.reserve_with_quota(10, &model, 4).unwrap();
        w.settle(3);
        m.settle(3);
        assert_eq!(window.snapshots()[0].used, 3);
        assert_eq!(model.snapshot().used, 3);
    }

    #[test]
    fn zero_release_drop_and_overage_preserve_other_pending_receipts() {
        let quota = WindowQuota::new([(10, Duration::from_secs(86400))]).unwrap();
        quota.reserve(10).unwrap().settle(0);
        assert!(quota.is_empty());
        quota.reserve(10).unwrap().release();
        assert!(quota.is_empty());
        drop(quota.reserve(3).unwrap());
        let first = quota.reserve(3).unwrap();
        let second = quota.reserve(4).unwrap();
        first.settle(30);
        assert_eq!(
            quota.snapshots()[0],
            QuotaSnapshot {
                limit: 10,
                used: 33,
                reserved: 4
            }
        );
        assert!(quota.reserve(1).is_err());
        second.release();
        assert_eq!(quota.snapshots()[0].reserved, 0);
    }

    #[test]
    fn validation_and_maximum_units_never_wrap() {
        assert!(WindowQuota::new([]).is_err());
        for (count, period) in [
            (0, Duration::from_secs(1)),
            (1, Duration::ZERO),
            (1, Duration::MAX),
        ] {
            assert!(WindowQuota::new([(count, period)]).is_err());
        }
        let quota = WindowQuota::new([(u64::MAX, Duration::from_secs(86400))]).unwrap();
        assert_eq!(quota.reserve(0).unwrap_err(), WindowQuotaError::Invalid);
        let a = quota.reserve(u64::MAX - 1).unwrap();
        let b = quota.reserve(1).unwrap();
        assert!(quota.reserve(1).is_err());
        a.settle(u64::MAX);
        b.settle(u64::MAX);
        assert_eq!(quota.snapshots()[0].used, u128::from(u64::MAX) * 2);
    }

    #[test]
    fn concurrent_combinations_cannot_overbook_or_lose_other_subject_capacity() {
        let model = Quota::new(30).unwrap();
        let subjects: Vec<_> = (0..16)
            .map(|_| WindowQuota::new([(10, Duration::from_secs(86400))]).unwrap())
            .collect();
        let barrier = Barrier::new(16);
        thread::scope(|scope| {
            let workers: Vec<_> = subjects
                .iter()
                .map(|subject| {
                    let model = &model;
                    let barrier = &barrier;
                    scope.spawn(move || {
                        barrier.wait();
                        subject.reserve_with_quota(10, model, 10).ok()
                    })
                })
                .collect();
            let receipts: Vec<_> = workers
                .into_iter()
                .filter_map(|w| w.join().unwrap())
                .collect();
            assert_eq!(receipts.len(), 3);
            assert_eq!(subjects.iter().filter(|s| s.is_empty()).count(), 13);
            drop(receipts);
        });
        assert_eq!(model.snapshot().used, 30);
        let shared = WindowQuota::new([(30, Duration::from_secs(86400))]).unwrap();
        thread::scope(|scope| {
            let workers: Vec<_> = (0..16)
                .map(|_| scope.spawn(|| shared.reserve(10).ok()))
                .collect();
            let receipts: Vec<_> = workers
                .into_iter()
                .filter_map(|w| w.join().unwrap())
                .collect();
            assert_eq!(receipts.len(), 3);
        });
        assert_eq!(shared.snapshots()[0].used, 30);
    }
    #[test]
    fn simultaneous_window_rejection_returns_the_longest_expiration() {
        let quota = WindowQuota::new([
            (10, Duration::from_secs(60)),
            (10, Duration::from_secs(86400)),
        ])
        .unwrap();
        let now = Instant::now();
        settle_at(reserve_at(&quota, 10, now).unwrap(), 10, now);
        denied(
            &quota,
            1,
            now + Duration::from_secs(1),
            Some(Duration::from_secs(86399)),
        );
    }
}
