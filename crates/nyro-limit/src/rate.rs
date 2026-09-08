//! Process-local, continuously refilled request admission.

use std::{
    error::Error,
    fmt,
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

/// A shared token bucket. Clones consume the same capacity.
#[derive(Clone, Debug)]
pub struct RateLimit {
    bucket: Arc<Mutex<Bucket>>,
}

#[derive(Debug)]
struct Bucket {
    requests: u32,
    period_ns: u128,
    capacity: u128,
    credit: u128,
    updated_at: Instant,
}

/// A zero rate, zero burst, or period outside the monotonic clock's range.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct InvalidRate;

impl fmt::Display for InvalidRate {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("rate requests, period and burst must be positive, and period must fit the monotonic clock")
    }
}

impl Error for InvalidRate {}

/// Admission was denied. The wait is advisory: other callers may consume refill.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct RateExceeded {
    pub retry_after: Duration,
}

impl fmt::Display for RateExceeded {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("request rate limit reached")
    }
}

impl Error for RateExceeded {}

impl RateLimit {
    /// Start with `burst` tokens and refill `requests` tokens per `period`.
    pub fn new(requests: u32, period: Duration, burst: u32) -> Result<Self, InvalidRate> {
        let now = Instant::now();
        if requests == 0 || period.is_zero() || burst == 0 || now.checked_add(period).is_none() {
            return Err(InvalidRate);
        }
        // One token costs period_ns credits; each nanosecond adds requests credits.
        let period_ns = period.as_nanos();
        let capacity = period_ns
            .checked_mul(u128::from(burst))
            .ok_or(InvalidRate)?;
        Ok(Self {
            bucket: Arc::new(Mutex::new(Bucket {
                requests,
                period_ns,
                capacity,
                credit: capacity,
                updated_at: now,
            })),
        })
    }

    /// Consume one token immediately, without waiting or refunding it later.
    pub fn try_acquire(&self) -> Result<(), RateExceeded> {
        let mut bucket = self
            .bucket
            .lock()
            .unwrap_or_else(|error| error.into_inner());
        // Sample after locking so concurrent calls cannot apply timestamps out of order.
        bucket.try_acquire(Instant::now())
    }
}

