// Package memory is an in-memory storage backend, used for tests and the
// no-DB desktop default. It implements storage.Storage by delegating to
// per-sub-store wrapper types (Go cannot have two List methods with different
// return types on one struct).
package memory

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

// ErrNotFound is returned by Update/Delete when no row matches the id.
var ErrNotFound = errors.New("memory: not found")

// Backend is the in-memory storage backend.
type Backend struct {
	mu        sync.RWMutex
	providers map[string]storage.Provider
	models    map[string]storage.Model
	backends  map[string]storage.ModelBackend
	settings  map[string]string
	apiKeys   map[string]storage.ApiKey
	bindings map[string][]string // apiKeyID → []modelID
}

// New creates an empty in-memory backend.
func New() *Backend {
	return &Backend{
		providers: map[string]storage.Provider{},
		models:    map[string]storage.Model{},
		backends:  map[string]storage.ModelBackend{},
		settings:  map[string]string{},
		apiKeys:   map[string]storage.ApiKey{},
		bindings: map[string][]string{},
	}
}

// Storage returns the backend as a storage.Storage.
func (b *Backend) Storage() storage.Storage { return b }

// storage.Storage composition — each returns a thin sub-store wrapper.
func (b *Backend) Providers() storage.ProviderStore               { return providerStore{b} }
func (b *Backend) Models() storage.ModelStore                     { return modelStore{b} }
func (b *Backend) ModelBackends() storage.ModelBackendStore       { return backendStore{b} }
func (b *Backend) Settings() storage.SettingsStore                { return settingsStore{b} }
func (b *Backend) APIKeys() storage.ApiKeyStore                   { return apiKeyStore{b} }
func (b *Backend) Auth() storage.AuthAccessStore { return authAccessStore{b} }
func (b *Backend) Bootstrap() storage.Bootstrap  { return b }

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

// modelWithTargets attaches the model's backends (sorted by priority then weight).
func (b *Backend) modelWithTargets(m storage.Model) storage.Model {
	out := m
	out.Targets = nil // targets are read fresh from the backends map
	for _, be := range b.backends {
		if be.ModelID == m.ID {
			out.Targets = append(out.Targets, be)
		}
	}
	sort.Slice(out.Targets, func(i, j int) bool {
		if out.Targets[i].Priority != out.Targets[j].Priority {
			return out.Targets[i].Priority < out.Targets[j].Priority
		}
		return out.Targets[i].Weight >= out.Targets[j].Weight
	})
	return out
}

func (b *Backend) replaceBackends(modelID string, targets []storage.CreateModelBackend) []storage.ModelBackend {
	// delete existing
	for id, be := range b.backends {
		if be.ModelID == modelID {
			delete(b.backends, id)
		}
	}
	now := nowISO()
	var out []storage.ModelBackend
	for _, t := range targets {
		be := storage.ModelBackend{
			ID: newID(), ModelID: modelID, ProviderID: t.ProviderID,
			Model: t.Model, Weight: t.Weight, Priority: t.Priority, CreatedAt: now,
		}
		b.backends[be.ID] = be
		out = append(out, be)
	}
	return out
}

// ── providerStore ──

type providerStore struct{ b *Backend }

func (s providerStore) List() ([]storage.Provider, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	out := make([]storage.Provider, 0, len(s.b.providers))
	for _, p := range s.b.providers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s providerStore) Get(id string) (*storage.Provider, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	p, ok := s.b.providers[id]
	if !ok {
		return nil, nil
	}
	return &p, nil
}

func (s providerStore) Create(in storage.CreateProvider) (storage.Provider, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	now := nowISO()
	authMode := in.AuthMode
	if authMode == "" {
		authMode = "apikey"
	}
	p := storage.Provider{
		ID: newID(), Name: in.Name, Vendor: in.Vendor, Protocol: in.Protocol,
		BaseURL: in.BaseURL, PresetKey: in.PresetKey, Channel: in.Channel,
		ModelsSource: in.ModelsSource, StaticModels: in.StaticModels,
		APIKey: in.APIKey, AuthMode: authMode, UseProxy: in.UseProxy,
		IsEnabled: true, CreatedAt: now, UpdatedAt: now,
	}
	s.b.providers[p.ID] = p
	return p, nil
}

func (s providerStore) Update(id string, in storage.UpdateProvider) (storage.Provider, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	p, ok := s.b.providers[id]
	if !ok {
		return storage.Provider{}, ErrNotFound
	}
	applyProviderUpdate(&p, in)
	p.UpdatedAt = nowISO()
	s.b.providers[id] = p
	return p, nil
}

func (s providerStore) Delete(id string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	delete(s.b.providers, id)
	return nil
}

func (s providerStore) ExistsByName(name, excludeID string) (bool, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	for _, p := range s.b.providers {
		if p.Name == name && p.ID != excludeID {
			return true, nil
		}
	}
	return false, nil
}

func (s providerStore) RecordTestResult(providerID string, result storage.ProviderTestResult) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	p, ok := s.b.providers[providerID]
	if !ok {
		return ErrNotFound
	}
	succ := result.Success
	p.LastTestSuccess = &succ
	p.LastTestAt = result.TestedAt
	s.b.providers[providerID] = p
	return nil
}

