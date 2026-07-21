// Package memory is an in-memory storage backend, used for tests and
// standalone mode. It implements storage.Storage via Storage(), delegating to
// per-sub-store wrapper types (Go cannot have two List methods with different
// return types on one struct).
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

	// config-schema (Storage) state.
	upstreams      map[string]storage.Upstream
	routes         map[string]storage.Route
	routeUpstreams map[string]storage.RouteUpstream
	consumers      map[string]storage.Consumer
	consumerKeys   map[string]storage.ConsumerKey
	consumerRoutes map[string]consumerRouteGrant // synthetic id -> grant
	consumerQuotas map[string]storage.ConsumerQuota
	settings       map[string]string

	// plaintextKeys mirrors the database backend: store the recoverable raw
	// key on creation so it can be retrieved later. Set once at startup via
	// SetPlaintextKeys; default false (hash-only). Standalone mode leaves this
	// off — the raw keys already live in the operator's YAML.
	plaintextKeys bool
}

// SetPlaintextKeys toggles recoverable plaintext key storage. It is set once
// at startup before the backend serves any request.
func (b *Backend) SetPlaintextKeys(v bool) { b.plaintextKeys = v }

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
		settings:       map[string]string{},
	}
}

func (b *Backend) Migrator() storage.Migrator { return b }

// Storage returns the backend as a storage.Storage. It is exposed as a
// distinct view type — not by implementing storage.Storage directly on
// Backend — because Backend already exposes Migrator() storage.Migrator
// directly and Go cannot layer a second same-named-but-differently-typed
// method set onto one struct.
func (b *Backend) Storage() storage.Storage { return storageView{b} }

type storageView struct{ b *Backend }

func (v storageView) Upstreams() storage.UpstreamStore { return upstreamStore(v) }
func (v storageView) Routes() storage.RouteStore       { return routeStore(v) }
func (v storageView) Consumers() storage.ConsumerStore { return consumerStore(v) }
func (v storageView) Auth() storage.KeyAuthStore       { return keyAuthStore(v) }
func (v storageView) Settings() storage.SettingsStore  { return coreSettingsStore(v) }
func (v storageView) Migrator() storage.Migrator       { return v.b }

var _ storage.Storage = storageView{}

// Migrator
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
