//! Unified extension kernel (P0 scaffolding for the request-lifecycle RFC).
//!
//! Nyro already has several compile-time, `inventory`-based registries that act
//! as implicit plugin systems:
//!
//! - [`HookRegistry`] — request/response integration hooks.
//! - [`VendorRegistry`] — provider vendor presets / adapters.
//! - [`ProtocolRegistry`] — protocol endpoint handlers.
//!
//! [`PluginKernel`] is a thin, **read-only** façade that aggregates those
//! registries behind one entry point. It does not own or replace them; it only
//! offers a single place for future phases (`PhaseHook`, capabilities) and admin
//! tooling to enumerate "what is loaded". Introducing it now is purely additive
//! and changes no runtime behaviour.
//!
//! See `docs/design/lifecycle.md` for the full framework design.

use std::sync::OnceLock;

use crate::integrations::HookRegistry;
use crate::protocol::registry::ProtocolRegistry;
use crate::provider::VendorRegistry;

/// The kind of extension capability a manifest entry represents.
///
/// Mirrors the capability taxonomy in the lifecycle RFC. Only the
/// already-existing capabilities are surfaced today; `PhaseHook` /
/// `TelemetryExporter` slots are reserved for later phases.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum CapabilityKind {
    /// A request-phase integration hook ([`crate::integrations::RequestHook`]).
    RequestHook,
    /// A response-phase integration hook ([`crate::integrations::ResponseHook`]).
    ResponseHook,
    /// A provider vendor preset/adapter.
    ProviderVendor,
    /// A protocol endpoint handler.
    ProtocolEndpoint,
}

impl CapabilityKind {
    pub fn as_str(self) -> &'static str {
        match self {
            CapabilityKind::RequestHook => "request_hook",
            CapabilityKind::ResponseHook => "response_hook",
            CapabilityKind::ProviderVendor => "provider_vendor",
            CapabilityKind::ProtocolEndpoint => "protocol_endpoint",
        }
    }
}

/// A read-only description of one loaded extension.
#[derive(Debug, Clone)]
pub struct PluginManifest {
    /// Stable identifier of the extension (hook name / vendor id / protocol id).
    pub id: String,
    /// Which capability slot this extension occupies.
    pub capability: CapabilityKind,
}

/// Aggregated, read-only view over Nyro's compile-time extension registries.
///
/// Cheap to build (it only borrows the global registries) and cached as a
/// process-wide singleton via [`PluginKernel::global`].
pub struct PluginKernel {
    hooks: &'static HookRegistry,
    vendors: &'static VendorRegistry,
    protocols: &'static ProtocolRegistry,
}

impl PluginKernel {
    /// Process-wide kernel singleton.
    pub fn global() -> &'static PluginKernel {
        static KERNEL: OnceLock<PluginKernel> = OnceLock::new();
        KERNEL.get_or_init(|| PluginKernel {
            hooks: HookRegistry::global(),
            vendors: VendorRegistry::global(),
            protocols: ProtocolRegistry::global(),
        })
    }

    /// Enumerate every loaded extension across all registries.
    pub fn manifests(&self) -> Vec<PluginManifest> {
        let mut out = Vec::new();

        for hook in self.hooks.request_hooks() {
            out.push(PluginManifest {
                id: hook.name().to_string(),
                capability: CapabilityKind::RequestHook,
            });
        }
        for hook in self.hooks.response_hooks() {
            out.push(PluginManifest {
                id: hook.name().to_string(),
                capability: CapabilityKind::ResponseHook,
            });
        }
        for vendor in self.vendors.list_metadata() {
            out.push(PluginManifest {
                id: vendor.id.to_string(),
                capability: CapabilityKind::ProviderVendor,
            });
        }
        for handler in self.protocols.list() {
            out.push(PluginManifest {
                id: handler.id().to_string(),
                capability: CapabilityKind::ProtocolEndpoint,
            });
        }

        out
    }

    /// Manifests filtered to a single capability kind.
    pub fn manifests_of(&self, kind: CapabilityKind) -> Vec<PluginManifest> {
        self.manifests()
            .into_iter()
            .filter(|m| m.capability == kind)
            .collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn kernel_aggregates_builtin_extensions() {
        let kernel = PluginKernel::global();
        let manifests = kernel.manifests();

        // Built-in protocol endpoints and vendor presets are always registered,
        // so the aggregated view must never be empty.
        assert!(
            !manifests.is_empty(),
            "expected built-in extensions to be registered"
        );
        assert!(
            !kernel
                .manifests_of(CapabilityKind::ProtocolEndpoint)
                .is_empty(),
            "expected at least one protocol endpoint"
        );
        assert!(
            !kernel
                .manifests_of(CapabilityKind::ProviderVendor)
                .is_empty(),
            "expected at least one provider vendor"
        );
    }
}
