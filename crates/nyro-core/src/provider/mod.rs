//! Provider layer — unified `Vendor` trait, metadata, and orchestration.
//!
//! # Architecture
//!
//! ```text
//! provider/
//! ├── vendor.rs           — Vendor trait + ProviderCtx (primary abstraction)
//! ├── vendor_ext.rs       — VendorExtension trait + VendorCtx (channel/family hooks)
//! ├── registry.rs         — VendorRegistry (unified, replaces dual registry)
//! ├── metadata.rs         — VendorMetadata types
//! ├── outbound.rs         — OutboundRequest (wire-format outbound)
//! ├── inbound.rs          — InboundResponse (wire-format inbound)
//! ├── common/
//! │   ├── openai_compat.rs — Bearer auth, URL helpers, openai_compat_vendor! macro
//! │   └── pipeline.rs      — standard 7-step request/response pipeline
//! └── <vendor>/mod.rs     — per-vendor Vendor impls
//! ```

pub mod common;
pub mod inbound;
pub mod metadata;
pub mod outbound;
pub mod registry;
pub mod vendor;
pub mod vendor_ext;

// ── Known vendors (each registers itself via inventory::submit!) ──────────────
pub mod anthropic;
pub mod custom;
pub mod deepseek;
pub mod google;
pub mod minimax;
pub mod moonshotai;
pub mod nvidia;
pub mod ollama;
pub mod openai;
pub mod openrouter;
pub mod requesty;
pub mod vertexai;
pub mod xai;
pub mod zai;
pub mod zhipuai;

// ── Flat re-exports ───────────────────────────────────────────────────────────

pub use inbound::InboundResponse;
pub use metadata::{
    AuthMode, ChannelDef, Label, OAuthConfig, ProtocolBaseUrl, RuntimeConfig, VendorMetadata,
};
pub use outbound::OutboundRequest;
pub use registry::{ExtensionRegistration, VendorRegistration, VendorRegistry, VendorScope};
pub use vendor::ProviderCtx;
pub use vendor::Vendor;
pub use vendor_ext::{VendorCtx, VendorExtension};
