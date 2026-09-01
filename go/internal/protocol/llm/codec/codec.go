// Package codec defines workload-specific codec interfaces that translate
// between external wire protocols and canonical LLM requests and responses.
// Chat stream codecs are stateful and are created once per request stream.
//
//	client → model     ChatRequestDecoder.Decode
//	model → upstream   ChatRequestEncoder.Encode
//	upstream → model   ChatResponseDecoder.Parse (non-stream)
//	model → client     ChatResponseEncoder.Format (non-stream)
//	upstream stream→IR StreamResponseDecoder.ParseChunk / Finish
//	IR → client stream StreamResponseEncoder.FormatDeltas / FormatDone
package codec

import (
	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/protocol/llm/spec"
)

// ChatRequestDecoder decodes an inbound chat wire request.
type ChatRequestDecoder interface {
	Decode(body []byte) (*llm.ChatRequest, error)
}

// ChatPathDecoder is implemented by chat codecs whose model and/or stream flag live in
// the URL rather than the request body — Gemini embeds both in
// /v1beta/models/{model}:{action}.
//
// The ingress shell hands over the matched route parameters verbatim and calls
// DecodeWithPath instead of Decode. Interpreting them is the codec's job: the
// URL shape is part of the wire protocol, so it belongs to the package that
// defines the protocol rather than to the Gateway.
type ChatPathDecoder interface {
	DecodeWithPath(body []byte, params map[string]string) (*llm.ChatRequest, error)
}

// ChatRequestEncoder encodes a canonical chat request for an upstream.
type ChatRequestEncoder interface {
	Encode(req *llm.ChatRequest) (OutboundRequest, error)
}

// EmbeddingRequestDecoder decodes an inbound embedding wire request.
type EmbeddingRequestDecoder interface {
	Decode(body []byte) (*llm.EmbeddingRequest, error)
}

// EmbeddingRequestEncoder encodes a canonical embedding request for an upstream.
type EmbeddingRequestEncoder interface {
	Encode(req *llm.EmbeddingRequest) (OutboundRequest, error)
}

// ChatResponseDecoder parses a non-streaming upstream chat response.
type ChatResponseDecoder interface {
	Parse(body []byte) (*llm.ChatResponse, error)
}

// ChatResponseEncoder formats a canonical chat response for the client.
type ChatResponseEncoder interface {
	Format(resp *llm.ChatResponse) ([]byte, error)
}

// StreamResponseDecoder parses upstream stream chunks into IR deltas.
// Stateful — create one per request stream via ChatEndpointHandler.
type StreamResponseDecoder interface {
	// ParseChunk processes one upstream SSE payload (the bytes after "data ").
	// Returns zero or more deltas; a "[DONE]" sentinel yields a Done delta.
	ParseChunk(payload string) ([]llm.StreamDelta, error)
	// Finish emits any terminal deltas when the upstream stream ends without
	// an explicit Done (e.g. synthesize Done on clean EOF).
	Finish() []llm.StreamDelta
}

// StreamResponseEncoder formats IR deltas into client-facing SSE frames.
// Stateful — create one per request stream via ChatEndpointHandler.
type StreamResponseEncoder interface {
	FormatDeltas(deltas []llm.StreamDelta) ([]SSE, error)
	FormatDone(usage llm.Usage) ([]SSE, error)
}

// OutboundRequest is the encoded upstream HTTP call produced by a request encoder.
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

// EndpointHandler describes one protocol endpoint without assuming a workload.
type EndpointHandler interface {
	// Endpoint identifies which protocol endpoint this handler serves.
	Endpoint() spec.ProtocolEndpoint
	// Capabilities describes the endpoint statically: the ingress routes it
	// claims and what the gateway may assume about it. Declaring this here
	// rather than in spec keeps everything about one protocol — wire types,
	// codecs, routes, capabilities — inside its own leaf package.
	Capabilities() spec.EndpointCapabilities
}

// ChatEndpointHandler supplies the codecs required by a chat endpoint.
type ChatEndpointHandler interface {
	EndpointHandler
	MakeRequestDecoder() ChatRequestDecoder
	MakeRequestEncoder() ChatRequestEncoder
	MakeResponseDecoder() ChatResponseDecoder
	MakeResponseEncoder() ChatResponseEncoder
	MakeStreamResponseDecoder() StreamResponseDecoder
	MakeStreamResponseEncoder() StreamResponseEncoder
}

// EmbeddingEndpointHandler supplies the request codecs required by a
// non-streaming embedding endpoint. Its response remains wire-compatible and
// is passed through without a canonical response conversion.
type EmbeddingEndpointHandler interface {
	EndpointHandler
	MakeRequestDecoder() EmbeddingRequestDecoder
	MakeRequestEncoder() EmbeddingRequestEncoder
}
