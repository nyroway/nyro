#![doc = include_str!("../README.md")]

mod host;
mod lifecycle;

pub use host::{Candidate, GenerationInfo, GenerationStatus, Host, HostOptions, Lease, Status};
pub use lifecycle::{Component, ComponentId, Context, Issue, KernelError, Lifecycle, Phase};
pub use tokio_util::sync::CancellationToken;
