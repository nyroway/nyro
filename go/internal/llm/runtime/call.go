package runtime

import (
	"github.com/nyroway/nyro/go/internal/llm"
	"github.com/nyroway/nyro/go/internal/llm/protocol"
	"github.com/nyroway/nyro/go/internal/security/authn"
)

// Call is the transport-neutral input to one LLM Runtime execution. Operation
// and Resource carry transport-observed request metadata; direct callers may
// leave them empty to use the Source codec's declared route.
type Call struct {
	Request       llm.ModelRequest
	Source        protocol.Endpoint
	Operation     string
	Resource      string
	Credentials   authn.Credentials
	ClientAddress string
	RequestID     string
	Sink          Sink
}
