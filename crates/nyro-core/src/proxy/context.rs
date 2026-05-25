//! Per-request lifecycle context.
//!
//! `RequestContext` is created once per inbound request (in an axum middleware)
//! and propagated through all layers via `axum::Extension`.  It carries:
//!
//! - **Identity** – a stable UUID `request_id` for log correlation.
//! - **Deadline** – an `Instant` after which any new I/O should abort.
//! - **Cancellation** – a shared flag; set when the client disconnects or the
//!   deadline fires.
//! - **Outcome** – a write-once cell recording the final `RequestOutcome`;
//!   `StreamBridge`'s `Drop` impl uses this to detect partial-success.
//! - **Trace** – an append-only in-memory event log for debugging; replaced
//!   by OpenTelemetry spans in P2-F.
//! - **Protocol / routing fields** – filled in progressively as the request
//!   moves through `intake → security → planner → dispatcher`.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, OnceLock};
use std::time::{Duration, Instant};

use uuid::Uuid;

use crate::protocol::ids::ProtocolId;

// ── Deadline ──────────────────────────────────────────────────────────────────

/// A hard deadline for a request.  Anything that starts new I/O SHOULD check
/// `deadline.is_exceeded()` before proceeding.
#[derive(Clone, Debug)]
pub struct Deadline {
    at: Instant,
}

impl Deadline {
    /// Create a deadline `ttl` from now.
    pub fn from_now(ttl: Duration) -> Self {
        Self {
            at: Instant::now() + ttl,
        }
    }

    /// A deadline that never fires (useful for unit tests / health probes).
    pub fn never() -> Self {
        Self {
            at: Instant::now() + Duration::from_secs(86400 * 365 * 100),
        }
    }

    /// Returns `true` if the deadline has already passed.
    pub fn is_exceeded(&self) -> bool {
        Instant::now() > self.at
    }

    /// How much time remains.  Returns zero if already exceeded.
    pub fn remaining(&self) -> Duration {
        self.at.saturating_duration_since(Instant::now())
    }

    /// The absolute `Instant` the deadline fires.
    pub fn at(&self) -> Instant {
        self.at
    }
}

// ── Cancellation ─────────────────────────────────────────────────────────────

/// A shareable cancellation signal.  Any holder can cancel; all holders see it.
#[derive(Clone, Debug)]
pub struct CancellationToken(Arc<AtomicBool>);

impl CancellationToken {
    pub fn new() -> Self {
        Self(Arc::new(AtomicBool::new(false)))
    }

    /// Signal cancellation.
    pub fn cancel(&self) {
        self.0.store(true, Ordering::Release);
    }

    /// Returns `true` if cancellation has been signalled.
    pub fn is_cancelled(&self) -> bool {
        self.0.load(Ordering::Acquire)
    }
}

impl Default for CancellationToken {
    fn default() -> Self {
        Self::new()
    }
}

// ── Outcome ───────────────────────────────────────────────────────────────────

/// The final disposition of a request.  Written exactly once; readers wait via
/// `OnceLock::get`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum RequestOutcome {
    /// The response was fully delivered to the client.
    Success,
    /// The response was started but not completed (stream partial).
    PartialSuccess { chunks_sent: usize },
    /// A `GatewayError` caused the request to fail.
    Failed { stable_code: &'static str },
    /// The client disconnected before we finished.
    ClientCancelled,
    /// The upstream / deadline timed out.
    Timeout,
}

impl RequestOutcome {
    /// Returns `true` for the two success variants.
    pub fn is_success(&self) -> bool {
        matches!(
            self,
            RequestOutcome::Success | RequestOutcome::PartialSuccess { .. }
        )
    }

    /// Best-effort HTTP status code for quota/health accounting.
    pub fn status_code(&self) -> u16 {
        match self {
            RequestOutcome::Success => 200,
            RequestOutcome::PartialSuccess { .. } => 200,
            RequestOutcome::Failed { .. } => 500,
            RequestOutcome::ClientCancelled => 499,
            RequestOutcome::Timeout => 504,
        }
    }
}

