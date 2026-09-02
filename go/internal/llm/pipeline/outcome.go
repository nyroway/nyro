package pipeline

import (
	"context"

	"github.com/nyroway/nyro/go/internal/llm"
)

// Decision is the only control result a Phase may return.
type Decision uint8

const (
	Continue Decision = iota
	ShortCircuit
	Reject
)

// Outcome controls whether the Runner continues to later phases.
type Outcome struct {
	Decision Decision
	Error    *llm.Error
}

// Completion is the canonical result visible to every Finalizer.
type Completion struct {
	Response *llm.ChatResponse
	Error    *llm.Error
	Usage    llm.Usage
	Streamed bool
}

// Finalizer releases or records Phase-private request state. The Runner calls
// all registered Finalizers exactly once in reverse registration order.
type Finalizer func(context.Context, *Exchange, Completion) error
