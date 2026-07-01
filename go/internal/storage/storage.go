package storage

import (
	"errors"
)

// ErrNotFound is returned by Update/Delete when no row matches the id.
var ErrNotFound = errors.New("storage: not found")

// StorageHealth describes a backend's runtime status.
type StorageHealth struct {
	Backend          string // "sqlite" | "postgres" | "mysql" | "memory"
	CanConnect       bool
	SchemaCompatible bool
	Writable         bool
}

// ProviderStore covers provider CRUD.
type ProviderStore interface {
	List() ([]Provider, error)
	Get(id string) (*Provider, error) // nil, nil = not found
	Create(in CreateProvider) (Provider, error)
	Update(id string, in UpdateProvider) (Provider, error)
	Delete(id string) error
	ExistsByName(name, excludeID string) (bool, error)
	RecordTestResult(providerID string, result ProviderTestResult) error
}

// ModelStore covers model-route CRUD (backends are managed via ModelBackendStore).
type ModelStore interface {
	List() ([]Model, error)
	Get(id string) (*Model, error)
	ByName(name string) (*Model, error)
	Create(in CreateModel) (Model, error)
	Update(id string, in UpdateModel) (Model, error)
	Delete(id string) error
	ExistsByName(name, excludeID string) (bool, error)
}

// ModelBackendStore manages the upstream targets of a model.
type ModelBackendStore interface {
	ListByModel(modelID string) ([]ModelBackend, error)
	SetBackends(modelID string, backends []CreateModelBackend) ([]ModelBackend, error)
	DeleteByModel(modelID string) error
}

// SettingsStore is the key-value config store.
type SettingsStore interface {
	Get(key string) (string, error) // "", nil = absent
	Set(key, value string) error
	ListAll() ([]Setting, error)
}

// Bootstrap handles schema initialization, migration, and health.
type Bootstrap interface {
	Init() error
	Migrate() error
	Health() (StorageHealth, error)
}

// ApiKeyStore covers gateway API-key CRUD (with model bindings).
type ApiKeyStore interface {
	List() ([]ApiKeyWithBindings, error)
	Get(id string) (*ApiKeyWithBindings, error)
	Create(in CreateApiKey) (ApiKeyWithBindings, error)
	Update(id string, in UpdateApiKey) (ApiKeyWithBindings, error)
	Delete(id string) error
	ExistsByName(name, excludeID string) (bool, error)
}

// AuthAccessStore is the read side used by the inbound access check: key lookup
// and model binding. Per-window quota counters used to live here too (backed by
// the now-removed request_logs table) but moved to the in-memory quota.Counter
// in P3a.
type AuthAccessStore interface {
	FindAPIKey(rawKey string) (*ApiKeyAccessRecord, error)
	ModelBindingExists(apiKeyID, modelID string) (bool, error)
	ListBoundModelIDs(apiKeyID string) ([]string, error)
}

// Storage is the aggregate persistence interface.
//
// The request-audit sink (LogStore) lived here through Phase 3 (dual-write);
// Phase 4 removed it — request logs now live exclusively in the parquet
// observability store (internal/observability), surfaced to the admin via
// admin.LogSource/admin.StatsSource.
type Storage interface {
	Providers() ProviderStore
	Models() ModelStore
	ModelBackends() ModelBackendStore
	Settings() SettingsStore
	APIKeys() ApiKeyStore
	Auth() AuthAccessStore
	Bootstrap() Bootstrap
}
