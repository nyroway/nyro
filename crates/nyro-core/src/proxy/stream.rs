//! StreamBridge: fault-safe state machine for streaming upstream responses.
//!
//! # Guarantees
//!
//! 1. **Explicit terminal state** — every stream ends with one of
//!    `Completed` / `Failed(_)`.  The `Drop` impl ensures this even for
//!    unexpected panics or early returns.
//!
//! 2. **No swallowed errors** — `read_error` → `Failed(Upstream)`,
//!    `parser_error` → `Failed(Parse)`, client disconnect → `Failed(ClientCancelled)`.
//!
//! 3. **Outcome propagated to `RequestContext`** — callers upstream (quota
//!    settle, target health, log) base decisions on `ctx.get_outcome()`.
//!
//! 4. **Singleflight / cache reservation released on drop** — registered
//!    callbacks (see `add_cleanup`) are always called.
//!
//! # Usage
//!
//! ```rust,ignore
//! let mut bridge = StreamBridge::new(ctx);
//! bridge.on_connected();
//! while let Some(chunk) = byte_stream.next().await {
//!     match bridge.push_chunk(chunk) {
//!         Ok(deltas) => { /* forward to client */ }
//!         Err(_) => break,
//!     }
//! }
//! bridge.finish();   // sets Completed; Drop handles the Failed case
//! ```

use crate::protocol::ir::Usage;
use crate::proxy::context::{CancellationToken, RequestContext, RequestOutcome};

// ── Failure variants ──────────────────────────────────────────────────────────

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum StreamFailure {
    /// The upstream connection or byte-stream failed.
    Upstream { msg: String },
    /// An SSE chunk could not be parsed.
    Parse { raw_chunk: Option<String> },
    /// The client disconnected before we finished.
    ClientCancelled,
    /// Hard deadline exceeded during streaming.
    Timeout,
    /// Unexpected internal failure.
    Internal { msg: String },
}

impl StreamFailure {
    pub fn upstream(msg: impl Into<String>) -> Self {
        Self::Upstream { msg: msg.into() }
    }

    pub fn parse(raw_chunk: Option<String>) -> Self {
        Self::Parse { raw_chunk }
    }

    pub fn to_outcome_code(&self) -> &'static str {
        match self {
            Self::Upstream { .. } => "NYRO_UPSTREAM_ERROR",
            Self::Parse { .. } => "NYRO_STREAM_PARSE_ERROR",
            Self::ClientCancelled => "NYRO_CLIENT_CANCELLED",
            Self::Timeout => "NYRO_UPSTREAM_TIMEOUT",
            Self::Internal { .. } => "NYRO_INTERNAL_ERROR",
        }
    }
}

// ── State ─────────────────────────────────────────────────────────────────────

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum StreamState {
    /// Bridge created, waiting for upstream connection.
    Init,
    /// HTTP connection established; waiting for first byte.
    UpstreamConnected,
    /// First byte received; chunks flowing.
    Streaming,
    /// All upstream chunks consumed; writing final SSE event.
    Finishing,
    /// Stream delivered completely to the client.
    Completed,
    /// Stream ended in an unrecoverable error.
    Failed(StreamFailure),
}

impl StreamState {
    /// Returns `true` if the state is terminal (no further transitions).
    pub fn is_terminal(&self) -> bool {
        matches!(self, Self::Completed | Self::Failed(_))
    }
}

// ── Cleanup callback ──────────────────────────────────────────────────────────

/// A callback that MUST run when the bridge is dropped.
///
/// Used to release resource reservations, cleanup state, etc.
pub type CleanupFn = Box<dyn FnOnce() + Send + 'static>;

// ── StreamBridge ──────────────────────────────────────────────────────────────

/// Fault-safe state machine for streaming upstream responses.
pub struct StreamBridge<'a> {
    ctx: &'a RequestContext,
    state: StreamState,
    chunks_sent: usize,
    final_usage: Option<Usage>,
    cancellation: CancellationToken,
    cleanups: Vec<CleanupFn>,
}

impl<'a> StreamBridge<'a> {
    /// Create a bridge bound to `ctx`.
    pub fn new(ctx: &'a RequestContext) -> Self {
        Self {
            ctx,
            state: StreamState::Init,
            chunks_sent: 0,
            final_usage: None,
            cancellation: ctx.cancellation.clone(),
            cleanups: Vec::new(),
        }
    }

    /// Register a cleanup callback.  All callbacks run on `Drop`.
    pub fn add_cleanup(&mut self, f: impl FnOnce() + Send + 'static) {
        self.cleanups.push(Box::new(f));
    }

    /// Current state.
    pub fn state(&self) -> &StreamState {
        &self.state
    }

