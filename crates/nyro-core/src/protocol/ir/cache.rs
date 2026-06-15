//! Cache control types for prompt caching.
//!
//! `CacheControl` is carried on `ContentBlock` variants that support it.
//! The egress encoder translates it into the wire format expected by the target
//! protocol (Anthropic per-block `cache_control`, OpenAI `prompt_cache_retention`,
//! or Google `cachedContent` resource reference).

use serde::{Deserialize, Serialize};

/// Cache time-to-live hint.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum CacheTtl {
    /// 5-minute ephemeral cache (Anthropic default for `type = "ephemeral"`).
    #[default]
    Ephemeral5m,
    /// 1-hour extended ephemeral cache (Anthropic `ttl = "1h"`).
    Ephemeral1h,
}

/// Per-block cache control breakpoint.
///
/// Placed on a `ContentBlock` by the ingress decoder when the client explicitly
/// requests a cache breakpoint at that position (e.g. Anthropic `cache_control`
/// field).  The encoder decides how to translate this into the wire format.
#[derive(Debug, Clone, PartialEq, Eq, Default, Serialize, Deserialize)]
pub struct CacheControl {
    pub ttl: CacheTtl,
    /// Priority for multi-breakpoint injection ordering (0 = lowest / injected last).
    pub breakpoint_priority: u8,
}

impl CacheControl {
    pub fn ephemeral() -> Self {
        Self::default()
    }

    pub fn ephemeral_1h() -> Self {
        Self {
            ttl: CacheTtl::Ephemeral1h,
            breakpoint_priority: 0,
        }
    }
}