// ── Auth subject ─────────────────────────────────────────────────────────────

/// Identifies the authenticated caller after the security layer.
#[derive(Debug, Clone)]
pub struct AuthSubject {
    /// The API-key row ID from the database (None for unauthenticated / bypass).
    pub api_key_id: Option<String>,
    /// Display / log name for the key, if available.
    pub label: Option<String>,
}

// ── Trace event ───────────────────────────────────────────────────────────────

/// A lightweight trace entry.  Replaces ad-hoc string annotations in handler.rs.
/// P2-F will replace this with proper OpenTelemetry spans.
#[derive(Debug, Clone)]
pub struct TraceEvent {
    pub elapsed_ms: u64,
    pub tag: &'static str,
    pub detail: String,
}

/// In-memory sink for `TraceEvent`s.
#[derive(Clone, Debug)]
pub struct TraceSink(Arc<Mutex<Vec<TraceEvent>>>);

impl TraceSink {
    pub fn new() -> Self {
        Self(Arc::new(Mutex::new(Vec::new())))
    }

    /// Append a trace event.
    pub fn push(&self, started_at: Instant, tag: &'static str, detail: impl Into<String>) {
        let elapsed_ms = started_at.elapsed().as_millis() as u64;
        if let Ok(mut guard) = self.0.lock() {
            guard.push(TraceEvent {
                elapsed_ms,
                tag,
                detail: detail.into(),
            });
        }
    }

    /// Snapshot all events (for logging / debugging).
    pub fn snapshot(&self) -> Vec<TraceEvent> {
        self.0.lock().map(|g| g.clone()).unwrap_or_default()
    }
}

impl Default for TraceSink {
    fn default() -> Self {
        Self::new()
    }
}

// ── RequestContext ────────────────────────────────────────────────────────────

/// Unified per-request context.  Stored in `axum::Extension` and cloned into
/// every handler that needs it.
///
/// Fields are filled in progressively:
/// - `request_id / started_at / deadline / cancellation` — set at middleware
/// - `ingress_protocol` — set at ingress decode
/// - `route_id / auth_subject` — set by `security`
/// - `provider_id / target_id / egress_protocol` — set by `planner`
/// - `outcome` — written once by `dispatcher` or `StreamBridge::Drop`
#[derive(Clone, Debug)]
pub struct RequestContext {
    /// Stable UUID for log correlation (e.g. "req-<uuid-v4>").
    pub request_id: String,
    /// When this request entered the gateway.
    pub started_at: Instant,
    /// Hard cut-off; any new I/O past this point should abort.
    pub deadline: Deadline,
    /// Shared cancellation flag; set on client disconnect or deadline.
    pub cancellation: CancellationToken,
    /// The protocol the client spoke on the ingress side.
    pub ingress_protocol: ProtocolId,
    /// Matched model ID (after `security` layer).
    pub model_id: Option<String>,
    /// Resolved provider ID (after `planner` layer).
    pub provider_id: Option<String>,
    /// Selected target ID (after `planner` layer).
    pub target_id: Option<String>,
    /// The protocol used on the egress (upstream) side.
    pub egress_protocol: Option<ProtocolId>,
    /// Authenticated caller, if any.
    pub auth_subject: Option<AuthSubject>,
    /// Final outcome — written exactly once.
    pub outcome: Arc<OnceLock<RequestOutcome>>,
    /// Lightweight trace log.
    pub trace: TraceSink,
}

impl RequestContext {
    /// Create a fresh context for an inbound request.
    pub fn new(ingress_protocol: ProtocolId, timeout: Duration) -> Self {
        Self {
            request_id: format!("req-{}", Uuid::new_v4()),
            started_at: Instant::now(),
            deadline: Deadline::from_now(timeout),
            cancellation: CancellationToken::new(),
            ingress_protocol,
            model_id: None,
            provider_id: None,
            target_id: None,
            egress_protocol: None,
            auth_subject: None,
            outcome: Arc::new(OnceLock::new()),
            trace: TraceSink::new(),
        }
    }

