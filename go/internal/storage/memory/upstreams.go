package memory

import (
	"sort"

	"github.com/nyroway/nyro/go/internal/storage"
)

type upstreamStore struct{ b *Backend }

func (s upstreamStore) List() ([]storage.Upstream, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	out := make([]storage.Upstream, 0, len(s.b.upstreams))
	for _, u := range s.b.upstreams {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s upstreamStore) Get(id string) (*storage.Upstream, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	u, ok := s.b.upstreams[id]
	if !ok {
		return nil, nil
	}
	return &u, nil
}

func (s upstreamStore) Create(in storage.CreateUpstream) (storage.Upstream, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	now := nowISO()
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	u := storage.Upstream{
		ID: newID(), Name: in.Name, Protocol: in.Protocol,
		BaseURL: in.BaseURL, CredentialsJSON: in.CredentialsJSON, ModelsJSON: in.ModelsJSON,
		ProxyURL: in.ProxyURL, Enabled: enabled, CreatedAt: now, UpdatedAt: now,
	}
	s.b.upstreams[u.ID] = u
	return u, nil
}

func (s upstreamStore) Update(id string, in storage.UpdateUpstream) (storage.Upstream, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	u, ok := s.b.upstreams[id]
	if !ok {
		return storage.Upstream{}, ErrNotFound
	}
	if in.Name != nil {
		u.Name = *in.Name
	}
	if in.Protocol != nil {
		u.Protocol = *in.Protocol
	}
	if in.BaseURL != nil {
		u.BaseURL = *in.BaseURL
	}
	if in.CredentialsJSON != nil {
		u.CredentialsJSON = *in.CredentialsJSON
	}
	if in.ModelsJSON != nil {
		u.ModelsJSON = *in.ModelsJSON
	}
	if in.ProxyURL != nil {
		u.ProxyURL = *in.ProxyURL
	}
	if in.Enabled != nil {
		u.Enabled = *in.Enabled
	}
	u.UpdatedAt = nowISO()
	s.b.upstreams[id] = u
	return u, nil
}

func (s upstreamStore) Delete(id string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	delete(s.b.upstreams, id)
	return nil
}

func (s upstreamStore) ExistsByName(name, excludeID string) (bool, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	for _, u := range s.b.upstreams {
		if u.Name == name && u.ID != excludeID {
			return true, nil
		}
	}
	return false, nil
}
