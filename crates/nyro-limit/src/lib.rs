//! nyro-limit.

pub mod quota;
pub mod rate;

use std::sync::Arc;
use thiserror::Error;
use tokio::sync::{OwnedSemaphorePermit, Semaphore};

#[derive(Debug, Error)]
pub enum LimitError {
    #[error("concurrency limit must be between 1 and {max}")]
    InvalidCapacity { max: usize },
    #[error("concurrency limit reached")]
    Reached,
}

#[derive(Clone)]
pub struct ConcurrencyLimit {
    semaphore: Arc<Semaphore>,
}

pub struct Permit {
    _permit: OwnedSemaphorePermit,
}

impl ConcurrencyLimit {
    pub fn new(capacity: usize) -> Result<Self, LimitError> {
        if capacity == 0 || capacity > Semaphore::MAX_PERMITS {
            return Err(LimitError::InvalidCapacity {
                max: Semaphore::MAX_PERMITS,
            });
        }
        Ok(Self {
            semaphore: Arc::new(Semaphore::new(capacity)),
        })
    }

    pub fn try_acquire(&self) -> Result<Permit, LimitError> {
        self.semaphore
            .clone()
            .try_acquire_owned()
            .map(|permit| Permit { _permit: permit })
            .map_err(|_| LimitError::Reached)
    }

    pub fn available(&self) -> usize {
        self.semaphore.available_permits()
    }
}

#[cfg(test)]
mod tests {
    use super::ConcurrencyLimit;
    use std::{
        sync::{Arc, Barrier, mpsc},
        thread,
    };

    #[test]
    fn rejects_invalid_capacity() {
        assert!(ConcurrencyLimit::new(0).is_err());
        assert!(ConcurrencyLimit::new(usize::MAX).is_err());
    }

    #[test]
    fn clones_share_capacity_and_dropped_permits_release_it() {
        let limit = ConcurrencyLimit::new(1).unwrap();
        let clone = limit.clone();
        let permit = limit.try_acquire().unwrap();

        assert_eq!(clone.available(), 0);
        assert!(clone.try_acquire().is_err());

        drop(permit);
        assert_eq!(clone.available(), 1);
        assert!(clone.try_acquire().is_ok());
    }

    #[test]
    fn concurrent_calls_never_exceed_shared_capacity() {
        let limit = ConcurrencyLimit::new(2).unwrap();
        let start = Arc::new(Barrier::new(5));
        let release = Arc::new(Barrier::new(5));
        let (tx, rx) = mpsc::channel();

        thread::scope(|scope| {
            for _ in 0..4 {
                let limit = limit.clone();
                let start = Arc::clone(&start);
                let release = Arc::clone(&release);
                let tx = tx.clone();
                scope.spawn(move || {
                    start.wait();
                    let permit = limit.try_acquire().ok();
                    tx.send(permit.is_some()).unwrap();
                    release.wait();
                    drop(permit);
                });
            }
            drop(tx);
            start.wait();
            let acquired = (0..4)
                .filter_map(|_| rx.recv().ok())
                .filter(|acquired| *acquired)
                .count();
            assert_eq!(acquired, 2);
            release.wait();
        });

        assert_eq!(limit.available(), 2);
    }
}
