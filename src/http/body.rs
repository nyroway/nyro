use axum::body::{Body, Bytes};
use futures::task::AtomicWaker;
use http_body::{Body as HttpBody, Frame, SizeHint};
use nyro_kernel::Lease;
use std::{
    pin::Pin,
    sync::{Arc, Mutex},
    task::{Context, Poll},
};
use tokio::{task::JoinHandle, time::Instant};
use tokio_util::task::TaskTracker;

enum Terminal {
    Active,
    Failed,
    Complete,
}

struct State<T: Send + Sync + 'static> {
    body: Option<Body>,
    lease: Option<Lease<T>>,
    cancellation: tokio_util::sync::CancellationToken,
    terminal: Terminal,
}

fn finish<T: Send + Sync + 'static>(state: &Arc<Mutex<State<T>>>, terminal: Terminal) -> bool {
    let (body, lease) = {
        let mut state = state.lock().unwrap();
        if !matches!(state.terminal, Terminal::Active) {
            return false;
        }
        state.terminal = terminal;
        (state.body.take(), state.lease.take())
    };
    drop(body);
    drop(lease);
    true
}

struct Retained<T: Send + Sync + 'static> {
    state: Arc<Mutex<State<T>>>,
    cancellation: tokio_util::sync::CancellationToken,
    deadline: Instant,
    waker: Arc<AtomicWaker>,
    watchdog: JoinHandle<()>,
}

impl<T: Send + Sync + 'static> Retained<T> {
    fn stop(&self, terminal: Terminal) {
        self.watchdog.abort();
        if finish(&self.state, terminal) {
            self.waker.wake();
        }
    }
}

impl<T: Send + Sync + 'static> Drop for Retained<T> {
    fn drop(&mut self) {
        self.stop(Terminal::Complete);
    }
}

impl<T: Send + Sync + 'static> HttpBody for Retained<T> {
    type Data = Bytes;
    type Error = axum::Error;

    fn poll_frame(
        self: Pin<&mut Self>,
        context: &mut Context<'_>,
    ) -> Poll<Option<Result<Frame<Self::Data>, Self::Error>>> {
        let this = self.get_mut();
        this.waker.register(context.waker());
        if this.cancellation.is_cancelled() || Instant::now() >= this.deadline {
            this.stop(Terminal::Failed);
        }

        let frame = {
            let mut state = this.state.lock().unwrap();
            match state.terminal {
                Terminal::Active => {
                    Some(Pin::new(state.body.as_mut().unwrap()).poll_frame(context))
                }
                Terminal::Failed => {
                    state.terminal = Terminal::Complete;
                    None
                }
                Terminal::Complete => return Poll::Ready(None),
            }
        };

        match frame {
            None => Poll::Ready(Some(Err(axum::Error::new(std::io::Error::other(
                "response cancelled or deadline exceeded",
            ))))),
            Some(Poll::Pending) => Poll::Pending,
            Some(Poll::Ready(Some(Ok(frame)))) => Poll::Ready(Some(Ok(frame))),
            Some(Poll::Ready(Some(Err(error)))) => {
                this.stop(Terminal::Complete);
                Poll::Ready(Some(Err(error)))
            }
            Some(Poll::Ready(None)) => {
                this.stop(Terminal::Complete);
                Poll::Ready(None)
            }
        }
    }

    fn is_end_stream(&self) -> bool {
        matches!(self.state.lock().unwrap().terminal, Terminal::Complete)
    }

    fn size_hint(&self) -> SizeHint {
        self.state
            .lock()
            .unwrap()
            .body
            .as_ref()
            .map_or_else(SizeHint::default, HttpBody::size_hint)
    }
}

pub(super) fn retain<T: Send + Sync + 'static>(
    body: Body,
    lease: Lease<T>,
    deadline: Instant,
    tracker: &TaskTracker,
) -> Body {
    let cancellation = lease.cancellation();
    let state = Arc::new(Mutex::new(State {
        body: Some(body),
        lease: Some(lease),
        cancellation: cancellation.clone(),
        terminal: Terminal::Active,
    }));
    let waker = Arc::new(AtomicWaker::new());
    let watchdog_state = Arc::clone(&state);
    let watchdog_waker = Arc::clone(&waker);
    let watchdog = tracker.spawn(async move {
        let cancelled = watchdog_state.lock().unwrap().cancellation.clone();
        tokio::select! {
            biased;
            _ = cancelled.cancelled() => {},
            _ = tokio::time::sleep_until(deadline) => {},
        }
        if finish(&watchdog_state, Terminal::Failed) {
            watchdog_waker.wake();
        }
    });

    Body::new(Retained {
        state,
        cancellation,
        deadline,
        waker,
        watchdog,
    })
}

#[cfg(test)]
mod tests {
    use super::retain;
    use async_trait::async_trait;
    use axum::body::{Body, Bytes, to_bytes};
    use http_body::Frame;
    use nyro_kernel::{Candidate, Component, Context, Host, HostOptions, Lease, Lifecycle};
    use std::{
        convert::Infallible,
        pin::Pin,
        sync::{Arc, Mutex},
        task::{Context as TaskContext, Poll},
        time::Duration,
    };
    use tokio::time::{Instant, timeout};
    use tokio_util::{sync::CancellationToken, task::TaskTracker};

    type Events = Arc<Mutex<Vec<&'static str>>>;

    struct Resource(Events);

    #[async_trait]
    impl Lifecycle for Resource {
        async fn start(&mut self, _: &Context) -> Result<(), String> {
            Ok(())
        }
        fn begin_stop(&mut self) {
            self.0.lock().unwrap().push("component-stop");
        }
        async fn wait_stopped(&mut self, _: &Context) -> Result<(), String> {
            Ok(())
        }
    }