    /// True if the client has cancelled (disconnected).
    pub fn is_cancelled(&self) -> bool {
        self.cancellation.is_cancelled()
    }

    // ── State transitions ─────────────────────────────────────────────────────

    /// Called when the upstream HTTP connection is established.
    pub fn on_connected(&mut self) {
        if self.state == StreamState::Init {
            self.state = StreamState::UpstreamConnected;
            self.ctx.trace("stream", "upstream connected");
        }
    }

    /// Called when a parsed chunk should be forwarded to the client.
    ///
    /// Returns `Err(failure)` and transitions to `Failed` if an error is
    /// detected; `Ok(chunks_sent)` otherwise.
    pub fn push_chunk(
        &mut self,
        parse_result: Result<(), StreamFailure>,
    ) -> Result<usize, &StreamFailure> {
        if self.cancellation.is_cancelled() {
            self.transition_to_failed(StreamFailure::ClientCancelled);
            return Err(self.failure().unwrap());
        }

        if self.ctx.deadline.is_exceeded() {
            self.transition_to_failed(StreamFailure::Timeout);
            return Err(self.failure().unwrap());
        }

        match parse_result {
            Ok(()) => {
                if self.state == StreamState::UpstreamConnected {
                    self.state = StreamState::Streaming;
                }
                self.chunks_sent += 1;
                Ok(self.chunks_sent)
            }
            Err(f) => {
                self.transition_to_failed(f);
                Err(self.failure().unwrap())
            }
        }
    }

    /// Record an upstream read error.
    pub fn on_read_error(&mut self, msg: impl Into<String>) {
        self.transition_to_failed(StreamFailure::upstream(msg));
    }

    /// Called after all chunks have been processed.  Transitions to `Finishing`
    /// and then `Completed`.
    pub fn finish(&mut self) {
        if !self.state.is_terminal() {
            self.state = StreamState::Finishing;
            self.ctx.trace(
                "stream",
                format!("finishing; {} chunks sent", self.chunks_sent),
            );
            self.complete();
        }
    }

    /// Record the final token usage (from the provider's `usage` event or
    /// the accumulator).
    pub fn set_final_usage(&mut self, usage: Usage) {
        self.final_usage = Some(usage);
    }

    pub fn final_usage(&self) -> Option<&Usage> {
        self.final_usage.as_ref()
    }

    pub fn chunks_sent(&self) -> usize {
        self.chunks_sent
    }

    // ── Private helpers ───────────────────────────────────────────────────────

    fn complete(&mut self) {
        self.state = StreamState::Completed;
        self.ctx.set_outcome(RequestOutcome::Success);
        self.ctx.trace("stream", "completed");
    }

    fn transition_to_failed(&mut self, failure: StreamFailure) {
        if self.state.is_terminal() {
            return;
        }
        self.ctx
            .trace("stream", format!("failed: {:?}", failure.to_outcome_code()));
        self.ctx.set_outcome(if self.chunks_sent > 0 {
            RequestOutcome::PartialSuccess {
                chunks_sent: self.chunks_sent,
            }
        } else {
            RequestOutcome::Failed {
                stable_code: failure.to_outcome_code(),
            }
        });
        self.state = StreamState::Failed(failure);
    }

    fn failure(&self) -> Option<&StreamFailure> {
        if let StreamState::Failed(f) = &self.state {
            Some(f)
        } else {
            None
        }
    }
}

impl Drop for StreamBridge<'_> {
    fn drop(&mut self) {
        // Run all registered cleanup callbacks unconditionally.
        for f in self.cleanups.drain(..) {
            f();
        }

        // If the outcome was never written (e.g. an early return / panic),
        // record ClientCancelled so that quota/health accounting is never based
        // on an implicit success.
        if self.ctx.get_outcome().is_none() {
            self.ctx.set_outcome(if self.chunks_sent > 0 {
                RequestOutcome::PartialSuccess {
                    chunks_sent: self.chunks_sent,
                }
            } else {
                RequestOutcome::ClientCancelled
            });
        }

        if !self.state.is_terminal() {
            self.state = StreamState::Failed(StreamFailure::ClientCancelled);
        }
    }
}

