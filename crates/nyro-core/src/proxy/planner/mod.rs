//! Planning layer: route resolution + deterministic protocol negotiation.
//!
//! Modules:
//! - `negotiator`: `ProtocolPlan`, `ProtocolMode`, `negotiate()`,
//!   `RoutingStrategy` trait, `OrderedStrategy`.

pub mod negotiator;

pub use negotiator::{
    OrderedStrategy, ProtocolMode, ProtocolPlan, RoutingStrategy, get_routing_strategy, negotiate,
};

use crate::db::models::{Model, ModelBackend};
use crate::protocol::ids::ProtocolId;

// ── Plan ──────────────────────────────────────────────────────────────────────

/// The resolved plan for a single request, produced by the planner and
/// consumed by the dispatcher.
#[derive(Debug, Clone)]
pub struct Plan {
    /// The route that was matched.
    pub route: Model,
    /// The ordered list of targets to try (already sorted by strategy).
    pub ordered_targets: Vec<ModelBackend>,
    /// The ingress protocol (copied from `RequestContext`).
    pub ingress: ProtocolId,
    /// The resolved protocol negotiation plan.
    pub protocol_plan: ProtocolPlan,
}
