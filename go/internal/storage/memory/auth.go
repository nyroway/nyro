package memory

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

func newToken() string {
	var buf [24]byte
	_, _ = rand.Read(buf[:])
	return "nyro_" + hex.EncodeToString(buf[:])
}

// ── apiKeyStore ──

type apiKeyStore struct{ b *Backend }

func (s apiKeyStore) List() ([]storage.ApiKeyWithBindings, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	out := make([]storage.ApiKeyWithBindings, 0, len(s.b.apiKeys))
	for _, k := range s.b.apiKeys {
		out = append(out, s.withBindings(k))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s apiKeyStore) Get(id string) (*storage.ApiKeyWithBindings, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	k, ok := s.b.apiKeys[id]
	if !ok {
		return nil, nil
	}
	wb := s.withBindings(k)
	return &wb, nil
}

func (s apiKeyStore) Create(in storage.CreateApiKey) (storage.ApiKeyWithBindings, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	now := nowISO()
	token := in.Token
	if token == "" {
		token = newToken()
	}
	k := storage.ApiKey{
		ID: newID(), Token: token, Name: in.Name,
		RPM: in.RPM, RPD: in.RPD, TPM: in.TPM, TPD: in.TPD,
		IsEnabled: true, ExpiresAt: in.ExpiresAt, CreatedAt: now, UpdatedAt: now,
	}
	s.b.apiKeys[k.ID] = k
	if len(in.ModelIDs) > 0 {
		s.b.bindings[k.ID] = append([]string(nil), in.ModelIDs...)
	}
	return s.withBindings(k), nil
}

func (s apiKeyStore) Update(id string, in storage.UpdateApiKey) (storage.ApiKeyWithBindings, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	k, ok := s.b.apiKeys[id]
	if !ok {
		return storage.ApiKeyWithBindings{}, ErrNotFound
	}
	if in.Name != nil {
		k.Name = *in.Name
	}
	if in.RPM != nil {
		k.RPM = in.RPM
	}
	if in.RPD != nil {
		k.RPD = in.RPD
	}
	if in.TPM != nil {
		k.TPM = in.TPM
	}
	if in.TPD != nil {
		k.TPD = in.TPD
	}
	if in.IsEnabled != nil {
		k.IsEnabled = *in.IsEnabled
	}
	if in.ExpiresAt != nil {
		k.ExpiresAt = *in.ExpiresAt
	}
	k.UpdatedAt = nowISO()
	s.b.apiKeys[id] = k
	if in.ModelIDs != nil {
		s.b.bindings[id] = append([]string(nil), *in.ModelIDs...)
	}
	return s.withBindings(k), nil
}

func (s apiKeyStore) Delete(id string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	delete(s.b.apiKeys, id)
	delete(s.b.bindings, id)
	return nil
}

func (s apiKeyStore) ExistsByName(name, excludeID string) (bool, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	for _, k := range s.b.apiKeys {
		if k.Name == name && k.ID != excludeID {
			return true, nil
		}
	}
	return false, nil
}

func (s apiKeyStore) withBindings(k storage.ApiKey) storage.ApiKeyWithBindings {
	return storage.ApiKeyWithBindings{ApiKey: k, ModelIDs: append([]string(nil), s.b.bindings[k.ID]...)}
}

// ── authAccessStore ──

type authAccessStore struct{ b *Backend }

func (s authAccessStore) FindAPIKey(rawKey string) (*storage.ApiKeyAccessRecord, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	for _, k := range s.b.apiKeys {
		if k.Token == rawKey {
			return &storage.ApiKeyAccessRecord{
				ID: k.ID, Name: k.Name, IsEnabled: k.IsEnabled, ExpiresAt: k.ExpiresAt,
				RPM: k.RPM, RPD: k.RPD, TPM: k.TPM, TPD: k.TPD,
			}, nil
		}
	}
	return nil, nil
}

func (s authAccessStore) ModelBindingExists(apiKeyID, modelID string) (bool, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	for _, mid := range s.b.bindings[apiKeyID] {
		if mid == modelID {
			return true, nil
		}
	}
	return false, nil
}

func (s authAccessStore) ListBoundModelIDs(apiKeyID string) ([]string, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	return append([]string(nil), s.b.bindings[apiKeyID]...), nil
}

// ── oauthStore ──

type oauthStore struct{ b *Backend }

func (s oauthStore) Get(providerID string) (*storage.OAuthCredential, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	c, ok := s.b.oauth[providerID]
	if !ok {
		return nil, nil
	}
	return &c, nil
}

func (s oauthStore) ListAll() ([]storage.OAuthCredential, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	out := make([]storage.OAuthCredential, 0, len(s.b.oauth))
	for _, c := range s.b.oauth {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProviderID < out[j].ProviderID })
	return out, nil
}

