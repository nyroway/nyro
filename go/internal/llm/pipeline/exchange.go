// Package pipeline defines the transport-neutral LLM request pipeline.
package pipeline

import (
	"time"

	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/security/authn"
	"github.com/nyroway/nyro/go/internal/security/authz"
)

// LogicalRoute is the resolved client-facing route visible across phases.
type LogicalRoute struct {
	ID    string
	Model string
}

// Target is the selected backend information visible across phases.
type Target struct {
	ID                string
	UpstreamID        string
	UpstreamName      string
	Model             string
	Endpoint          protocol.Endpoint
	UpstreamStatus    *int32
	UpstreamLatencyMs *int64
}

// RequestInfo carries normalized ingress metadata used by audit telemetry.
type RequestInfo struct {
	ClientModel string
	Operation   string
	Resource    string
}

// Exchange is the typed, per-request state shared by LLM phases. Context is
// passed explicitly to phases and finalizers and is intentionally absent here.
type Exchange struct {
	Request       llm.ModelRequest
	Source        protocol.Endpoint
	Credentials   authn.Credentials
	Identity      authn.Identity
	Authorization authz.Decision
	Route         LogicalRoute
	Target        Target
	Response      *llm.ChatResponse
	Usage         llm.Usage
	Error         *llm.Error
	Started       time.Time
	Streamed      bool
	Status        int
	RequestInfo   RequestInfo
	ClientAddress string
	RequestID     string
}
