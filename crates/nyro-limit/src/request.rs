//! Exact rolling request windows, optionally combined with a token bucket.

use crate::rate::{Bucket, RateExceeded, RateLimit};
use std::{
    collections::VecDeque,
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

/// Clones share rolling histories. Each admitted request occupies one slot in every window.
#[derive(Clone, Debug)]
pub struct RequestLimit {
    windows: Arc<Mutex<Windows>>,
}

#[derive(Debug)]
struct Windows(Vec<Window>);

#[derive(Debug)]
struct Window {
    count: u32,
    period: Duration,
    admitted: VecDeque<Instant>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, thiserror::Error)]
#[error("request windows require at least one positive count and positive representable period")]
pub struct InvalidRequestLimit;

impl RequestLimit {
    pub fn new(
        windows: impl IntoIterator<Item = (u32, Duration)>,
    ) -> Result<Self, InvalidRequestLimit> {
        let now = Instant::now();
        let windows = windows
            .into_iter()
            .map(|(count, period)| {
                if count == 0 || period.is_zero() || now.checked_add(period).is_none() {
                    return Err(InvalidRequestLimit);
                }
                Ok(Window {
                    count,
                    period,
                    admitted: VecDeque::new(),
                })
            })
            .collect::<Result<Vec<_>, _>>()?;
        if windows.is_empty() {
            return Err(InvalidRequestLimit);
        }
        Ok(Self {
            windows: Arc::new(Mutex::new(Windows(windows))),
        })
    }

    /// Atomically admit to all windows and an optional token bucket. Rejection charges none.
    /// Lock order is always windows then bucket; no lock survives this synchronous call.
    /// Retry delay is advisory and names the longest blocked window, or the bucket if
    /// all windows permit admission. Successful admissions are never refunded.
    pub fn try_acquire(&self, rate: Option<&RateLimit>) -> Result<(), RateExceeded> {
        let mut windows = self
            .windows
            .lock()
            .unwrap_or_else(|error| error.into_inner());
        let mut bucket = rate.map(|rate| {
            rate.bucket
                .lock()
                .unwrap_or_else(|error| error.into_inner())
        });
        // One timestamp after both locks prevents contention from backdating admissions.
        windows.try_acquire(Instant::now(), bucket.as_deref_mut())
    }

    /// Prune expired admissions and report whether all windows are empty.
    /// Registries can reclaim an unreferenced limiter once this returns true.
    pub fn is_empty(&self) -> bool {
        let mut windows = self
            .windows
            .lock()
            .unwrap_or_else(|error| error.into_inner());
        windows.prune(Instant::now());
        windows.0.iter().all(|window| window.admitted.is_empty())
    }
}

impl Windows {
    fn prune(&mut self, now: Instant) {
        for window in &mut self.0 {
            while window
                .admitted
                .front()
                .is_some_and(|at| now.saturating_duration_since(*at) >= window.period)
            {
                window.admitted.pop_front();
            }
        }
    }

