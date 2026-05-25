//! `RequestHook` / `ResponseHook` traits and the process-wide `HookRegistry`.

use std::sync::{Arc, OnceLock};

use async_trait::async_trait;

use crate::error::GatewayError;
use crate::protocol::ir::{AiRequest, AiResponse};

// ── HookContext ───────────────────────────────────────────────────────────────

/// Context passed to every hook invocation. Fields are always owned strings so
/// hooks can be `'static` without lifetime constraints.
#[derive(Debug, Clone)]
pub struct HookContext {
    /// Matched model ID (from the model database row).
    pub model_id: String,
    /// Upstream provider name (empty for `RequestHook` — provider not yet selected).
    pub provider_name: String,
    /// The model name as seen by the client (may differ from upstream model).
    pub model: String,
    /// Authenticated API-key ID, if any.
    pub api_key_id: Option<String>,
}

// ── RequestHook ───────────────────────────────────────────────────────────────

/// Called after auth + cache-miss, before the upstream HTTP call.
///
/// - Returning `Ok(())` allows the request to proceed.
/// - Returning `Err(e)` immediately returns a 500 to the client and skips
///   upstream entirely.  Use sparingly — prefer observe-only hooks.
/// - Mutations to `req` (e.g. injecting headers, rewriting model name) are
///   forwarded to all subsequent pipeline stages.
#[async_trait]
pub trait RequestHook: Send + Sync + 'static {
    /// Short stable name used in log messages (e.g. `"content-moderation"`).
    fn name(&self) -> &'static str;

    async fn on_request(&self, ctx: &HookContext, req: &mut AiRequest) -> Result<(), GatewayError>;
}

/// `inventory` registration record for a [`RequestHook`].
pub struct RequestHookRegistration {
    pub make: fn() -> Box<dyn RequestHook>,
}

inventory::collect!(RequestHookRegistration);

// ── ResponseHook ──────────────────────────────────────────────────────────────

/// Called after a successful upstream response has been parsed into
/// [`AiResponse`], before it is formatted and returned to the client.
///
/// Errors returned by `on_response` are **logged and ignored** — the response
/// is still delivered to the client. This is intentional: response hooks must
/// not silently break real traffic.
///
/// Mutations to `resp` are visible to downstream formatters (e.g. injecting
/// extra fields into the response, enriching usage data).
#[async_trait]
pub trait ResponseHook: Send + Sync + 'static {
    fn name(&self) -> &'static str;

    async fn on_response(&self, ctx: &HookContext, resp: &mut AiResponse, latency_ms: u64);
}

/// `inventory` registration record for a [`ResponseHook`].
pub struct ResponseHookRegistration {
    pub make: fn() -> Box<dyn ResponseHook>,
}

inventory::collect!(ResponseHookRegistration);

// ── HookRegistry ──────────────────────────────────────────────────────────────

/// Process-wide registry of all registered hooks. Initialized once on first
/// access via `inventory` iteration.
pub struct HookRegistry {
    request_hooks: Vec<Arc<dyn RequestHook>>,
    response_hooks: Vec<Arc<dyn ResponseHook>>,
}

impl HookRegistry {
    /// Access the global singleton (built lazily on first call).
    pub fn global() -> &'static Self {
        static INSTANCE: OnceLock<HookRegistry> = OnceLock::new();
        INSTANCE.get_or_init(Self::build)
    }

    fn build() -> Self {
        let request_hooks = inventory::iter::<RequestHookRegistration>
            .into_iter()
            .map(|r| Arc::from((r.make)()) as Arc<dyn RequestHook>)
            .collect();
        let response_hooks = inventory::iter::<ResponseHookRegistration>
            .into_iter()
            .map(|r| Arc::from((r.make)()) as Arc<dyn ResponseHook>)
            .collect();
        Self {
            request_hooks,
            response_hooks,
        }
    }

    pub fn request_hooks(&self) -> &[Arc<dyn RequestHook>] {
        &self.request_hooks
    }

    pub fn response_hooks(&self) -> &[Arc<dyn ResponseHook>] {
        &self.response_hooks
    }

    pub fn has_request_hooks(&self) -> bool {
        !self.request_hooks.is_empty()
    }

    pub fn has_response_hooks(&self) -> bool {
        !self.response_hooks.is_empty()
    }
}