    /// Elapsed milliseconds since request entry.
    pub fn elapsed_ms(&self) -> f64 {
        self.started_at.elapsed().as_secs_f64() * 1000.0
    }

    /// Record the final outcome.  Silently ignores duplicate writes (only the
    /// first write wins, per `OnceLock` semantics).
    pub fn set_outcome(&self, outcome: RequestOutcome) {
        let _ = self.outcome.set(outcome);
    }

    /// Read the current outcome (None if not yet written).
    pub fn get_outcome(&self) -> Option<&RequestOutcome> {
        self.outcome.get()
    }

    /// Emit a trace event anchored to `self.started_at`.
    pub fn trace(&self, tag: &'static str, detail: impl Into<String>) {
        self.trace.push(self.started_at, tag, detail);
    }
}

// ── Axum middleware ───────────────────────────────────────────────────────────

use axum::extract::Request;
use axum::middleware::Next;
use axum::response::Response;

/// Axum middleware that creates a `RequestContext` and stores it as an
/// `Extension`.  Mount this as the outermost layer on the proxy router.
///
/// The timeout used is 300 s (matching the existing reqwest client timeout).
/// P2 can thread per-route timeouts through here once `RequestContext` carries
/// a configurable timeout.
pub async fn inject_context(mut request: Request, next: Next) -> Response {
    // We don't know the ingress protocol at middleware time; it will be
    // overwritten by the ingress handler via `inject_context_with_protocol`.
    // Use a sentinel until then.
    use crate::protocol::ids::OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1;
    let ctx = RequestContext::new(
        OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
        Duration::from_secs(300),
    );
    request.extensions_mut().insert(ctx);
    next.run(request).await
}

/// Helper called by each ingress handler to stamp the correct ingress protocol
/// onto the context that was injected by the middleware.
pub fn stamp_ingress_protocol(ctx: &mut RequestContext, protocol: ProtocolId) {
    ctx.ingress_protocol = protocol;
}

// ── Tests ─────────────────────────────────────────────────────────────────────

#[cfg(test)]
mod tests {
    use super::*;
    use crate::protocol::ids::OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1;

    #[test]
    fn request_id_is_unique() {
        let a = RequestContext::new(
            OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
            Duration::from_secs(30),
        );
        let b = RequestContext::new(
            OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
            Duration::from_secs(30),
        );
        assert_ne!(a.request_id, b.request_id);
        assert!(a.request_id.starts_with("req-"));
    }

    #[test]
    fn outcome_write_once() {
        let ctx = RequestContext::new(
            OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
            Duration::from_secs(30),
        );
        ctx.set_outcome(RequestOutcome::Success);
        ctx.set_outcome(RequestOutcome::ClientCancelled); // second write ignored
        assert_eq!(ctx.get_outcome(), Some(&RequestOutcome::Success));
    }

    #[test]
    fn cancellation_token_shared() {
        let ctx = RequestContext::new(
            OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
            Duration::from_secs(30),
        );
        let token = ctx.cancellation.clone();
        assert!(!token.is_cancelled());
        ctx.cancellation.cancel();
        assert!(token.is_cancelled());
    }

    #[test]
    fn deadline_remaining() {
        let d = Deadline::from_now(Duration::from_secs(60));
        assert!(!d.is_exceeded());
        assert!(d.remaining() > Duration::from_secs(50));
    }

    #[test]
    fn trace_sink_push_snapshot() {
        let ctx = RequestContext::new(
            OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
            Duration::from_secs(30),
        );
        ctx.trace("intake", "body parsed");
        ctx.trace("security", "api_key validated");
        let events = ctx.trace.snapshot();
        assert_eq!(events.len(), 2);
        assert_eq!(events[0].tag, "intake");
        assert_eq!(events[1].tag, "security");
    }
}
