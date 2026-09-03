package runtime

import (
	"context"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
)

// Sink is the client-facing delivery boundary. Implementations encode the
// canonical values for their ingress transport. SendOpaque is reserved for a
// Runtime-approved same-Endpoint passthrough. SendDelta reports committed only
// when that call made at least one complete event visible to the client.
// ResetStreamAttempt discards the current attempt's uncommitted encoder and
// delivery state before Runtime retries or fails over. It must reject resetting
// a stream that has already committed client-visible output.
type Sink interface {
	ResetStreamAttempt() error
	SendResponse(context.Context, *llm.ChatResponse) error
	SendError(context.Context, *llm.Error) error
	SendDelta(context.Context, llm.StreamDelta) (committed bool, err error)
	SendOpaque(context.Context, protocol.WireResponse) error
}
