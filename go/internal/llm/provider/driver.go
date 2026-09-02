package provider

import (
	"context"
	"encoding/json"
	"io"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

type UpstreamRuntime struct {
	Name            string
	Provider        string
	Protocol        string
	BaseURL         string
	CredentialsJSON json.RawMessage
	ProxyURL        string
}

type Request struct {
	Method   string
	URL      string
	Headers  map[string]string
	Body     []byte
	Stream   bool
	ProxyURL string
}

type Response struct {
	StatusCode int
	Headers    map[string][]string
	Body       io.ReadCloser
}

type Classification struct {
	Failed    bool
	Retryable bool
	Error     *llm.Error
}

type Driver interface {
	// ExtendRequest applies Provider-owned canonical extensions to one cloned
	// attempt request. It must not retain or mutate requests from other attempts.
	ExtendRequest(context.Context, UpstreamRuntime, llm.ModelRequest) error
	// Prepare applies endpoint, header, authentication, and signing rules after
	// the Egress Codec has encoded the canonical request.
	Prepare(context.Context, UpstreamRuntime, protocol.WireRequest) (Request, error)
	// Classify inspects raw Provider metadata without reading or closing Body.
	Classify(Response) Classification
	// ExtendResponse and ExtendError apply Provider-owned post-processing only
	// after the Egress Codec or Runtime has produced a normalized value.
	ExtendResponse(context.Context, UpstreamRuntime, *llm.ChatResponse) error
	ExtendError(context.Context, UpstreamRuntime, *llm.Error) error
}

type Factory func() Driver
