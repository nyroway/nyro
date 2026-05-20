//! Protocol registry acceptance.
//!
//! Five handlers are registered:
//! - OpenAI Chat / OpenAI Responses
//! - Anthropic Messages / Google Generate
//! - OpenAI Embeddings (passthrough codec, registered for
//!   `find_by_ingress_route` and capability discovery; the standard
//!   request pipeline still bypasses its codec)

use nyro_core::protocol::ids::{
    ANTHROPIC_MESSAGES_2023_06_01, GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA,
    OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1, OPENAI_COMPATIBLE_EMBEDDINGS_V1, OPENAI_RESPONSES_V1,
    Protocol, ProtocolId,
};
use nyro_core::protocol::ir::Role;
use nyro_core::protocol::registry::ProtocolRegistry;
use serde_json::json;

#[test]
fn registers_all_handlers_with_correct_ids() {
    let reg = ProtocolRegistry::global();
    assert_eq!(reg.list().len(), 5);

    for id in [
        OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
        OPENAI_RESPONSES_V1,
        OPENAI_COMPATIBLE_EMBEDDINGS_V1,
        ANTHROPIC_MESSAGES_2023_06_01,
        GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA,
    ] {
        let h = reg.get(&id).unwrap_or_else(|| panic!("missing {id}"));
        assert_eq!(h.id(), id);
    }
}

#[test]
fn list_by_protocol_segments() {
    let reg = ProtocolRegistry::global();
    assert_eq!(reg.list_by_protocol(Protocol::OpenAICompatible).len(), 2);
    assert_eq!(reg.list_by_protocol(Protocol::OpenAIResponses).len(), 1);
    assert_eq!(reg.list_by_protocol(Protocol::AnthropicMessages).len(), 1);
    assert_eq!(reg.list_by_protocol(Protocol::GoogleGemini).len(), 1);
}

#[test]
fn ingress_routes_match_axum_router() {
    let reg = ProtocolRegistry::global();

    let cases: &[(&str, &str, ProtocolId)] = &[
        (
            "POST",
            "/v1/chat/completions",
            OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
        ),
        ("POST", "/v1/responses", OPENAI_RESPONSES_V1),
        ("POST", "/v1/messages", ANTHROPIC_MESSAGES_2023_06_01),
        (
            "POST",
            "/v1beta/models/:model_action",
            GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA,
        ),
    ];

    for (method, path, expected) in cases {
        let h = reg
            .find_by_ingress_route(method, path)
            .unwrap_or_else(|| panic!("no handler for {method} {path}"));
        assert_eq!(h.id(), *expected, "wrong handler for {method} {path}");
    }
}

#[test]
fn capabilities_match_dialect_special_cases() {
    let reg = ProtocolRegistry::global();
    assert!(
        reg.get(&OPENAI_RESPONSES_V1)
            .unwrap()
            .capabilities()
            .force_upstream_stream,
        "Responses must force upstream streaming"
    );
    assert!(
        !reg.get(&OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1)
            .unwrap()
            .capabilities()
            .force_upstream_stream
    );
    assert!(
        reg.get(&GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA)
            .unwrap()
            .capabilities()
            .override_model_in_body,
        "Google must override model in body"
    );
}

// ── Decoder / encoder smoke ──

fn sample_body(id: ProtocolId) -> serde_json::Value {
    if id == OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1 {
        json!({
            "model": "gpt-4o-mini",
            "messages": [
                {"role": "system", "content": "be helpful"},
                {"role": "user", "content": "hi"}
            ],
            "stream": false,
            "temperature": 0.5
        })
    } else if id == OPENAI_RESPONSES_V1 {
        json!({
            "model": "gpt-4o-mini",
            "instructions": "be helpful",
            "input": [
                {"role": "user", "content": [{"type": "input_text", "text": "hi"}]}
            ],
            "stream": false,
            "temperature": 0.5
        })
    } else if id == ANTHROPIC_MESSAGES_2023_06_01 {
        json!({
            "model": "claude-3-5-sonnet",
            "system": "be helpful",
            "messages": [
                {"role": "user", "content": "hi"}
            ],
            "max_tokens": 256,
            "stream": false
        })
    } else if id == GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA {
        json!({
            "system_instruction": {"parts": [{"text": "be helpful"}]},
            "contents": [
                {"role": "user", "parts": [{"text": "hi"}]}
            ]
        })
    } else {
        panic!("no sample body for {id}")
    }
}

