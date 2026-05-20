//! Wire-level codec implementations. Each submodule owns a single Protocol's
//! request/response/stream codecs **and** the thin `EndpointHandler`
//! registration shell for every endpoint.

pub mod anthropic;
pub mod google;
pub mod openai;
pub mod reasoning;
pub mod tool_correlation;
