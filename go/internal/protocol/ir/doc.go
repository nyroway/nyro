// Package ir implements the canonical Internal Representation for the Nyro
// gateway: protocol-agnostic types that every wire codec decodes into and
// encodes out of.
//
// # Design principles (ported from protocol/ir/mod.rs)
//
//  1. No silent drops — every field from every supported protocol has an
//     explicit home: a core IR field, ProtocolExt, VendorExtensions, or DROP.
//  2. Lossless envelope — RawEnvelope keeps a snapshot of the original
//     request body + headers for pass-through and audit.
//  3. Repair, not validate — single-direction mutations (fill missing
//     tool_call_id, fix orphaned refs) without rejecting the request.
//
// # Sum-type representation (Go port of Rust enums)
//
// Rust enums are ported to Go using two conventions:
//
//   - Value enums (all unit variants, serialized as strings): a named string
//     type with const values — e.g. Role, AiErrorKind, CacheTtl, ids.Protocol.
//
//   - Tagged unions carrying data (Rust enums with struct/tuple variants): a
//     sealed interface backed by concrete struct variants, each implementing
//     an unexported marker method so the union is closed (only types in this
//     package satisfy it). Consumers dispatch with a type switch, mirroring
//     Rust's `match`. Examples: StreamDelta, ProtocolExt, ContentBlock.
//
// serde_json::Value fields (raw/preserved bytes) are ported as json.RawMessage
// to stay lossless; codecs parse them as needed. Wire-format JSON is produced
// by the codec layer (not by marshaling IR directly), so IR types generally
// carry no JSON tags.
package ir
