package embeddings

import (
	"errors"

	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// ResponseDecoder/Encoder are unused: embeddings takes a verbatim passthrough
// path in the proxy (no AiResponse round-trip). They exist only to satisfy the
// codec.EndpointHandler interface.

type responseDecoder struct{}

func (responseDecoder) Parse([]byte) (*ir.AiResponse, error) {
	return nil, errors.New("embeddings uses verbatim passthrough; ResponseDecoder not called")
}

type responseEncoder struct{}

func (responseEncoder) Format(*ir.AiResponse) ([]byte, error) {
	return nil, errors.New("embeddings uses verbatim passthrough; ResponseEncoder not called")
}
