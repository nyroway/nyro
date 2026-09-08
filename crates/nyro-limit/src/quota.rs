//! Process-local cumulative admission for arbitrary units.

use std::sync::{Arc, Mutex};
use thiserror::Error;

/// A cumulative budget with no refill or reset. Clones share one balance.
#[derive(Clone, Debug)]
pub struct Quota {
    state: Arc<Mutex<QuotaSnapshot>>,
}

#[derive(Clone, Copy, Debug, Error, Eq, PartialEq)]
pub enum QuotaError {
    #[error("quota limit and reservation amount must be positive")]
    Invalid,
    #[error("quota limit exceeded")]
    Exceeded,
}

/// An atomic view of settled usage and pending reservations.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct QuotaSnapshot {
    pub limit: u64,
    /// Actual settled usage, including any overage beyond the limit.
    pub used: u128,
    pub reserved: u64,
}

/// A pending charge. Dropping it charges its reserved amount exactly once.
#[must_use = "a dropped reservation charges its reserved amount"]
#[derive(Debug)]
pub struct Reservation {
    quota: Quota,
    amount: u64,
}

impl Quota {
    pub fn new(limit: u64) -> Result<Self, QuotaError> {
        if limit == 0 {
            return Err(QuotaError::Invalid);
        }
        Ok(Self {
            state: Arc::new(Mutex::new(QuotaSnapshot {
                limit,
                used: 0,
                reserved: 0,
            })),
        })
    }

    /// Reserve positive units if settled usage plus pending units leaves room.
    pub fn reserve(&self, amount: u64) -> Result<Reservation, QuotaError> {
        if amount == 0 {
            return Err(QuotaError::Invalid);
        }
        let mut state = self.state.lock().unwrap_or_else(|error| error.into_inner());
        if state.used + u128::from(state.reserved) + u128::from(amount) > u128::from(state.limit) {
            return Err(QuotaError::Exceeded);
        }
        // Admission above guarantees reserved + amount fits the u64 limit.
        state.reserved += amount;
        Ok(Reservation {
            quota: self.clone(),
            amount,
        })
    }

    pub fn snapshot(&self) -> QuotaSnapshot {
        *self.state.lock().unwrap_or_else(|error| error.into_inner())
    }
}

impl Reservation {
    pub fn amount(&self) -> u64 {
        self.amount
    }

    /// Replace this reservation with actual usage, retaining any overage debt.
    pub fn settle(mut self, actual: u64) {
        self.finish(actual);
    }

    /// Explicitly settle zero usage and return all reserved capacity.
    pub fn release(self) {
        self.settle(0);
    }

    fn finish(&mut self, actual: u64) {
        if self.amount == 0 {
            return;
        }
        let mut state = self
            .quota
            .state
            .lock()
            .unwrap_or_else(|error| error.into_inner());
        state.reserved -= self.amount;
        // Once used reaches limit, admission stops. At most limit positive
        // reservations can still settle u64 amounts, so u128 cannot overflow.
        state.used += u128::from(actual);
        self.amount = 0;
    }
}

