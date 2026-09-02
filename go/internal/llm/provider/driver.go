package provider

import (
	"context"
	"encoding/json"
	"io"

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
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
	Stream  bool
}

type Response struct {
	StatusCode int
	Headers    map[string][]string
	Body       io.ReadCloser
}

type Classification struct {
	Failed    bool
	Retryable bool
}

type Driver interface {
	Prepare(context.Context, UpstreamRuntime, protocol.WireRequest) (Request, error)
	Classify(Response) Classification
}

type Factory func() Driver
