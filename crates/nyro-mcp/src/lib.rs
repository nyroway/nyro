//! MCP tools gateway, independent of the LLM application and generation host.
mod body;
pub mod config;
mod error;
mod handler;
mod headers;
mod runtime;
mod upstream;
pub use runtime::{BuildError, Runtime};
