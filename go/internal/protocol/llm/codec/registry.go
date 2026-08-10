package codec

import (
	"sort"

	"github.com/nyroway/nyro/go/internal/protocol/llm/spec"
)

// registry maps each ProtocolEndpoint to its EndpointHandler.
//
// Populated via Register from package init() — the Go equivalent of Rust's
// inventory::submit!. A codec implementation package is only registered if it
// is imported (directly or via a blank import); the binary wires this up.
var registry = map[spec.ProtocolEndpoint]EndpointHandler{}

// Register registers a handler for its endpoint. Intended for init() use.
// A duplicate registration overwrites the previous handler.
func Register(h EndpointHandler) {
	registry[h.Endpoint()] = h
}

// Get looks up the handler for an endpoint.
func Get(ep spec.ProtocolEndpoint) (EndpointHandler, bool) {
	h, ok := registry[ep]
	return h, ok
}

// All returns every registered handler, ordered by endpoint string so callers
// that build routes or render lists are deterministic across runs.
func All() []EndpointHandler {
	out := make([]EndpointHandler, 0, len(registry))
	for _, h := range registry {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Endpoint().String() < out[j].Endpoint().String()
	})
	return out
}

// EndpointFor returns the registered endpoint for a protocol — the replacement
// for a hand-written protocol→endpoint switch. If a protocol ever registers
// more than one version, the lowest endpoint string wins; make that explicit
// here rather than at the call site when it happens.
func EndpointFor(p spec.Protocol) (spec.ProtocolEndpoint, bool) {
	for _, h := range All() {
		if ep := h.Endpoint(); ep.Protocol == p {
			return ep, true
		}
	}
	return spec.ProtocolEndpoint{}, false
}
