package runtime

import (
	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/security/authn"
)

// Call is the transport-neutral input to one LLM Runtime execution.
type Call struct {
	Request       llm.ModelRequest
	Source        protocol.Endpoint
	Credentials   authn.Credentials
	ClientAddress string
	RequestID     string
	Sink          Sink
}
