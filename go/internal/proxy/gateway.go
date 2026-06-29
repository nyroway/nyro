package proxy

import (
	"net/http"
	"time"

	"github.com/nyroway/nyro/go/internal/auth"
	"github.com/nyroway/nyro/go/internal/router"
	"github.com/nyroway/nyro/go/internal/storage"
)

// Gateway holds the runtime dependencies for dispatching requests. It is
// config-driven: model→backend→provider resolution happens against Storage on
// every request; Router selects among a model's backends and tracks failover.
type Gateway struct {
	HTTPClient     *http.Client
	Storage        storage.Storage
	Router         *router.Router
	driverRegistry *auth.Registry
}

// NewGateway builds a Gateway backed by the given storage.
func NewGateway(s storage.Storage) *Gateway {
	return &Gateway{
		HTTPClient: &http.Client{Timeout: 5 * time.Minute},
		Storage:    s,
		Router:     router.New(),
	}
}

// SetDriverRegistry wires the OAuth driver registry (for token refresh).
func (g *Gateway) SetDriverRegistry(r *auth.Registry) { g.driverRegistry = r }
