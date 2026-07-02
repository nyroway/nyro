// Package memory is an in-memory storage backend, used for tests and the
// no-DB desktop default. It implements storage.CoreStorage via Core(),
// delegating to per-sub-store wrapper types (Go cannot have two List methods
// with different return types on one struct).
package memory

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

// ErrNotFound is returned by Update/Delete when no row matches the id.
var ErrNotFound = errors.New("memory: not found")

// Backend is the in-memory storage backend.
type Backend struct {
	mu sync.RWMutex

	// config-schema (CoreStorage) state.
	upstreams      map[string]storage.Upstream
	routes         map[string]storage.Route
	routeUpstreams map[string]storage.RouteUpstream
	consumers      map[string]storage.Consumer
	consumerKeys   map[string]storage.ConsumerKey
	consumerRoutes map[string]consumerRouteGrant // synthetic id -> grant
	consumerQuotas map[string]storage.ConsumerQuota
	coreSettings   map[string]string
}

// consumerRouteGrant is one consumer_routes row (consumer_id, route_id), kept
// under a synthetic map key since the pair itself has no natural single key
// for Go map storage without a composite key type.
type consumerRouteGrant struct {
	ConsumerID string
	RouteID    string
}

// New creates an empty in-memory backend.
func New() *Backend {
	return &Backend{
		upstreams:      map[string]storage.Upstream{},
		routes:         map[string]storage.Route{},
		routeUpstreams: map[string]storage.RouteUpstream{},
		consumers:      map[string]storage.Consumer{},
		consumerKeys:   map[string]storage.ConsumerKey{},
		consumerRoutes: map[string]consumerRouteGrant{},
		consumerQuotas: map[string]storage.ConsumerQuota{},
		coreSettings:   map[string]string{},
	}
}

func (b *Backend) Bootstrap() storage.Bootstrap { return b }

// Core returns the backend as a storage.CoreStorage (config-schema tables).
// It is exposed as a distinct view — not by implementing CoreStorage directly
// on Backend — because CoreStorage.Auth() and the legacy Storage.Auth() above
// have the same name but different return types, which one Go type cannot
// implement simultaneously.
func (b *Backend) Core() storage.CoreStorage { return coreView{b} }

type coreView struct{ b *Backend }

func (v coreView) Upstreams() storage.UpstreamStore    { return upstreamStore{v.b} }
func (v coreView) Routes() storage.RouteStore          { return routeStore{v.b} }
func (v coreView) Consumers() storage.ConsumerStore    { return consumerStore{v.b} }
func (v coreView) Auth() storage.KeyAuthStore          { return keyAuthStore{v.b} }
func (v coreView) Settings() storage.CoreSettingsStore { return coreSettingsStore{v.b} }
func (v coreView) Bootstrap() storage.Bootstrap        { return v.b }

var _ storage.CoreStorage = coreView{}

// Bootstrap
func (b *Backend) Init() error    { return nil }
func (b *Backend) Migrate() error { return nil }
func (b *Backend) Health() (storage.StorageHealth, error) {
	return storage.StorageHealth{Backend: "memory", CanConnect: true, SchemaCompatible: true, Writable: true}, nil
}

// ── helpers ──

func newID() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

func nowISO() string { return time.Now().UTC().Format(time.RFC3339) }
