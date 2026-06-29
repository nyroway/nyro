package codec

import "github.com/nyroway/nyro/go/internal/protocol/ids"

// registry maps each ProtocolEndpoint to its EndpointHandler.
//
// Populated via Register from package init() — the Go equivalent of Rust's
// inventory::submit!. A codec implementation package is only registered if it
// is imported (directly or via a blank import); the binary wires this up.
var registry = map[ids.ProtocolEndpoint]EndpointHandler{}

// Register registers a handler for its endpoint. Intended for init() use.
// A duplicate registration overwrites the previous handler.
func Register(h EndpointHandler) {
	registry[h.Endpoint()] = h
}

// Get looks up the handler for an endpoint.
func Get(ep ids.ProtocolEndpoint) (EndpointHandler, bool) {
	h, ok := registry[ep]
	return h, ok
}
