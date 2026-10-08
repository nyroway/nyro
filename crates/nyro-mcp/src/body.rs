//! Bounded upstream byte streams and request-owned response cleanup.
use axum::body::{Body, Bytes};
use futures::{StreamExt, stream::BoxStream};
use std::{
    io,
    sync::{
        Arc,
        atomic::{AtomicUsize, Ordering},
    },
};

// Count raw bytes before the JSON collector or SSE parser can allocate for them.
// Count comments as well as data, and tolerate LF, CRLF and CR SSE line endings.
pub(crate) fn bounded(
    response: reqwest::Response,
    remaining: Arc<AtomicUsize>,
    frame_limit: Option<usize>,
) -> BoxStream<'static, Result<Bytes, io::Error>> {
    let mut frame = 0usize;
    let mut line = 0usize;
    let mut cr = false;
    response
        .bytes_stream()
        .map(move |chunk| {
            let chunk = chunk.map_err(|_| io::Error::other("MCP upstream read failed"))?;
            remaining
                .fetch_update(Ordering::Relaxed, Ordering::Relaxed, |n| {
                    n.checked_sub(chunk.len())
                })
                .map_err(|_| io::Error::other("MCP response limit exceeded"))?;
            if let Some(limit) = frame_limit {
                for &byte in &chunk {
                    if cr {
                        cr = false;
                        if byte == b'\n' {
                            continue;
                        }
                    }
                    frame = frame.saturating_add(1);
                    if frame > limit {
                        return Err(io::Error::other("MCP frame limit exceeded"));
                    }
                    if byte == b'\r' || byte == b'\n' {
                        cr = byte == b'\r';
                        if line == 0 {
                            frame = 0;
                        }
                        line = 0;
                    } else {
                        line += 1;
                    }
                }
            }
            Ok(chunk)
        })
        .boxed()
}

use futures::task::AtomicWaker;
use http_body::{Body as HttpBody, Frame};
use nyro_limit::Permit;
use std::{
    pin::Pin,
    sync::Mutex,
    task::{Context, Poll},
};
use tokio::{task::JoinHandle, time::Instant};
use tokio_util::sync::CancellationToken;

struct State {
    body: Option<Body>,
    permit: Option<Permit>,
    failed: bool,
}
struct Retained {
    state: Arc<Mutex<State>>,
    cancel: CancellationToken,
    waker: Arc<AtomicWaker>,
    watchdog: JoinHandle<()>,
}
impl Drop for Retained {
    fn drop(&mut self) {
        self.cancel.cancel();
        self.watchdog.abort();
        let mut state = self.state.lock().unwrap();
        state.body.take();
        state.permit.take();
    }
}
impl HttpBody for Retained {
    type Data = Bytes;
    type Error = axum::Error;
    fn poll_frame(
        self: Pin<&mut Self>,
        cx: &mut Context<'_>,
    ) -> Poll<Option<Result<Frame<Bytes>, Self::Error>>> {
        self.waker.register(cx.waker());
        let mut state = self.state.lock().unwrap();
        if state.failed {
            state.failed = false;
            return Poll::Ready(Some(Err(axum::Error::new(io::Error::other(
                "MCP response cancelled or deadline exceeded",
            )))));
        }
        let Some(body) = state.body.as_mut() else {
            return Poll::Ready(None);
        };
        let poll = Pin::new(body).poll_frame(cx);
        if matches!(poll, Poll::Ready(None) | Poll::Ready(Some(Err(_)))) {
            state.body.take();
            state.permit.take();
            self.cancel.cancel();
            self.watchdog.abort();
        }
        poll
    }
}
pub(crate) fn retain(
    body: Body,
    permit: Option<Permit>,
    cancel: CancellationToken,
    deadline: Instant,
) -> Body {
    let state = Arc::new(Mutex::new(State {
        body: Some(body),
        permit,
        failed: false,
    }));
    let waker = Arc::new(AtomicWaker::new());
    let task_state = state.clone();
    let task_waker = waker.clone();
    let task_cancel = cancel.clone();
    let watchdog = tokio::spawn(async move {
        tokio::select! { _=task_cancel.cancelled()=>{}, _=tokio::time::sleep_until(deadline)=>{} }
        task_cancel.cancel();
        {
            let mut state = task_state.lock().unwrap();
            state.body.take();
            state.permit.take();
            state.failed = true;
        }
        task_waker.wake();
    });
    Body::new(Retained {
        state,
        cancel,
        waker,
        watchdog,
    })
}