impl Drop for Reservation {
    fn drop(&mut self) {
        self.finish(self.amount);
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{sync::Barrier, thread};

    #[test]
    fn rejects_zero_limits_and_reservations_without_mutation() {
        assert_eq!(Quota::new(0).unwrap_err(), QuotaError::Invalid);
        let quota = Quota::new(10).unwrap();
        assert_eq!(quota.reserve(0).unwrap_err(), QuotaError::Invalid);
        assert_eq!(
            quota.snapshot(),
            QuotaSnapshot {
                limit: 10,
                used: 0,
                reserved: 0
            }
        );
    }

    #[test]
    fn clones_share_used_and_pending_capacity_at_exact_boundary() {
        let quota = Quota::new(10).unwrap();
        let clone = quota.clone();
        quota.reserve(4).unwrap().settle(3);
        let pending = clone.reserve(7).unwrap();
        assert_eq!(pending.amount(), 7);
        assert_eq!(
            quota.snapshot(),
            QuotaSnapshot {
                limit: 10,
                used: 3,
                reserved: 7
            }
        );
        assert_eq!(quota.reserve(1).unwrap_err(), QuotaError::Exceeded);
        assert_eq!(clone.snapshot(), quota.snapshot());
        pending.settle(7);
        assert_eq!(
            quota.snapshot(),
            QuotaSnapshot {
                limit: 10,
                used: 10,
                reserved: 0
            }
        );
    }

    #[test]
    fn lower_actual_refunds_unused_reservation_exactly_once() {
        let quota = Quota::new(10).unwrap();
        quota.reserve(10).unwrap().settle(4);
        assert_eq!(
            quota.snapshot(),
            QuotaSnapshot {
                limit: 10,
                used: 4,
                reserved: 0
            }
        );
        let pending = quota.reserve(6).unwrap();
        assert_eq!(quota.reserve(1).unwrap_err(), QuotaError::Exceeded);
        pending.settle(0);
        assert_eq!(
            quota.snapshot(),
            QuotaSnapshot {
                limit: 10,
                used: 4,
                reserved: 0
            }
        );
    }

    #[test]
    fn release_restores_capacity_without_drop_charge() {
        let quota = Quota::new(10).unwrap();
        quota.reserve(10).unwrap().release();
        assert_eq!(
            quota.snapshot(),
            QuotaSnapshot {
                limit: 10,
                used: 0,
                reserved: 0
            }
        );
        assert_eq!(quota.reserve(10).unwrap().amount(), 10);
    }

    #[test]
    fn abandoned_reservations_charge_their_amount() {
        let quota = Quota::new(10).unwrap();
        drop(quota.reserve(6).unwrap());
        assert_eq!(
            quota.snapshot(),
            QuotaSnapshot {
                limit: 10,
                used: 6,
                reserved: 0
            }
        );
        assert_eq!(quota.reserve(5).unwrap_err(), QuotaError::Exceeded);
        drop(quota.reserve(4).unwrap());
        assert_eq!(quota.snapshot().used, 10);
    }

    #[test]
    fn overage_records_debt_and_preserves_other_pending_reservations() {
        let quota = Quota::new(10).unwrap();
        let first = quota.reserve(4).unwrap();
        let second = quota.reserve(6).unwrap();
        first.settle(20);
        assert_eq!(
            quota.snapshot(),
            QuotaSnapshot {
                limit: 10,
                used: 20,
                reserved: 6
            }
        );
        assert_eq!(quota.reserve(1).unwrap_err(), QuotaError::Exceeded);
        second.release();
        assert_eq!(
            quota.snapshot(),
            QuotaSnapshot {
                limit: 10,
                used: 20,
                reserved: 0
            }
        );
        assert_eq!(quota.reserve(1).unwrap_err(), QuotaError::Exceeded);
    }

    #[test]
    fn maximum_amounts_do_not_wrap_admission_or_cumulative_debt() {
        let quota = Quota::new(u64::MAX).unwrap();
        let first = quota.reserve(u64::MAX - 1).unwrap();
        assert_eq!(quota.reserve(2).unwrap_err(), QuotaError::Exceeded);
        let second = quota.reserve(1).unwrap();
        assert_eq!(quota.reserve(u64::MAX).unwrap_err(), QuotaError::Exceeded);
        first.settle(u64::MAX);
        second.settle(u64::MAX);
        assert_eq!(
            quota.snapshot(),
            QuotaSnapshot {
                limit: u64::MAX,
                used: u128::from(u64::MAX) * 2,
                reserved: 0,
            }
        );
        assert_eq!(quota.reserve(u64::MAX).unwrap_err(), QuotaError::Exceeded);
    }

    #[test]
    fn concurrent_reservations_and_settlements_share_one_atomic_balance() {
        let quota = Quota::new(30).unwrap();
        let start = Barrier::new(16);
        let settle = Barrier::new(16);
        thread::scope(|scope| {
            let workers: Vec<_> = (0..16)
                .map(|_| {
                    let quota = quota.clone();
                    let start = &start;
                    let settle = &settle;
                    scope.spawn(move || {
                        start.wait();
                        let pending = quota.reserve(10).ok();
                        settle.wait();
                        if let Some(pending) = pending {
                            pending.settle(7);
                            true
                        } else {
                            false
                        }
                    })
                })
                .collect();
            let admitted = workers
                .into_iter()
                .map(|worker| usize::from(worker.join().unwrap()))
                .sum::<usize>();
            assert_eq!(admitted, 3);
        });
        assert_eq!(
            quota.snapshot(),
            QuotaSnapshot {
                limit: 30,
                used: 21,
                reserved: 0
            }
        );
        assert_eq!(quota.reserve(10).unwrap_err(), QuotaError::Exceeded);
        quota.reserve(9).unwrap().release();
    }
}
