//! Durable configuration drafts and published snapshots for the local control plane.

use nyro_config::Config;
use serde::Serialize;

pub mod entity;
mod storage;
pub use storage::Store;

pub const MAX_CONFIG_BYTES: usize = 1_048_576;

#[derive(Clone, Serialize)]
pub struct Snapshot {
    pub revision: u64,
    pub config: Config,
}

#[derive(Clone, Serialize)]
pub struct State {
    pub draft: Snapshot,
    pub published: Snapshot,
}

#[derive(Debug, thiserror::Error)]
pub enum Error {
    #[error("configuration storage failed")]
    Storage,
    #[error("invalid configuration")]
    Invalid,
    #[error("configuration revision conflict")]
    Conflict,
    #[error("entity not found")]
    NotFound,
    #[error("entity already exists")]
    AlreadyExists,
    #[error("entity is referenced")]
    Referenced,
    #[error("unsupported or corrupt configuration database")]
    Schema,
    #[error("a configuration seed is required")]
    SeedRequired,
    #[error("configuration database is already initialized")]
    AlreadyInitialized,
}
