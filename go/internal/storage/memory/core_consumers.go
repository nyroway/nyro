package memory

import (
	"sort"

	"github.com/nyroway/nyro/go/internal/storage"
)

type consumerStore struct{ b *Backend }

// consumerWithDetails attaches c's keys, granted route models, and quotas.
func (b *Backend) consumerWithDetails(c storage.Consumer) storage.Consumer {
	out := c
	out.Keys, out.Routes, out.Quotas = nil, nil, nil

	var keys []storage.ConsumerKey
	for _, k := range b.consumerKeys {
		if k.ConsumerID == c.ID {
			keys = append(keys, k)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].Name < keys[j].Name })
	out.Keys = keys

	for _, g := range b.consumerRoutes {
		if g.ConsumerID == c.ID {
			if r, ok := b.routes[g.RouteID]; ok {
				out.Routes = append(out.Routes, r.Model)
			}
		}
	}

	for _, q := range b.consumerQuotas {
		if q.ConsumerID == c.ID {
			out.Quotas = append(out.Quotas, q)
		}
	}
	return out
}

// createConsumerKey generates (or accepts) a raw token, stores only its
// prefix+hash, and returns the DTO with Token set to the one-time plaintext.
func (b *Backend) createConsumerKey(consumerID string, in storage.CreateConsumerKey) (storage.ConsumerKey, error) {
	now := nowISO()
	raw := in.Token
	var prefix, hash string
	if raw == "" {
		var err error
		raw, prefix, hash, err = storage.GenerateKey()
		if err != nil {
			return storage.ConsumerKey{}, err
		}
	} else {
		prefix = storage.PrefixOf(raw)
		hash = storage.HashKey(raw)
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	k := storage.ConsumerKey{
		ID: newID(), ConsumerID: consumerID, Name: in.Name, KeyPrefix: prefix, KeyHash: hash,
		Enabled: enabled, ExpiresAt: in.ExpiresAt, CreatedAt: now, UpdatedAt: now,
	}
	b.consumerKeys[k.ID] = k
	k.Token = raw
	return k, nil
}

func (s consumerStore) List() ([]storage.Consumer, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	out := make([]storage.Consumer, 0, len(s.b.consumers))
	for _, c := range s.b.consumers {
		out = append(out, s.b.consumerWithDetails(c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s consumerStore) Get(id string) (*storage.Consumer, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	c, ok := s.b.consumers[id]
	if !ok {
		return nil, nil
	}
	out := s.b.consumerWithDetails(c)
	return &out, nil
}

func (s consumerStore) ByName(name string) (*storage.Consumer, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	for _, c := range s.b.consumers {
		if c.Name == name {
			out := s.b.consumerWithDetails(c)
			return &out, nil
		}
	}
	return nil, nil
}

func (s consumerStore) Create(in storage.CreateConsumer) (storage.Consumer, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	now := nowISO()
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	c := storage.Consumer{ID: newID(), Name: in.Name, Enabled: enabled, CreatedAt: now, UpdatedAt: now}
	s.b.consumers[c.ID] = c

	// Collect the created keys directly (with their one-time raw Token);
	// consumerWithDetails below reads from the map, which never stores Token.
	createdKeys := make([]storage.ConsumerKey, 0, len(in.Keys))
	for _, k := range in.Keys {
		ck, err := s.b.createConsumerKey(c.ID, k)
		if err != nil {
			return storage.Consumer{}, err
		}
		createdKeys = append(createdKeys, ck)
	}
	for _, routeModel := range in.Routes {
		for _, r := range s.b.routes {
			if r.Model == routeModel {
				gid := newID()
				s.b.consumerRoutes[gid] = consumerRouteGrant{ConsumerID: c.ID, RouteID: r.ID}
				break
			}
		}
	}
	for _, qin := range in.Quotas {
		cq := storage.ConsumerQuota{
			ID: newID(), ConsumerID: c.ID, QuotaType: qin.QuotaType,
			QuotaLimit: qin.QuotaLimit, Window: qin.Window,
		}
		s.b.consumerQuotas[cq.ID] = cq
	}

	out := s.b.consumerWithDetails(c)
	out.Keys = createdKeys
	return out, nil
}

func (s consumerStore) Update(id string, in storage.UpdateConsumer) (storage.Consumer, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	c, ok := s.b.consumers[id]
	if !ok {
		return storage.Consumer{}, ErrNotFound
	}
	if in.Name != nil {
		c.Name = *in.Name
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	c.UpdatedAt = nowISO()
	s.b.consumers[id] = c
	return s.b.consumerWithDetails(c), nil
}

func (s consumerStore) Delete(id string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	delete(s.b.consumers, id)
	for kid, k := range s.b.consumerKeys {
		if k.ConsumerID == id {
			delete(s.b.consumerKeys, kid)
		}
	}
	for gid, g := range s.b.consumerRoutes {
		if g.ConsumerID == id {
			delete(s.b.consumerRoutes, gid)
		}
	}
	for qid, q := range s.b.consumerQuotas {
		if q.ConsumerID == id {
			delete(s.b.consumerQuotas, qid)
		}
	}
	return nil
}
