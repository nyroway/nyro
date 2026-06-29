// Package codec defines the six codec interfaces that translate between
// external wire protocols (OpenAI / Anthropic / Gemini) and the canonical IR.
//
// Each protocol endpoint implements EndpointHandler, which factories the six
// codecs. Request/stream encoders+decoders come in pairs:
//
//	client → IR        RequestDecoder.Decode
//	IR → upstream      RequestEncoder.Encode
//	upstream → IR      ResponseDecoder.Parse (non-stream)
//	IR → client        ResponseEncoder.Format (non-stream)
//	upstream stream→IR StreamResponseDecoder.ParseChunk / Finish
//	IR → client stream StreamResponseEncoder.FormatDeltas / FormatDone
//
// Stream codecs are stateful (one instance per request stream), hence the
// factory methods on EndpointHandler. Ported from protocol/{mod,traits}.rs.
package codec

import (
	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// RequestDecoder decodes an inbound wire request body into canonical IR.
type RequestDecoder interface {
	Decode(body []byte) (*ir.AiRequest, error)
}

// PathModelDecoder is implemented by codecs whose model and stream flag come
// from the URL path rather than the request body (Google Gemini embeds the
// model in the path: /v1beta/models/{model}:generateContent). The ingress
// shell extracts them and calls DecodeWithModel instead of Decode.
type PathModelDecoder interface {
	DecodeWithModel(body []byte, model string, stream bool) (*ir.AiRequest, error)
}

// RequestEncoder encodes canonical IR into the upstream wire request.
type RequestEncoder interface {
	Encode(req *ir.AiRequest) (OutboundRequest, error)
}

// ResponseDecoder parses a non-streaming upstream response body into IR.
type ResponseDecoder interface {
	Parse(body []byte) (*ir.AiResponse, error)
}

// ResponseEncoder formats an IR response into the client-facing wire body.
type ResponseEncoder interface {
	Format(resp *ir.AiResponse) ([]byte, error)
}

// StreamResponseDecoder parses upstream stream chunks into IR deltas.
// Stateful — create one per request stream via EndpointHandler.
type StreamResponseDecoder interface {
	// ParseChunk processes one upstream SSE payload (the bytes after "data ").
	// Returns zero or more deltas; a "[DONE]" sentinel yields a Done delta.
	ParseChunk(payload string) ([]ir.StreamDelta, error)
	// Finish emits any terminal deltas when the upstream stream ends without
	// an explicit Done (e.g. synthesize Done on clean EOF).
	Finish() []ir.StreamDelta
}

// StreamResponseEncoder formats IR deltas into client-facing SSE frames.
// Stateful — create one per request stream via EndpointHandler.
type StreamResponseEncoder interface {
	FormatDeltas(deltas []ir.StreamDelta) ([]SSE, error)
	FormatDone(usage ir.Usage) ([]SSE, error)
}

// OutboundRequest is the encoded upstream HTTP call produced by RequestEncoder.
type OutboundRequest struct {
	Method  string
	Path    string // appended to the provider base URL
	Headers map[string]string
	Body    []byte
	Stream  bool
}

// SSE is a single server-sent event frame written to the client.
type SSE struct {
	Event string // optional event name; empty omits the "event:" line
	Data  string // payload; "[DONE]" emits the OpenAI terminator
}

// Bytes serializes the frame to the wire SSE text (terminated by a blank line).
func (s SSE) Bytes() []byte {
	var b []byte
	if s.Event != "" {
		b = append(b, "event: "...)
		b = append(b, s.Event...)
		b = append(b, '\n')
	}
	b = append(b, "data: "...)
	b = append(b, s.Data...)
	b = append(b, '\n', '\n')
	return b
}

// EndpointHandler aggregates the six codec factories for one protocol endpoint.
type EndpointHandler interface {
	// Endpoint identifies which protocol endpoint this handler serves.
	Endpoint() ids.ProtocolEndpoint
	MakeRequestDecoder() RequestDecoder
	MakeRequestEncoder() RequestEncoder
	MakeResponseDecoder() ResponseDecoder
	MakeResponseEncoder() ResponseEncoder
	MakeStreamResponseDecoder() StreamResponseDecoder
	MakeStreamResponseEncoder() StreamResponseEncoder
}
