//! Tests confirming the dispatcher-level route-type gate has been removed (Q2).
//!
//! Before Q2, routes carried a `route_type` field and the dispatcher rejected
//! embedding requests on "chat" routes (and vice-versa). That gate no longer
//! exists at the DB / protocol-negotiation layer. These tests document the new
//! invariants:
//!
//! 1. A provider declaring the `openai-compatible` *protocol* suite automatically
//!    exposes **all** endpoints in that suite — including embeddings — without
//!    any subset filtering.
//! 2. Endpoint-keyed legacy protocol values are still resolved to their parent
//!    suite during migration.

use nyro_core::db::models::Provider;
use nyro_core::protocol::ProviderProtocols;
use nyro_core::protocol::ids::{
    OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1, OPENAI_COMPATIBLE_EMBEDDINGS_V1,
};

fn provider(protocol: &str) -> Provider {
    Provider {
        id: "p".to_string(),
        name: "p".to_string(),
        vendor: None,
        protocol: protocol.to_string(),
        base_url: "https://a.example/v1".to_string(),
        preset_key: None,
        channel: None,
        models_source: None,
        static_models: None,
        api_key: String::new(),
        auth_mode: "apikey".to_string(),
        use_proxy: false,
        last_test_success: None,
        last_test_at: None,
        is_enabled: true,
        created_at: String::new(),
        updated_at: String::new(),
    }
}

/// A provider declaring the `openai-compatible` protocol *suite* must expose every
/// endpoint registered under that suite, including embeddings.
#[test]
fn openai_compat_protocol_suite_includes_embeddings() {
    let p = provider("openai-compatible");
    let pp = ProviderProtocols::from_provider(&p);

    assert!(
        pp.supports(OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1),
        "chat-completions must be included in openai-compatible suite"
    );
    assert!(
        pp.supports(OPENAI_COMPATIBLE_EMBEDDINGS_V1),
        "embeddings must be included in openai-compatible suite"
    );
}

/// Protocol parsing resolves any endpoint-id key to its parent `Protocol`, so
/// even a legacy endpoint-keyed value triggers full protocol-suite support.
/// `"openai/chat/v1"` belongs to `OpenAICompatible`, so embeddings is included.
#[test]
fn endpoint_keyed_format_expands_to_full_protocol_suite() {
    let p = provider("openai/chat/v1");
    let pp = ProviderProtocols::from_provider(&p);

    // parse_protocol("openai/chat/v1") → Protocol::OpenAICompatible → suite expansion
    assert!(pp.supports(OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1));
    assert!(
        pp.supports(OPENAI_COMPATIBLE_EMBEDDINGS_V1),
        "endpoint-keyed key triggers suite expansion; embeddings included"
    );

    // Embeddings resolves directly (Tier 1 — exact match after expansion).
    let resolved = pp.resolve_egress(OPENAI_COMPATIBLE_EMBEDDINGS_V1);
    assert_eq!(resolved.protocol, OPENAI_COMPATIBLE_EMBEDDINGS_V1);
    assert!(!resolved.needs_conversion);
    assert_eq!(resolved.base_url, "https://a.example/v1");
}

/// `"openai/embeddings/v1"` also belongs to `OpenAICompatible`, so the full suite is included.
#[test]
fn embeddings_endpoint_key_also_expands_to_full_openai_compat_suite() {
    let p = provider("openai/embeddings/v1");
    let pp = ProviderProtocols::from_provider(&p);

    // Both chat and embeddings present after suite expansion.
    assert!(pp.supports(OPENAI_COMPATIBLE_EMBEDDINGS_V1));
    assert!(
        pp.supports(OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1),
        "chat-completions included via OpenAICompatible suite expansion"
    );

    // Chat resolves directly with no conversion needed.
    let resolved = pp.resolve_egress(OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1);
    assert_eq!(resolved.protocol, OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1);
    assert!(!resolved.needs_conversion);
}
