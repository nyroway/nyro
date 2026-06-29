package proxy

import (
	"context"
	"time"

	"github.com/nyroway/nyro/go/internal/protocol/ids"
)

// RequestContext is the per-request state threaded through the dispatcher.
// Fields are progressively filled as the request flows (port from
// RequestContext; P1 keeps the subset the dispatcher actually uses).
type RequestContext struct {
	Context         context.Context
	RequestID       string
	StartedAt       time.Time
	IngressProtocol ids.ProtocolEndpoint
	EgressProtocol  ids.ProtocolEndpoint
	ModelID         string
	ProviderID      string
}