    fn try_acquire(
        &mut self,
        now: Instant,
        bucket: Option<&mut Bucket>,
    ) -> Result<(), RateExceeded> {
        self.prune(now);
        let retry_after = self
            .0
            .iter()
            .filter(|window| window.admitted.len() >= window.count as usize)
            .map(|window| {
                window.period.saturating_sub(
                    now.saturating_duration_since(*window.admitted.front().unwrap()),
                )
            })
            .max();
        if let Some(retry_after) = retry_after {
            return Err(RateExceeded { retry_after });
        }
        if let Some(bucket) = bucket {
            bucket.try_acquire(now)?;
        }
        for window in &mut self.0 {
            window.admitted.push_back(now);
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{sync::Barrier, thread};

    fn acquire_at(limit: &RequestLimit, now: Instant) -> Result<(), RateExceeded> {
        limit.windows.lock().unwrap().try_acquire(now, None)
    }

    #[test]
    fn rolling_windows_expire_at_exact_boundary_and_return_longest_wait() {
        let limit = RequestLimit::new([
            (2, Duration::from_secs(60)),
            (3, Duration::from_secs(86400)),
        ])
        .unwrap();
        let now = Instant::now();
        acquire_at(&limit, now).unwrap();
        acquire_at(&limit, now + Duration::from_secs(10)).unwrap();
        assert_eq!(
            acquire_at(&limit, now + Duration::from_secs(59))
                .unwrap_err()
                .retry_after,
            Duration::from_secs(1)
        );
        acquire_at(&limit, now + Duration::from_secs(60)).unwrap();
        assert_eq!(
            acquire_at(&limit, now + Duration::from_secs(60))
                .unwrap_err()
                .retry_after,
            Duration::from_secs(86340)
        );
        assert_eq!(
            acquire_at(&limit, now + Duration::from_secs(86399))
                .unwrap_err()
                .retry_after,
            Duration::from_secs(1)
        );
        acquire_at(&limit, now + Duration::from_secs(86400)).unwrap();
        // Rejecting the day window did not insert into the minute window.
        acquire_at(&limit, now + Duration::from_secs(86410)).unwrap();
    }

    #[test]
    fn rejection_never_charges_other_windows_or_model_bucket() {
        let period = Duration::from_secs(86400);
        let a = RequestLimit::new([(1, period)]).unwrap();
        let b = RequestLimit::new([(1, period)]).unwrap();
        let first = RateLimit::new(1, period, 1).unwrap();
        let second = RateLimit::new(1, period, 1).unwrap();
        a.try_acquire(Some(&first)).unwrap();
        assert!(a.try_acquire(Some(&second)).is_err());
        assert!(b.try_acquire(Some(&first)).is_err());
        b.try_acquire(Some(&second)).unwrap();
        assert!(b.try_acquire(None).is_err());
    }

    #[test]
    fn concurrent_subjects_share_atomic_model_capacity_without_losing_window_credit() {
        let period = Duration::from_secs(86400);
        let model = RateLimit::new(1, period, 7).unwrap();
        let subjects: Vec<_> = (0..16)
            .map(|_| RequestLimit::new([(1, period)]).unwrap())
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
                        subject.try_acquire(Some(model)).is_ok()
                    })
                })
                .collect();
            assert_eq!(
                workers
                    .into_iter()
                    .map(|w| usize::from(w.join().unwrap()))
                    .sum::<usize>(),
                7
            );
        });
        // Exactly the nine model-denied subjects still have their own admission.
        assert_eq!(
            subjects
                .iter()
                .filter(|s| s.try_acquire(None).is_ok())
                .count(),
            9
        );
        let shared = RequestLimit::new([(3, period)]).unwrap();
        thread::scope(|scope| {
            let workers: Vec<_> = (0..16)
                .map(|_| scope.spawn(|| shared.try_acquire(None).is_ok()))
                .collect();
            assert_eq!(
                workers
                    .into_iter()
                    .map(|w| usize::from(w.join().unwrap()))
                    .sum::<usize>(),
                3
            );
        });
    }

    #[test]
    fn invalid_windows_reject_and_idle_histories_are_reclaimed() {
        assert!(RequestLimit::new([]).is_err());
        assert!(RequestLimit::new([(0, Duration::from_secs(1))]).is_err());
        assert!(RequestLimit::new([(1, Duration::ZERO)]).is_err());
        assert!(RequestLimit::new([(1, Duration::MAX)]).is_err());
        let limit = RequestLimit::new([(1, Duration::from_secs(60))]).unwrap();
        assert!(limit.is_empty());
        let now = Instant::now();
        acquire_at(&limit, now).unwrap();
        assert!(!limit.is_empty());
        let mut windows = limit.windows.lock().unwrap();
        windows.prune(now + Duration::from_secs(60));
        assert!(windows.0.iter().all(|window| window.admitted.is_empty()));
    }
}