// ── Fault injection tests ─────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::ids::OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1;
    use crate::proxy::context::RequestContext;
    use std::sync::Arc;
    use std::time::Duration;

    fn ctx() -> RequestContext {
        RequestContext::new(
            OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
            Duration::from_secs(30),
        )
    }

    // ── Fault 1: read interrupt ───────────────────────────────────────────────

    #[test]
    fn fault_read_interrupt_sets_partial_success() {
        let c = ctx();
        let mut b = StreamBridge::new(&c);
        b.on_connected();
        b.push_chunk(Ok(())).unwrap(); // first chunk OK
        b.push_chunk(Ok(())).unwrap();
        b.on_read_error("connection reset"); // simulated read break
        assert!(matches!(
            b.state(),
            StreamState::Failed(StreamFailure::Upstream { .. })
        ));
        let outcome = c.get_outcome().unwrap().clone();
        assert!(matches!(
            outcome,
            RequestOutcome::PartialSuccess { chunks_sent: 2 }
        ));
    }

    // ── Fault 2: parser error ─────────────────────────────────────────────────

    #[test]
    fn fault_parser_error_sets_failed() {
        let c = ctx();
        let mut b = StreamBridge::new(&c);
        b.on_connected();
        let err = b.push_chunk(Err(StreamFailure::parse(Some("bad chunk".into()))));
        assert!(err.is_err());
        assert!(matches!(
            b.state(),
            StreamState::Failed(StreamFailure::Parse { .. })
        ));
        assert!(matches!(
            c.get_outcome().unwrap(),
            RequestOutcome::Failed { .. }
        ));
    }

    // ── Fault 3: client cancel ────────────────────────────────────────────────

    #[test]
    fn fault_client_cancel_mid_stream() {
        let c = ctx();
        let mut b = StreamBridge::new(&c);
        b.on_connected();
        b.push_chunk(Ok(())).unwrap();
        // Simulate client disconnect.
        b.cancellation.cancel();
        let result = b.push_chunk(Ok(()));
        assert!(result.is_err());
        assert!(matches!(
            b.state(),
            StreamState::Failed(StreamFailure::ClientCancelled)
        ));
    }

    // ── Fault 4: timeout ──────────────────────────────────────────────────────

    #[test]
    fn fault_timeout_detected_in_push_chunk() {
        let c = RequestContext::new(
            OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
            Duration::from_nanos(1),
        );
        std::thread::sleep(std::time::Duration::from_millis(1));
        let mut b = StreamBridge::new(&c);
        b.on_connected();
        let result = b.push_chunk(Ok(()));
        assert!(result.is_err());
        assert!(matches!(
            b.state(),
            StreamState::Failed(StreamFailure::Timeout)
        ));
    }

    // ── Fault 5: 5xx from upstream after SSE started ──────────────────────────

    #[test]
    fn fault_upstream_5xx_after_sse_started() {
        let c = ctx();
        let mut b = StreamBridge::new(&c);
        b.on_connected();
        b.push_chunk(Ok(())).unwrap();
        b.on_read_error("upstream 503");
        assert!(matches!(
            b.state(),
            StreamState::Failed(StreamFailure::Upstream { .. })
        ));
        assert!(matches!(
            c.get_outcome().unwrap(),
            RequestOutcome::PartialSuccess { .. }
        ));
    }

    // ── Fault 6: stream ends without DONE event ───────────────────────────────

    #[test]
    fn fault_no_done_event_drop_fallback() {
        let c = ctx();
        {
            let mut b = StreamBridge::new(&c);
            b.on_connected();
            b.push_chunk(Ok(())).unwrap();
            b.push_chunk(Ok(())).unwrap();
            // No call to b.finish() — simulates stream ending without [DONE].
            // Drop fires the fallback.
        }
        // Drop should have recorded PartialSuccess.
        assert!(c.get_outcome().is_some());
        assert!(matches!(
            c.get_outcome().unwrap(),
            RequestOutcome::PartialSuccess { chunks_sent: 2 }
        ));
    }

    // ── Normal happy path ─────────────────────────────────────────────────────

    #[test]
    fn happy_path_completes() {
        let c = ctx();
        let mut b = StreamBridge::new(&c);
        b.on_connected();
        for _ in 0..5 {
            b.push_chunk(Ok(())).unwrap();
        }
        b.finish();
        assert_eq!(b.state(), &StreamState::Completed);
        assert_eq!(c.get_outcome().unwrap(), &RequestOutcome::Success);
    }

    // ── Cleanup callback ──────────────────────────────────────────────────────

    #[test]
    fn cleanup_runs_on_drop() {
        use std::sync::atomic::{AtomicBool, Ordering};
        let ran = Arc::new(AtomicBool::new(false));
        let ran_clone = ran.clone();
        let c = ctx();
        {
            let mut b = StreamBridge::new(&c);
            b.add_cleanup(move || {
                ran_clone.store(true, Ordering::SeqCst);
            });
        }
        assert!(ran.load(Ordering::SeqCst));
    }

    // ── Second-write idempotency ──────────────────────────────────────────────

    #[test]
    fn outcome_is_written_once() {
        let c = ctx();
        let mut b = StreamBridge::new(&c);
        b.on_connected();
        b.finish();
        // Second finish is a no-op.
        b.finish();
        assert_eq!(c.get_outcome().unwrap(), &RequestOutcome::Success);
    }
}
