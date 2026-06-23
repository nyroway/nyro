//! Phase-hook type skeleton (lifecycle RFC P1-a).
//!
//! This module defines the *data-plane* extension contract from
//! `docs/design/lifecycle.md` — the fixed five-phase model and the `PhaseHook`
//! trait plugins implement, plus a compile-time registry mirroring the existing
//! `inventory`-based registries.
//!
//! P1-a scope: **types + registration only**. Nothing here is wired into
//! `dispatch_pipeline` yet (that is P1-c), so introducing it is purely additive
//! and changes no runtime behaviour. `PhaseCtx` / `HostContext` are the stable
//! boundary the future pipeline will hand to hooks.

use std::sync::{Arc, OnceLock};

use crate::error::GatewayError;
use crate::protocol::ir::{AiRequest, AiResponse, AiStreamDelta, Usage};
use crate::proxy::context::RequestContext;
use async_trait::async_trait;
use axum::response::Response;

/// The fixed set of request-lifecycle phases (Nyro's analogue of nginx's
/// processing phases). The set is intentionally closed: new behaviour is added
/// via hooks within a phase, not by inventing new phases.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum Phase {
    /// Shape the routing key from request-only data (e.g. model alias rewrite).
    OnRequest,
    /// Native authn → routing → authz; the only phase that may reject.
    OnAccess,
    /// Target selection / load balancing; hooks here may short-circuit.
    OnUpstream,
    /// Upstream call + response handling (per-chunk for streaming).
    OnResponse,
    /// Unconditional terminal phase: logs, metrics, telemetry dispatch.
    OnLog,
}

impl Phase {
    pub fn as_str(self) -> &'static str {
        match self {
            Phase::OnRequest => "on_request",
            Phase::OnAccess => "on_access",
            Phase::OnUpstream => "on_upstream",
            Phase::OnResponse => "on_response",
            Phase::OnLog => "on_log",
        }
    }
}

/// What a hook sees of the response, depending on phase and mode.
///
/// Phases before the upstream call see [`ResponseView::Pending`]; non-stream
/// `OnResponse` sees [`ResponseView::Full`]; streaming `OnResponse` is invoked
/// once per [`AiStreamDelta`] (see lifecycle RFC §5.4).
pub enum ResponseView<'a> {
    /// No response yet (OnRequest / OnAccess / OnUpstream).
    Pending,
    /// A fully-buffered non-streaming response.
    Full(&'a mut AiResponse),
    /// One streaming delta; the hook is called repeatedly.
    Stream(&'a mut AiStreamDelta),
}

/// Canonical response-side snapshot for one request, injected into
/// [`RequestContext::extensions`] by the native `OnResponse` step so that
/// `OnLog` (and `OnLogHook`) read a single consistent set of metrics rather than
/// re-deriving them. See lifecycle RFC §4 ("响应侧状态注入 ctx").
///
/// Stored type-keyed in the [`crate::proxy::context::ContextBag`]; fetch with
/// `ctx.extensions.get::<ResponseStats>()`.
#[derive(Debug, Clone, Default)]
pub struct ResponseStats {
    /// Status code returned to the client.
    pub client_status: u16,
    /// Upstream provider status, when an upstream call was made.
    pub upstream_status: Option<u16>,
    /// Token usage as reported/accumulated for this response.
    pub usage: Usage,
    /// Upstream call latency in milliseconds, when measured.
    pub upstream_latency_ms: Option<i64>,
    /// Time-to-first-byte (first streamed chunk) in milliseconds.
    pub ttfb_ms: Option<i64>,
    /// Number of streamed chunks (0 for non-streaming responses).
    pub stream_chunks: u32,
}

/// Stable host boundary handed to hooks: storage / config / metrics / http.
///
/// Backed by the already-aggregating [`crate::Gateway`]; kept as a distinct
/// newtype so the surface stays stable even if `Gateway` internals change and
/// so a future WASM runtime can be bridged against the same boundary.
pub struct HostContext<'a> {
    pub gateway: &'a crate::Gateway,
}

