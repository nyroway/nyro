package storage

import (
	"errors"
	"time"
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
// request_logs) but moved to the in-memory quota.Counter in P3a.
type AuthAccessStore interface {
	FindAPIKey(rawKey string) (*ApiKeyAccessRecord, error)
	ModelBindingExists(apiKeyID, modelID string) (bool, error)
	ListBoundModelIDs(apiKeyID string) ([]string, error)
}

// OAuthCredentialStore holds upstream OAuth tokens.
//
// The CAS methods (TryBeginRefresh/CompleteRefresh/FailRefresh/ListExpiring/
// RecoverStaleRefreshing) coordinate cross-replica refresh via the shared DB.
// The gateway stopped using them in xDS P3b: it now reads OAuth from its
// ConfigCache snapshot and refreshes locally under a per-process mutex. They
// remain on the interface because the admin process still drives DB-backed
// reconnect/refresh flows. ListAll is the full-table snapshot used to populate
// the xDS ConfigSnapshot (admin → gateway).
type OAuthCredentialStore interface {
	Get(providerID string) (*OAuthCredential, error)
	ListAll() ([]OAuthCredential, error)
	Upsert(providerID string, in UpsertOAuthCredential) (OAuthCredential, error)
	Delete(providerID string) error
	TryBeginRefresh(providerID string, expectedVersion int32) (*OAuthCredential, error)
	CompleteRefresh(providerID string, in UpsertOAuthCredential) (OAuthCredential, error)
	FailRefresh(providerID string, errorMessage string) error
	ListExpiring(before time.Duration) ([]OAuthCredential, error)
	RecoverStaleRefreshing(timeout time.Duration) (int64, error)
}

// LogStore is the request-audit sink + query surface.
type LogStore interface {
	AppendBatch(entries []RequestLog) error
	Query(q LogQuery) (LogPage, error)
	FindByID(id string) (*RequestLog, error)
	ClearAll() (int64, error)
	DeleteBefore(cutoffMs int64) (int64, error)
	StatsOverview(hours int64) (StatsOverview, error)
	StatsByModel(hours int64) ([]ModelStats, error)
	StatsByProvider(hours int64) ([]ProviderStats, error)
	StatsByApiKey(hours int64) ([]ApiKeyStats, error)
	StatsHourly(hours int64) ([]StatsHourly, error)
}

// Storage is the aggregate persistence interface.
type Storage interface {
	Providers() ProviderStore
	Models() ModelStore
	ModelBackends() ModelBackendStore
	Settings() SettingsStore
	APIKeys() ApiKeyStore
	Auth() AuthAccessStore
	OAuthCredentials() OAuthCredentialStore
	Logs() LogStore
	Bootstrap() Bootstrap
}
