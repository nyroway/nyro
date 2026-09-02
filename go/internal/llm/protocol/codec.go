package protocol

import "github.com/nyroway/nyro/go/internal/llm"

type IngressCodec interface {
	Endpoint() Endpoint
	Capabilities() Capabilities
	IngressCodec()
}

type EgressCodec interface {
	Endpoint() Endpoint
	Capabilities() Capabilities
	EgressCodec()
}

type ChatIngressCodec interface {
	IngressCodec
	DecodeRequest(IngressRequest) (*llm.ChatRequest, error)
	EncodeResponse(*llm.ChatResponse) (WireResponse, error)
	NewStreamEncoder() StreamEncoder
}

type ChatEgressCodec interface {
	EgressCodec
	EncodeRequest(*llm.ChatRequest) (WireRequest, error)
	DecodeResponse(WireResponse) (*llm.ChatResponse, error)
	DecodeError(WireResponse) (*llm.Error, error)
	NewStreamDecoder() StreamDecoder
}

type EmbeddingIngressCodec interface {
	IngressCodec
	DecodeRequest(IngressRequest) (*llm.EmbeddingRequest, error)
}

type EmbeddingEgressCodec interface {
	EgressCodec
	EncodeRequest(*llm.EmbeddingRequest) (WireRequest, error)
	DecodeError(WireResponse) (*llm.Error, error)
}

type StreamDecoder interface {
	ParseChunk(string) ([]llm.StreamDelta, error)
	Finish() []llm.StreamDelta
}

type StreamEncoder interface {
	FormatDeltas([]llm.StreamDelta) ([]Event, error)
	FormatDone(llm.Usage) ([]Event, error)
}
