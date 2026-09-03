package protocol_test

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/llm/protocol/anthropic/messages"
	"github.com/nyroway/nyro/go/internal/llm/protocol/gemini/generatecontent"
	"github.com/nyroway/nyro/go/internal/llm/protocol/openai/chatcompletions"
	"github.com/nyroway/nyro/go/internal/llm/protocol/openai/embeddings"
	"github.com/nyroway/nyro/go/internal/llm/protocol/openai/responses"
)

func TestBuiltInIngressCodecsEncodeProtocolNativeErrors(t *testing.T) {
	providerError := llm.NewError(llm.ErrRateLimitError, "rate limited").WithStatus(429)
	tests := []struct {
		name  string
		codec protocol.IngressCodec
		body  string
	}{
		{"OpenAI Chat Completions", chatcompletions.NewIngress(), `{"error":{"message":"rate limited","type":"rate_limit_error"}}`},
		{"OpenAI Responses", responses.NewIngress(), `{"error":{"message":"rate limited","type":"rate_limit_error"}}`},
		{"OpenAI Embeddings", embeddings.NewIngress(), `{"error":{"message":"rate limited","type":"rate_limit_error"}}`},
		{"Anthropic Messages", messages.NewIngress(), `{"type":"error","error":{"type":"rate_limit_error","message":"rate limited"}}`},
		{"Gemini GenerateContent", generatecontent.NewIngress(), `{"error":{"code":429,"message":"rate limited","status":"RESOURCE_EXHAUSTED"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire, err := test.codec.EncodeError(providerError)
			if err != nil {
				t.Fatalf("EncodeError: %v", err)
			}
			if wire.Status != 429 || wire.Headers["Content-Type"] != "application/json" || string(wire.Body) != test.body {
				t.Fatalf("wire = status %d headers %#v body %q", wire.Status, wire.Headers, wire.Body)
			}
		})
	}
}

func TestBuiltInIngressCodecsMapCanonicalKindsToNativeDiscriminators(t *testing.T) {
	providerError := llm.NewError(llm.ErrInvalidRequest, "invalid prompt").WithStatus(400)
	tests := []struct {
		name  string
		codec protocol.IngressCodec
		body  string
	}{
		{"OpenAI Chat Completions", chatcompletions.NewIngress(), `{"error":{"message":"invalid prompt","type":"invalid_request_error"}}`},
		{"OpenAI Responses", responses.NewIngress(), `{"error":{"message":"invalid prompt","type":"invalid_request_error"}}`},
		{"OpenAI Embeddings", embeddings.NewIngress(), `{"error":{"message":"invalid prompt","type":"invalid_request_error"}}`},
		{"Anthropic Messages", messages.NewIngress(), `{"type":"error","error":{"type":"invalid_request_error","message":"invalid prompt"}}`},
		{"Gemini GenerateContent", generatecontent.NewIngress(), `{"error":{"code":400,"message":"invalid prompt","status":"INVALID_ARGUMENT"}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			wire, err := test.codec.EncodeError(providerError)
			if err != nil {
				t.Fatalf("EncodeError: %v", err)
			}
			if string(wire.Body) != test.body {
				t.Fatalf("body = %q, want %q", wire.Body, test.body)
			}
		})
	}
}

func TestBuiltInChatStreamEncodersEncodeProtocolNativeTerminalErrors(t *testing.T) {
	providerError := llm.NewError(llm.ErrRateLimitError, "rate limited").WithStatus(429)
	tests := []struct {
		name  string
		codec protocol.IngressCodec
		wire  string
	}{
		{"OpenAI Chat Completions", chatcompletions.NewIngress(), "data: {\"error\":{\"message\":\"rate limited\",\"type\":\"rate_limit_error\"}}\n\n"},
		{"OpenAI Responses", responses.NewIngress(), "data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"rate_limit_error\",\"message\":\"rate limited\"}}}\n\n"},
		{"Anthropic Messages", messages.NewIngress(), "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"rate limited\"}}\n\n"},
		{"Gemini GenerateContent", generatecontent.NewIngress(), "data: {\"error\":{\"code\":429,\"message\":\"rate limited\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			codec, ok := test.codec.(protocol.ChatIngressCodec)
			if !ok {
				t.Fatalf("codec %T is not a ChatIngressCodec", test.codec)
			}
			frames, err := codec.NewStreamEncoder().FormatDeltas([]llm.StreamDelta{&llm.StreamErrorDelta{Error: providerError}})
			if err != nil {
				t.Fatalf("FormatDeltas: %v", err)
			}
			var got []byte
			for _, frame := range frames {
				got = append(got, frame.Bytes()...)
			}
			if string(got) != test.wire {
				t.Fatalf("terminal stream wire = %q, want %q", got, test.wire)
			}
		})
	}
}
