package pipeline

import (
	"context"

	"github.com/nyroway/nyro/go/internal/llm"
)

// Phase is one explicit request-processing position. It cannot invoke the
// next phase; only Runner controls ordering and termination.
type Phase interface {
	Name() string
	Apply(context.Context, *Exchange) (Outcome, Finalizer)
}

// StreamObserver observes canonical deltas without controlling stream flow.
type StreamObserver interface {
	OnDelta(context.Context, *Exchange, llm.StreamDelta)
}
