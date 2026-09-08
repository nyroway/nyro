use axum::body::{Body, BodyDataStream, Bytes};
use futures::StreamExt;
use tokio::time::Instant;
use tokio_util::sync::CancellationToken;

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub(crate) enum Outcome {
    Complete,
    Cancelled,
    Timeout,
    Error,
}

struct Managed {
    stream: Option<BodyDataStream>,
    cancellation: CancellationToken,
    deadline: Instant,
    finalize: Option<Box<dyn FnOnce(Outcome) + Send>>,
    done: bool,
}

impl Managed {
    fn finish(&mut self, outcome: Outcome) {
        if self.done {
            return;
        }
        self.done = true;
        drop(self.stream.take());
        if let Some(finalize) = self.finalize.take() {
            finalize(outcome);
        }
    }
}

impl Drop for Managed {
    fn drop(&mut self) {
        let outcome = if self.cancellation.is_cancelled() {
            Outcome::Cancelled
        } else if Instant::now() >= self.deadline {
            Outcome::Timeout
        } else {
            Outcome::Cancelled
        };
        self.finish(outcome);
    }
}

/// Enforces cancellation and deadline while the response is polled; callers own any watchdog
/// needed when downstream backpressure stops polling.
pub(crate) fn managed(
    body: Body,
    cancellation: CancellationToken,
    deadline: Instant,
    finalize: impl FnOnce(Outcome) + Send + 'static,
) -> Body {
    Body::from_stream(futures::stream::unfold(
        Managed {
            stream: Some(body.into_data_stream()),
            cancellation,
            deadline,
            finalize: Some(Box::new(finalize)),
            done: false,
        },
        |mut state| async move {
            if state.done {
                return None;
            }

            tokio::select! {
                biased;
                _ = state.cancellation.cancelled() => {
                    state.finish(Outcome::Cancelled);
                    Some((Err(axum::Error::new(std::io::Error::other("response cancelled"))), state))
                }
                _ = tokio::time::sleep_until(state.deadline) => {
                    state.finish(Outcome::Timeout);
                    Some((Err(axum::Error::new(std::io::Error::other("response deadline exceeded"))), state))
                }
                frame = state.stream.as_mut().expect("active stream").next() => match frame {
                    Some(Ok(data)) => Some((Ok::<Bytes, axum::Error>(data), state)),
                    Some(Err(error)) => {
                        state.finish(Outcome::Error);
                        Some((Err(error), state))
                    }
                    None => {
                        state.finish(Outcome::Complete);
                        None
                    }
                },
            }
        },
    ))
}

#[cfg(test)]
mod tests {
    use super::{Outcome, managed};
    use axum::body::{Body, Bytes, to_bytes};
    use futures::stream;
    use std::{
        io,
        sync::{
            Arc, Mutex,
            atomic::{AtomicBool, Ordering},
        },
    };
    use tokio::time::{Duration, Instant};
    use tokio_util::sync::CancellationToken;

    fn outcomes() -> (
        Arc<Mutex<Vec<Outcome>>>,
        impl FnOnce(Outcome) + Send + 'static,
    ) {
        let outcomes = Arc::new(Mutex::new(Vec::new()));
        let recorded = Arc::clone(&outcomes);
        (outcomes, move |outcome| {
            recorded.lock().unwrap().push(outcome)
        })
    }

    #[tokio::test]
    async fn completes_and_finalizes_once_at_eof() {
        let (outcomes, finalize) = outcomes();
        let body = managed(
            Body::from("ok"),
            CancellationToken::new(),
            Instant::now() + Duration::from_secs(1),
            finalize,
        );

        assert_eq!(to_bytes(body, 1024).await.unwrap(), "ok");
        assert_eq!(*outcomes.lock().unwrap(), vec![Outcome::Complete]);
    }

    #[tokio::test]
    async fn dropping_an_unconsumed_body_cancels_once() {
        let (outcomes, finalize) = outcomes();
        let body = managed(
            Body::from("unread"),
            CancellationToken::new(),
            Instant::now() + Duration::from_secs(1),
            finalize,
        );

        drop(body);
        assert_eq!(*outcomes.lock().unwrap(), vec![Outcome::Cancelled]);
    }

    #[tokio::test]
    async fn cancellation_ends_a_pending_body_with_an_error() {
        let (outcomes, finalize) = outcomes();
        let cancellation = CancellationToken::new();
        cancellation.cancel();
        let body = managed(
            Body::from_stream(stream::pending::<Result<Bytes, io::Error>>()),
            cancellation,
            Instant::now() + Duration::from_secs(1),
            finalize,
        );

        assert!(to_bytes(body, 1024).await.is_err());
        assert_eq!(*outcomes.lock().unwrap(), vec![Outcome::Cancelled]);
    }

    #[tokio::test]
    async fn expired_deadline_ends_a_pending_body_with_an_error() {
        let (outcomes, finalize) = outcomes();
        let body = managed(
            Body::from_stream(stream::pending::<Result<Bytes, io::Error>>()),
            CancellationToken::new(),
            Instant::now() - Duration::from_millis(1),
            finalize,
        );

        assert!(to_bytes(body, 1024).await.is_err());
        assert_eq!(*outcomes.lock().unwrap(), vec![Outcome::Timeout]);
    }

    #[tokio::test]
    async fn dropping_an_expired_unpolled_body_times_out() {
        let (outcomes, finalize) = outcomes();
        let body = managed(
            Body::from_stream(stream::pending::<Result<Bytes, io::Error>>()),
            CancellationToken::new(),
            Instant::now() - Duration::from_millis(1),
            finalize,
        );

        drop(body);
        assert_eq!(*outcomes.lock().unwrap(), vec![Outcome::Timeout]);
    }

    #[tokio::test]
    async fn upstream_error_finalizes_once() {
        let (outcomes, finalize) = outcomes();
        let source = stream::once(async { Err::<Bytes, _>(io::Error::other("upstream")) });
        let body = managed(
            Body::from_stream(source),
            CancellationToken::new(),
            Instant::now() + Duration::from_secs(1),
            finalize,
        );

        assert!(to_bytes(body, 1024).await.is_err());
        assert_eq!(*outcomes.lock().unwrap(), vec![Outcome::Error]);
    }

    #[tokio::test]
    async fn drops_the_source_before_finalizing() {
        struct DropSignal(Arc<AtomicBool>);
        impl Drop for DropSignal {
            fn drop(&mut self) {
                self.0.store(true, Ordering::SeqCst);
            }
        }

        let dropped = Arc::new(AtomicBool::new(false));
        let source = stream::unfold(DropSignal(Arc::clone(&dropped)), |_signal| async move {
            std::future::pending::<Option<(Result<Bytes, io::Error>, DropSignal)>>().await
        });
        let body = managed(
            Body::from_stream(source),
            CancellationToken::new(),
            Instant::now() + Duration::from_secs(1),
            move |_| assert!(dropped.load(Ordering::SeqCst)),
        );

        drop(body);
    }
}
