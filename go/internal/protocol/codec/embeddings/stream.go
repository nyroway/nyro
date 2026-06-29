package embeddings

import (
	"github.com/nyroway/nyro/go/internal/protocol/codec"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// Embeddings does not stream; these satisfy the codec interface as no-ops.

type streamResponseDecoder struct{}

func (*streamResponseDecoder) ParseChunk(string) ([]ir.StreamDelta, error) { return nil, nil }
func (*streamResponseDecoder) Finish() []ir.StreamDelta                    { return nil }

type streamResponseEncoder struct{}

func (*streamResponseEncoder) FormatDeltas([]ir.StreamDelta) ([]codec.SSE, error) { return nil, nil }
func (*streamResponseEncoder) FormatDone(ir.Usage) ([]codec.SSE, error)           { return nil, nil }
