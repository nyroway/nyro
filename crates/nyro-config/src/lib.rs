//! Runtime resource configuration. Process bootstrap settings belong to the host.
pub mod compile;
pub mod resources;
pub use resources::{Error as ConfigError, Resources as Config};