func applyProviderUpdate(p *storage.Provider, in storage.UpdateProvider) {
	if in.Name != nil {
		p.Name = *in.Name
	}
	if in.Vendor != nil {
		p.Vendor = *in.Vendor
	}
	if in.Protocol != nil {
		p.Protocol = *in.Protocol
	}
	if in.BaseURL != nil {
		p.BaseURL = *in.BaseURL
	}
	if in.PresetKey != nil {
		p.PresetKey = *in.PresetKey
	}
	if in.Channel != nil {
		p.Channel = *in.Channel
	}
	if in.ModelsSource != nil {
		p.ModelsSource = *in.ModelsSource
	}
	if in.StaticModels != nil {
		p.StaticModels = *in.StaticModels
	}
	if in.APIKey != nil {
		p.APIKey = *in.APIKey
	}
	if in.AuthMode != nil {
		p.AuthMode = *in.AuthMode
	}
	if in.UseProxy != nil {
		p.UseProxy = *in.UseProxy
	}
	if in.IsEnabled != nil {
		p.IsEnabled = *in.IsEnabled
	}
}

// ── modelStore ──

type modelStore struct{ b *Backend }

func (s modelStore) List() ([]storage.Model, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	out := make([]storage.Model, 0, len(s.b.models))
	for _, m := range s.b.models {
		out = append(out, s.b.modelWithTargets(m))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s modelStore) Get(id string) (*storage.Model, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	m, ok := s.b.models[id]
	if !ok {
		return nil, nil
	}
	with := s.b.modelWithTargets(m)
	return &with, nil
}

func (s modelStore) ByName(name string) (*storage.Model, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	for _, m := range s.b.models {
		if m.Name == name {
			with := s.b.modelWithTargets(m)
			return &with, nil
		}
	}
	return nil, nil
}

func (s modelStore) Create(in storage.CreateModel) (storage.Model, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	balance, _ := storage.ParseModelBalance(string(in.Balance))
	m := storage.Model{
		ID: newID(), Name: in.Name, Balance: balance,
		EnableAuth: in.EnableAuth, EnablePayload: in.EnablePayload,
		IsEnabled: true, CreatedAt: nowISO(),
	}
	if in.EnablePayload != nil {
		m.EnablePayload = in.EnablePayload
	}
	s.b.models[m.ID] = storage.Model{
		ID: m.ID, Name: m.Name, Balance: m.Balance, EnableAuth: m.EnableAuth,
		EnablePayload: m.EnablePayload, IsEnabled: m.IsEnabled, CreatedAt: m.CreatedAt,
	}
	m.Targets = s.b.replaceBackends(m.ID, in.Targets)
	return m, nil
}

func (s modelStore) Update(id string, in storage.UpdateModel) (storage.Model, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	m, ok := s.b.models[id]
	if !ok {
		return storage.Model{}, ErrNotFound
	}
	if in.Name != nil {
		m.Name = *in.Name
	}
	if in.Balance != nil {
		m.Balance = *in.Balance
	}
	if in.EnableAuth != nil {
		m.EnableAuth = *in.EnableAuth
	}
	if in.EnablePayload != nil {
		m.EnablePayload = in.EnablePayload
	}
	if in.IsEnabled != nil {
		m.IsEnabled = *in.IsEnabled
	}
	s.b.models[id] = m
	if in.Targets != nil {
		m.Targets = s.b.replaceBackends(id, *in.Targets)
	}
	return s.b.modelWithTargets(m), nil
}

func (s modelStore) Delete(id string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	delete(s.b.models, id)
	for bid, be := range s.b.backends {
		if be.ModelID == id {
			delete(s.b.backends, bid)
		}
	}
	return nil
}

func (s modelStore) ExistsByName(name, excludeID string) (bool, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	for _, m := range s.b.models {
		if m.Name == name && m.ID != excludeID {
			return true, nil
		}
	}
	return false, nil
}

// ── backendStore ──

type backendStore struct{ b *Backend }

func (s backendStore) ListByModel(modelID string) ([]storage.ModelBackend, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	var out []storage.ModelBackend
	for _, be := range s.b.backends {
		if be.ModelID == modelID {
			out = append(out, be)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].Weight >= out[j].Weight
	})
	return out, nil
}

func (s backendStore) SetBackends(modelID string, targets []storage.CreateModelBackend) ([]storage.ModelBackend, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	return s.b.replaceBackends(modelID, targets), nil
}

func (s backendStore) DeleteByModel(modelID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	for bid, be := range s.b.backends {
		if be.ModelID == modelID {
			delete(s.b.backends, bid)
		}
	}
	return nil
}

// ── settingsStore ──

type settingsStore struct{ b *Backend }

func (s settingsStore) Get(key string) (string, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	return s.b.settings[key], nil
}

func (s settingsStore) Set(key, value string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	s.b.settings[key] = value
	return nil
}

func (s settingsStore) ListAll() ([]storage.Setting, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	out := make([]storage.Setting, 0, len(s.b.settings))
	for k, v := range s.b.settings {
		out = append(out, storage.Setting{Key: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