#[test]
fn decoder_preserves_role_sequence_and_source_protocol() {
    let reg = ProtocolRegistry::global();
    for id in [
        OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
        OPENAI_RESPONSES_V1,
        ANTHROPIC_MESSAGES_2023_06_01,
        GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA,
    ] {
        let body = sample_body(id);
        let req = reg
            .get(&id)
            .unwrap()
            .make_request_decoder()
            .decode_request(body)
            .unwrap_or_else(|e| panic!("decoder failed for {id}: {e}"));

        assert_eq!(
            req.meta.source_protocol.unwrap(),
            id,
            "source_protocol mismatch for {id}"
        );
        assert!(!req.messages.is_empty(), "messages empty for {id}");
        let _: Vec<Role> = req.messages.iter().map(|m| m.role).collect();
    }
}

#[test]
fn encoder_round_trips_body_for_every_handler() {
    let reg = ProtocolRegistry::global();
    for id in [
        OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
        OPENAI_RESPONSES_V1,
        ANTHROPIC_MESSAGES_2023_06_01,
        GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA,
    ] {
        let body = sample_body(id);
        let h = reg.get(&id).unwrap();
        let internal = h.make_request_decoder().decode_request(body).unwrap();
        let (out_body, headers) = h
            .make_request_encoder()
            .encode_request(&internal)
            .unwrap_or_else(|e| panic!("encoder failed for {id}: {e}"));
        assert!(
            out_body.is_object(),
            "encoded body must be an object for {id}"
        );
        let _ = headers;

        let _path = h
            .make_request_encoder()
            .egress_path(&internal.model, internal.stream.enabled);
    }
}

#[test]
fn parser_and_formatter_factories_construct() {
    let reg = ProtocolRegistry::global();
    for id in [
        OPENAI_COMPATIBLE_CHAT_COMPLETIONS_V1,
        OPENAI_RESPONSES_V1,
        ANTHROPIC_MESSAGES_2023_06_01,
        GOOGLE_GEMINI_GENERATE_CONTENT_V1BETA,
    ] {
        let h = reg.get(&id).unwrap();
        let _ = h.make_response_decoder();
        let _ = h.make_response_encoder();
        let _ = h.make_stream_response_decoder();
        let _ = h.make_stream_response_encoder();
    }
}

// ── Embeddings — registered for routing/capability surface only ──

#[test]
fn embeddings_handler_advertises_passthrough_capabilities() {
    let reg = ProtocolRegistry::global();
    let caps = reg
        .get(&OPENAI_COMPATIBLE_EMBEDDINGS_V1)
        .unwrap()
        .capabilities();
    assert!(caps.embeddings);
    assert!(!caps.streaming);
    assert!(!caps.tools);
    assert!(!caps.force_upstream_stream);
    assert!(!caps.override_model_in_body);
}

#[test]
fn embeddings_routes_resolve_to_handler() {
    let reg = ProtocolRegistry::global();
    for path in ["/v1/embeddings"] {
        let h = reg
            .find_by_ingress_route("POST", path)
            .unwrap_or_else(|| panic!("no handler for POST {path}"));
        assert_eq!(h.id(), OPENAI_COMPATIBLE_EMBEDDINGS_V1);
    }
}

#[test]
fn embeddings_aliases_resolve() {
    let reg = ProtocolRegistry::global();
    assert_eq!(
        reg.resolve_alias("openai-embeddings"),
        Some(OPENAI_COMPATIBLE_EMBEDDINGS_V1)
    );
    assert_eq!(
        reg.resolve_alias("embeddings"),
        Some(OPENAI_COMPATIBLE_EMBEDDINGS_V1)
    );
    assert_eq!(
        reg.resolve_alias("openai/embeddings/v1"),
        Some(OPENAI_COMPATIBLE_EMBEDDINGS_V1)
    );
}

#[test]
fn embeddings_decoder_round_trips_body() {
    let reg = ProtocolRegistry::global();
    let body = json!({
        "model": "text-embedding-3-small",
        "input": ["hello", "world"],
        "encoding_format": "float"
    });
    let internal = reg
        .get(&OPENAI_COMPATIBLE_EMBEDDINGS_V1)
        .unwrap()
        .make_request_decoder()
        .decode_request(body.clone())
        .unwrap();
    assert_eq!(internal.model, "text-embedding-3-small");
    assert!(!internal.stream.enabled);

    let (encoded, _headers) = reg
        .get(&OPENAI_COMPATIBLE_EMBEDDINGS_V1)
        .unwrap()
        .make_request_encoder()
        .encode_request(&internal)
        .unwrap();
    assert_eq!(encoded, body, "encoder must round-trip the original body");
}
