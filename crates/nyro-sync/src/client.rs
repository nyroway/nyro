//! Application feedback and reconnection are shared by all configuration sources.
use crate::{ApplicationResult, Error, HttpClient, Hub, Poll, Snapshot};
use serde::{Serialize, de::DeserializeOwned};
use std::{future::Future, sync::Arc, time::Duration};
use tokio_util::sync::CancellationToken;

#[async_trait::async_trait]
pub trait Source<T>: Send + Sync {
    async fn poll(&self, request: &Poll) -> Result<Option<Arc<Snapshot<T>>>, Error>;
}

#[async_trait::async_trait]
impl<T: Serialize + Send + Sync> Source<T> for Hub<T> {
    async fn poll(&self, request: &Poll) -> Result<Option<Arc<Snapshot<T>>>, Error> {
        Hub::poll(self, request.clone(), Duration::from_secs(25)).await
    }
}

#[async_trait::async_trait]
impl<T: Serialize + DeserializeOwned + Send + Sync> Source<T> for HttpClient<T> {
    async fn poll(&self, request: &Poll) -> Result<Option<Arc<Snapshot<T>>>, Error> {
        HttpClient::poll(self, request)
            .await
            .map(|snapshot| snapshot.map(Arc::new))
    }
}

/// Codes are fixed, credential-free identifiers selected by the application.
pub enum ApplyError {
    Rejected(&'static str),
    Retry(&'static str),
}

/// The applier owns validation, activation and retaining the previous generation.
/// A success result means activation completed, not merely that parsing succeeded.
pub async fn run<T, S, A, F>(
    source: &S,
    node_id: &str,
    cancel: CancellationToken,
    mut apply: A,
) -> Result<(), Error>
where
    S: Source<T>,
    A: FnMut(Arc<Snapshot<T>>) -> F,
    F: Future<Output = Result<(), ApplyError>>,
{
    let mut poll = Poll::new(node_id);
    let mut applied: Option<String> = None;
    let mut rejected: Option<(String, &'static str)> = None;
    let mut delay = Duration::from_millis(500);
    loop {
        let response = tokio::select! {
            biased;
            _ = cancel.cancelled() => return Ok(()),
            response = source.poll(&poll) => response,
        };
        let snapshot = match response {
            Ok(Some(snapshot)) => snapshot,
            Ok(None) => {
                delay = Duration::from_millis(500);
                continue;
            }
            Err(_) => {
                // A fresh full snapshot is mandatory after a broken exchange. Retain the
                // last successful fingerprint to avoid recreating an unchanged runtime.
                poll.received = None;
                if wait(&cancel, delay).await {
                    return Ok(());
                }
                delay = (delay * 2).min(Duration::from_secs(30));
                continue;
            }
        };
        poll.received = Some(snapshot.version.clone());
        let result = if applied.as_ref() == Some(&snapshot.fingerprint) {
            Ok(())
        } else if let Some((_, code)) = rejected
            .as_ref()
            .filter(|(hash, _)| hash == &snapshot.fingerprint)
        {
            Err(ApplyError::Rejected(code))
        } else {
            tokio::select! {
                biased;
                _ = cancel.cancelled() => return Ok(()),
                result = apply(snapshot.clone()) => result,
            }
        };
        match result {
            Ok(()) => {
                applied = Some(snapshot.fingerprint.clone());
                rejected = None;
                poll.result = Some(ApplicationResult::Applied {
                    version: snapshot.version.clone(),
                });
                delay = Duration::from_millis(500);
            }
            Err(ApplyError::Rejected(code)) => {
                rejected = Some((snapshot.fingerprint.clone(), code));
                poll.result = Some(ApplicationResult::Rejected {
                    version: snapshot.version.clone(),
                    code: code.into(),
                });
                delay = Duration::from_millis(500);
            }
            Err(ApplyError::Retry(code)) => {
                poll.result = Some(ApplicationResult::Rejected {
                    version: snapshot.version.clone(),
                    code: code.into(),
                });
                if wait(&cancel, delay).await {
                    return Ok(());
                }
                delay = (delay * 2).min(Duration::from_secs(30));
                // Fetch the latest desired state before retrying; do not retry a superseded candidate.
                poll.received = None;
            }
        }
    }
}

async fn wait(cancel: &CancellationToken, duration: Duration) -> bool {
    tokio::select! {
        biased;
        _ = cancel.cancelled() => true,
        _ = tokio::time::sleep(duration) => false,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};

    #[tokio::test(start_paused = true)]
    async fn retryable_activation_waits_then_acks_only_success() {
        let hub = Hub::new(serde_json::json!({})).unwrap();
        let stop = CancellationToken::new();
        let attempts = Arc::new(AtomicUsize::new(0));
        let count = attempts.clone();
        let source = hub.clone();
        let cancel = stop.clone();
        let task = tokio::spawn(async move {
            run(&source, "node", cancel, move |_| {
                let number = count.fetch_add(1, Ordering::SeqCst);
                async move {
                    if number == 0 {
                        Err(ApplyError::Retry("activation_failed"))
                    } else {
                        Ok(())
                    }
                }
            })
            .await
        });
        tokio::task::yield_now().await;
        assert_eq!(attempts.load(Ordering::SeqCst), 1);
        assert!(hub.node("node").unwrap().applied.is_none());
        tokio::time::advance(Duration::from_millis(499)).await;
        tokio::task::yield_now().await;
        assert_eq!(attempts.load(Ordering::SeqCst), 1);
        tokio::time::advance(Duration::from_millis(1)).await;
        tokio::task::yield_now().await;
        assert_eq!(attempts.load(Ordering::SeqCst), 2);
        assert_eq!(
            hub.node("node").unwrap().applied,
            Some(hub.current().version.clone())
        );
        stop.cancel();
        task.await.unwrap().unwrap();
    }

    struct Repeated {
        calls: AtomicUsize,
        snapshot: Arc<Snapshot<serde_json::Value>>,
    }
    #[async_trait::async_trait]
    impl Source<serde_json::Value> for Repeated {
        async fn poll(
            &self,
            poll: &Poll,
        ) -> Result<Option<Arc<Snapshot<serde_json::Value>>>, Error> {
            match self.calls.fetch_add(1, Ordering::SeqCst) {
                0 => Ok(Some(self.snapshot.clone())),
                1 => Err(Error::Unavailable),
                2 => {
                    assert!(poll.received.is_none());
                    let mut snapshot = (*self.snapshot).clone();
                    snapshot.version.epoch = "new-epoch".into();
                    Ok(Some(Arc::new(snapshot)))
                }
                _ => {
                    let Some(ApplicationResult::Rejected { version, code }) = &poll.result else {
                        panic!("rejection must be acknowledged");
                    };
                    assert_eq!(version.epoch, "new-epoch");
                    assert_eq!(code, "invalid_config");
                    std::future::pending().await
                }
            }
        }
    }
    #[tokio::test(start_paused = true)]
    async fn permanent_rejection_is_not_reapplied_after_reconnect_or_epoch_change() {
        let source = Arc::new(Repeated {
            calls: AtomicUsize::new(0),
            snapshot: Hub::new(serde_json::json!({})).unwrap().current(),
        });
        let stop = CancellationToken::new();
        let attempts = Arc::new(AtomicUsize::new(0));
        let count = attempts.clone();
        let reader = source.clone();
        let cancel = stop.clone();
        let task = tokio::spawn(async move {
            run(&*reader, "node", cancel, move |_| {
                count.fetch_add(1, Ordering::SeqCst);
                async { Err(ApplyError::Rejected("invalid_config")) }
            })
            .await
        });
        tokio::task::yield_now().await;
        tokio::time::advance(Duration::from_secs(1)).await;
        tokio::task::yield_now().await;
        assert_eq!(source.calls.load(Ordering::SeqCst), 4);
        assert_eq!(attempts.load(Ordering::SeqCst), 1);
        stop.cancel();
        task.await.unwrap().unwrap();
    }
}