impl Bucket {
    fn try_acquire(&mut self, now: Instant) -> Result<(), RateExceeded> {
        let elapsed = now.saturating_duration_since(self.updated_at).as_nanos();
        let refill = elapsed.saturating_mul(u128::from(self.requests));
        self.credit = self.credit.saturating_add(refill).min(self.capacity);
        self.updated_at = self.updated_at.max(now);
        if self.credit >= self.period_ns {
            self.credit -= self.period_ns;
            Ok(())
        } else {
            let nanos = (self.period_ns - self.credit).div_ceil(u128::from(self.requests));
            // The delay cannot exceed period, so these Duration components fit.
            Err(RateExceeded {
                retry_after: Duration::new(
                    (nanos / 1_000_000_000) as u64,
                    (nanos % 1_000_000_000) as u32,
                ),
            })
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{sync::Barrier, thread};

    fn start(limit: &RateLimit) -> Instant {
        limit.bucket.lock().unwrap().updated_at
    }

    fn acquire_at(limit: &RateLimit, now: Instant) -> Result<(), RateExceeded> {
        limit.bucket.lock().unwrap().try_acquire(now)
    }

    #[test]
    fn rejects_invalid_rates() {
        assert!(RateLimit::new(0, Duration::from_secs(1), 1).is_err());
        assert!(RateLimit::new(1, Duration::ZERO, 1).is_err());
        assert!(RateLimit::new(1, Duration::from_secs(1), 0).is_err());
        assert!(RateLimit::new(1, Duration::MAX, 1).is_err());
    }

    #[test]
    fn public_api_shares_consumed_burst_across_clones() {
        let limit = RateLimit::new(1, Duration::from_secs(86400), 2).unwrap();
        let clone = limit.clone();
        assert_eq!(limit.try_acquire(), Ok(()));
        assert_eq!(clone.try_acquire(), Ok(()));
        let denied = limit.try_acquire().unwrap_err();
        assert!(denied.retry_after > Duration::ZERO);
        assert!(denied.retry_after <= Duration::from_secs(86400));
        assert!(clone.try_acquire().is_err());
        assert!(
            RateLimit::new(1, Duration::from_secs(86400), 1)
                .unwrap()
                .try_acquire()
                .is_ok()
        );
    }

    #[test]
    fn refill_keeps_fractions_and_rounds_retry_up_to_nanoseconds() {
        let limit = RateLimit::new(3, Duration::from_nanos(10), 2).unwrap();
        let now = start(&limit);
        assert_eq!(acquire_at(&limit, now), Ok(()));
        assert_eq!(acquire_at(&limit, now), Ok(()));
        for (elapsed, retry) in [(0, 4), (1, 3), (2, 2), (3, 1)] {
            assert_eq!(
                acquire_at(&limit, now + Duration::from_nanos(elapsed)),
                Err(RateExceeded {
                    retry_after: Duration::from_nanos(retry)
                })
            );
        }
        assert_eq!(acquire_at(&limit, now + Duration::from_nanos(4)), Ok(()));
        assert_eq!(
            acquire_at(&limit, now + Duration::from_nanos(6)),
            Err(RateExceeded {
                retry_after: Duration::from_nanos(1)
            })
        );
        assert_eq!(acquire_at(&limit, now + Duration::from_nanos(7)), Ok(()));
        assert_eq!(acquire_at(&limit, now + Duration::from_nanos(10)), Ok(()));
    }

    #[test]
    fn refill_admits_at_exact_boundary() {
        let limit = RateLimit::new(2, Duration::from_secs(1), 1).unwrap();
        let now = start(&limit);
        assert_eq!(acquire_at(&limit, now), Ok(()));
        assert_eq!(
            acquire_at(&limit, now + Duration::from_nanos(499_999_999)),
            Err(RateExceeded {
                retry_after: Duration::from_nanos(1)
            })
        );
        assert_eq!(acquire_at(&limit, now + Duration::from_millis(500)), Ok(()));
    }

    #[test]
    fn idle_refill_is_capped_at_burst_without_saved_overflow() {
        let limit = RateLimit::new(2, Duration::from_secs(1), 3).unwrap();
        let now = start(&limit) + Duration::from_secs(100);
        for _ in 0..3 {
            assert_eq!(acquire_at(&limit, now), Ok(()));
        }
        assert_eq!(
            acquire_at(&limit, now),
            Err(RateExceeded {
                retry_after: Duration::from_millis(500)
            })
        );
    }

    #[test]
    fn large_rates_and_periods_preserve_integer_bounds() {
        let period = Duration::from_secs(20_000_000_000);
        let limit = RateLimit::new(u32::MAX, period, u32::MAX).unwrap();
        let now = start(&limit);
        assert_eq!(acquire_at(&limit, now + period), Ok(()));

        let limit = RateLimit::new(1, period, 1).unwrap();
        let now = start(&limit);
        assert_eq!(acquire_at(&limit, now), Ok(()));
        assert_eq!(
            acquire_at(&limit, now),
            Err(RateExceeded {
                retry_after: period
            })
        );
        assert_eq!(acquire_at(&limit, now + period), Ok(()));
    }

    #[test]
    fn concurrent_admissions_share_one_atomic_burst() {
        let limit = RateLimit::new(1, Duration::from_secs(1), 3).unwrap();
        let now = start(&limit);
        let barrier = Barrier::new(16);
        thread::scope(|scope| {
            let workers: Vec<_> = (0..16)
                .map(|_| {
                    let limit = &limit;
                    let barrier = &barrier;
                    scope.spawn(move || {
                        barrier.wait();
                        acquire_at(limit, now).is_ok()
                    })
                })
                .collect();
            let admitted = workers
                .into_iter()
                .map(|worker| usize::from(worker.join().unwrap()))
                .sum::<usize>();
            assert_eq!(admitted, 3);
        });
    }
}
