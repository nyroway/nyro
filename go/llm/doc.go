// Package llm holds the LLM inference protocol family: everything needed to
// speak, translate between, and reason about the wire formats of chat-style
// model APIs.
//
// It has exactly three parts:
//
//	spec/   protocol identity — Protocol, ProtocolEndpoint, and the descriptor
//	        types codecs declare. Zero dependencies, so config/provider/admin/
//	        storage can name protocols without importing codec.
//	ir/     the canonical internal representation (AiRequest / AiResponse /
//	        StreamDelta) that every protocol decodes into and encodes out of.
//	codec/  the six codec interfaces plus one leaf package per wire format,
//	        laid out as {family}/{api} — see the naming rule below.
//
// # Codecs are bidirectional
//
// A codec is not an "inbound protocol adapter". The same EndpointHandler
// serves both directions, and which one it plays is decided per request by
// internal/proxy/dispatcher.go, not by the package:
//
//	ingress = the protocol the client spoke
//	          Decode (client → IR), Format (IR → client)
//	egress  = the protocol the upstream provider speaks
//	          Encode (IR → upstream), Parse (upstream → IR)
//
// When the two are equal the request is passed through natively with no IR
// round-trip; when they differ the gateway transforms. Because nyro's whole
// purpose is N×M translation, the same protocol routinely appears on both
// sides — which is why codecs are symmetric rather than split north/south.
//
// # Naming rule
//
//	Protocol ID  = {family}-{api}
//	package path = strings.ReplaceAll(id, "-", "/")
//
// family is the brand that owns the wire format's naming (openai, anthropic,
// gemini — note gemini, not google: Gemini is the protocol family and it has
// more than one API under it). api is the vendor's own API name, lowercased
// with all separators removed. There is exactly one "-", and the api segment
// must not contain another, or the ID↔path bijection breaks. Enforced by
// codec/layout_test.go.
//
// # Adding an ingress protocol
//
// Create the leaf package, declare its ingress routes in Capabilities(), and
// blank-import it from internal/proxy. There is no route table to edit: the
// proxy builds one by walking codec.All().
//
// # Stability
//
// This package is importable but carries no compatibility guarantee before
// github.com/nyroway/nyro/go is tagged go/v1.0.0. Until then the exported
// interfaces and the ir sealed unions may change without notice.
//
// MCP and A2A deliberately do NOT live here: they are not LLM inference calls
// and do not map onto the IR.
package llm
