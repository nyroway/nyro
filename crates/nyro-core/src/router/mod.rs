pub mod health;
mod matcher;
pub mod selector;

pub use matcher::ModelCache;
pub use selector::{
    CooldownStrategy, LatencyStrategy, PriorityStrategy, RoutingStrategy, SelectedTarget,
    TargetSelector, WeightedStrategy,
};

use crate::db::models::Model;

impl ModelCache {
    pub fn match_model(&self, model: &str) -> Option<&Model> {
        matcher::match_model(&self.models, model)
    }
}