func (s oauthStore) Upsert(providerID string, in storage.UpsertOAuthCredential) (storage.OAuthCredential, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	now := nowISO()
	existing, ok := s.b.oauth[providerID]
	c := storage.OAuthCredential{
		ProviderID: providerID, DriverKey: in.DriverKey, Scheme: in.Scheme,
		AccessToken: in.AccessToken, RefreshToken: in.RefreshToken, ExpiresAt: in.ExpiresAt,
		ResourceURL: in.ResourceURL, SubjectID: in.SubjectID, Scopes: in.Scopes, Meta: in.Meta,
		Status: "connected", LastRefreshAt: now,
	}
	if ok {
		c.StatusVersion = existing.StatusVersion
		c.CreatedAt = existing.CreatedAt
	} else {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	s.b.oauth[providerID] = c
	return c, nil
}

func (s oauthStore) Delete(providerID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	delete(s.b.oauth, providerID)
	return nil
}

func (s oauthStore) TryBeginRefresh(providerID string, expectedVersion int32) (*storage.OAuthCredential, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	c, ok := s.b.oauth[providerID]
	if !ok {
		return nil, nil
	}
	if c.StatusVersion != expectedVersion {
		return nil, nil // CAS mismatch — another refresh already started
	}
	c.Status = "refreshing"
	c.StatusVersion = expectedVersion + 1
	c.UpdatedAt = nowISO()
	s.b.oauth[providerID] = c
	return &c, nil
}

func (s oauthStore) CompleteRefresh(providerID string, in storage.UpsertOAuthCredential) (storage.OAuthCredential, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	c, ok := s.b.oauth[providerID]
	if !ok {
		return storage.OAuthCredential{}, ErrNotFound
	}
	c.AccessToken = in.AccessToken
	c.RefreshToken = in.RefreshToken
	c.ExpiresAt = in.ExpiresAt
	c.Status = "connected"
	c.LastError = ""
	c.LastRefreshAt = nowISO()
	c.UpdatedAt = c.LastRefreshAt
	s.b.oauth[providerID] = c
	return c, nil
}

func (s oauthStore) FailRefresh(providerID, errorMessage string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	c, ok := s.b.oauth[providerID]
	if !ok {
		return ErrNotFound
	}
	c.Status = "error"
	c.LastError = errorMessage
	c.UpdatedAt = nowISO()
	s.b.oauth[providerID] = c
	return nil
}

func (s oauthStore) ListExpiring(before time.Duration) ([]storage.OAuthCredential, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	cutoff := time.Now().Add(before)
	var out []storage.OAuthCredential
	for _, c := range s.b.oauth {
		if c.ExpiresAt != "" {
			if t, err := time.Parse(time.RFC3339, c.ExpiresAt); err == nil && t.Before(cutoff) {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

func (s oauthStore) RecoverStaleRefreshing(timeout time.Duration) (int64, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	cutoff := time.Now().Add(-timeout)
	var n int64
	for k, c := range s.b.oauth {
		if c.Status == "refreshing" {
			if t, err := time.Parse(time.RFC3339, c.UpdatedAt); err == nil && t.Before(cutoff) {
				c.Status = "error"
				c.LastError = "stale refresh recovery"
				c.UpdatedAt = nowISO()
				s.b.oauth[k] = c
				n++
			}
		}
	}
	return n, nil
}