impl<'a> HostContext<'a> {
    pub fn new(gateway: &'a crate::Gateway) -> Self {
        Self { gateway }
    }
}

/// The mutable, request-scoped context handed to every [`PhaseHook`].
///
/// `req_ctx` is the end-to-end [`RequestContext`] (with its type-keyed
/// `extensions` bag); `request` is the protocol-neutral IR; `response` reflects
/// the current phase; `host` is the stable host boundary.
pub struct PhaseCtx<'a> {
    pub req_ctx: &'a mut RequestContext,
    pub request: &'a mut AiRequest,
    pub response: ResponseView<'a>,
    pub host: &'a HostContext<'a>,
}

/// Control-flow signal returned by a hook.
pub enum PhaseOutcome {
    /// Proceed to the next hook / native step.
    Continue,
    /// Produce a response directly, skipping the upstream call (OnUpstream).
    ShortCircuit(Response),
    /// Reject the request with a typed error (OnAccess).
    Reject(GatewayError),
}

/// A data-plane plugin that runs within one lifecycle phase.
#[async_trait]
pub trait PhaseHook: Send + Sync {
    /// Stable identifier, used for manifests / diagnostics.
    fn name(&self) -> &'static str;
    /// Which phase this hook attaches to.
    fn phase(&self) -> Phase;
    /// Execute the hook against the mutable phase context.
    async fn run(&self, ctx: &mut PhaseCtx<'_>) -> PhaseOutcome;
}

/// Compile-time registration entry (mirrors `RequestHookRegistration` etc.).
pub struct PhaseHookRegistration {
    pub make: fn() -> Arc<dyn PhaseHook>,
}

inventory::collect!(PhaseHookRegistration);

/// Process-wide registry of all submitted [`PhaseHook`]s.
pub struct PhaseHookRegistry {
    hooks: Vec<Arc<dyn PhaseHook>>,
}

impl PhaseHookRegistry {
    /// Build (once) and return the global registry from all `inventory`
    /// submissions across the linked crates.
    pub fn global() -> &'static PhaseHookRegistry {
        static REGISTRY: OnceLock<PhaseHookRegistry> = OnceLock::new();
        REGISTRY.get_or_init(|| {
            let hooks = inventory::iter::<PhaseHookRegistration>
                .into_iter()
                .map(|reg| (reg.make)())
                .collect();
            PhaseHookRegistry { hooks }
        })
    }

    /// All registered hooks, in deterministic registration order.
    pub fn all(&self) -> &[Arc<dyn PhaseHook>] {
        &self.hooks
    }

    /// Hooks attached to a given phase, in deterministic registration order.
    pub fn for_phase(&self, phase: Phase) -> Vec<&Arc<dyn PhaseHook>> {
        self.hooks.iter().filter(|h| h.phase() == phase).collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    struct DummyOnRequestHook;

    #[async_trait]
    impl PhaseHook for DummyOnRequestHook {
        fn name(&self) -> &'static str {
            "test-dummy-on-request"
        }
        fn phase(&self) -> Phase {
            Phase::OnRequest
        }
        async fn run(&self, _ctx: &mut PhaseCtx<'_>) -> PhaseOutcome {
            PhaseOutcome::Continue
        }
    }

    inventory::submit! {
        PhaseHookRegistration { make: || Arc::new(DummyOnRequestHook) }
    }

    #[test]
    fn registry_collects_submitted_hook_under_correct_phase() {
        let reg = PhaseHookRegistry::global();
        assert!(
            reg.all()
                .iter()
                .any(|h| h.name() == "test-dummy-on-request"),
            "submitted hook must appear in the registry"
        );
        assert!(
            reg.for_phase(Phase::OnRequest)
                .iter()
                .any(|h| h.name() == "test-dummy-on-request"),
            "hook must be grouped under its declared phase"
        );
        assert!(
            reg.for_phase(Phase::OnLog)
                .iter()
                .all(|h| h.name() != "test-dummy-on-request"),
            "hook must not leak into other phases"
        );
    }
}
