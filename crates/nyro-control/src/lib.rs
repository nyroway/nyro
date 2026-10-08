//! Durable gateway resources; each save is an atomic desired-state change.
pub mod resource;
pub use resource::{POSTGRES_SCHEMA, Store};
pub const MAX_CONFIG_BYTES: usize = 1_048_576;
#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("configuration storage failed")]
    Storage,
    #[error("configuration storage outcome is unknown; restart and reconcile durable state")]
    OutcomeUnknown,
    #[error("invalid configuration")]
    Invalid,
    #[error("entity not found")]
    NotFound,
    #[error("entity already exists")]
    AlreadyExists,
    #[error("entity is referenced")]
    Referenced,
    #[error("unsupported or corrupt configuration database")]
    Schema,
}