    struct Source {
        events: Events,
    }

    impl Drop for Source {
        fn drop(&mut self) {
            self.events.lock().unwrap().push("source-drop");
        }
    }

    impl http_body::Body for Source {
        type Data = Bytes;
        type Error = Infallible;

        fn poll_frame(
            self: Pin<&mut Self>,
            _: &mut TaskContext<'_>,
        ) -> Poll<Option<Result<Frame<Bytes>, Infallible>>> {
            Poll::Pending
        }
    }

    struct EofSource {
        events: Events,
        sent: bool,
    }

    impl Drop for EofSource {
        fn drop(&mut self) {
            self.events.lock().unwrap().push("source-drop");
        }
    }

    impl http_body::Body for EofSource {
        type Data = Bytes;
        type Error = Infallible;

        fn poll_frame(
            mut self: Pin<&mut Self>,
            _: &mut TaskContext<'_>,
        ) -> Poll<Option<Result<Frame<Bytes>, Infallible>>> {
            if self.sent {
                Poll::Ready(None)
            } else {
                self.sent = true;
                Poll::Ready(Some(Ok(Frame::data(Bytes::from_static(b"ok")))))
            }
        }
    }

    async fn host_with_retiring_lease(events: Events) -> (Arc<Host<()>>, Lease<()>) {
        host_with_retiring_lease_with_options(events, HostOptions::default()).await
    }

    async fn host_with_retiring_lease_with_options(
        events: Events,
        options: HostOptions,
    ) -> (Arc<Host<()>>, Lease<()>) {
        let host = Arc::new(Host::new(options));
        let context = Context {
            deadline: Instant::now() + Duration::from_secs(1),
            cancellation: CancellationToken::new(),
        };
        host.activate(
            Candidate {
                version: "old".into(),
                fingerprint: None,
                value: (),
                components: vec![Component {
                    id: "resource".into(),
                    after: vec![],
                    lifecycle: Box::new(Resource(events)),
                }],
            },
            context,
        )
        .await
        .unwrap();
        let lease = host.acquire().unwrap();
        host.activate(
            Candidate {
                version: "new".into(),
                fingerprint: None,
                value: (),
                components: vec![],
            },
            Context {
                deadline: Instant::now() + Duration::from_secs(1),
                cancellation: CancellationToken::new(),
            },
        )
        .await
        .unwrap();
        (host, lease)
    }

    async fn until(mut predicate: impl FnMut() -> bool) {
        timeout(Duration::from_secs(1), async {
            while !predicate() {
                tokio::task::yield_now().await;
            }
        })
        .await
        .unwrap();
    }

    #[tokio::test]
    async fn retains_source_and_lease_until_response_drop() {
        let events = Events::default();
        let (host, lease) = host_with_retiring_lease(Arc::clone(&events)).await;
        let tracker = TaskTracker::new();
        let body = retain(
            Body::new(Source {
                events: Arc::clone(&events),
            }),
            lease,
            Instant::now() + Duration::from_secs(1),
            &tracker,
        );

        assert_eq!(host.status().retiring[0].leases, 1);
        assert!(events.lock().unwrap().is_empty());
        drop(body);
        until(|| host.status().retiring.is_empty()).await;
        assert_eq!(*events.lock().unwrap(), ["source-drop", "component-stop"]);
        tracker.close();
        tracker.wait().await;
    }

    #[tokio::test]
    async fn eof_drops_the_source_before_retiring_the_component() {
        let events = Events::default();
        let (host, lease) = host_with_retiring_lease(Arc::clone(&events)).await;
        let tracker = TaskTracker::new();
        let body = retain(
            Body::new(EofSource {
                events: Arc::clone(&events),
                sent: false,
            }),
            lease,
            Instant::now() + Duration::from_secs(1),
            &tracker,
        );

        assert_eq!(to_bytes(body, 1024).await.unwrap(), "ok");
        until(|| host.status().retiring.is_empty()).await;
        assert_eq!(*events.lock().unwrap(), ["source-drop", "component-stop"]);
        tracker.close();
        tracker.wait().await;
    }

    #[tokio::test]
    async fn deadline_releases_an_unpolled_response_and_then_yields_one_error() {
        let events = Events::default();
        let (host, lease) = host_with_retiring_lease(Arc::clone(&events)).await;
        let tracker = TaskTracker::new();
        let mut body = retain(
            Body::new(Source {
                events: Arc::clone(&events),
            }),
            lease,
            Instant::now(),
            &tracker,
        );

        until(|| host.status().retiring.is_empty()).await;
        assert_eq!(*events.lock().unwrap(), ["source-drop", "component-stop"]);
        assert!(
            futures::future::poll_fn(|cx| http_body::Body::poll_frame(Pin::new(&mut body), cx))
                .await
                .unwrap()
                .is_err()
        );
        assert!(
            futures::future::poll_fn(|cx| http_body::Body::poll_frame(Pin::new(&mut body), cx))
                .await
                .is_none()
        );
        tracker.close();
        tracker.wait().await;
    }

    #[tokio::test]
    async fn host_shutdown_cancellation_releases_an_unpolled_response() {
        let events = Events::default();
        let (host, lease) = host_with_retiring_lease_with_options(
            Arc::clone(&events),
            HostOptions {
                shutdown_grace: Duration::ZERO,
                cleanup_timeout: Duration::from_secs(1),
            },
        )
        .await;
        let tracker = TaskTracker::new();
        let body = retain(
            Body::new(Source {
                events: Arc::clone(&events),
            }),
            lease,
            Instant::now() + Duration::from_secs(60),
            &tracker,
        );

        let _ = host.shutdown().await;
        until(|| events.lock().unwrap().contains(&"source-drop")).await;
        drop(body);
        tracker.close();
        tracker.wait().await;
    }
}
