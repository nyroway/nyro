// Package embeddings implements the OpenAI-compatible embeddings codec
// (/v1/embeddings). Embeddings is a passthrough endpoint: the gateway forwards
// the request body verbatim (only swapping the model for routing) and returns
// the upstream response verbatim — there is no AiResponse round-trip.
//
// Ported from crates/nyro-core/src/protocol/codec/openai/compatible/embeddings.rs.
// The Response/Stream codecs are stubs; the proxy takes a verbatim path for
// this endpoint (see proxy.Dispatch).
package embeddings

// BodyKey is the vendor-ingress key under which the original request body is
// preserved for verbatim forwarding.
const BodyKey = "__embeddings_passthrough_body__"
