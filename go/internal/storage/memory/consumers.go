package memory

import (
	"fmt"
	"sort"
	"strings"

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
	var preview, hash string
	if raw == "" {
		var err error
		raw, preview, hash, err = storage.GenerateKey()
		if err != nil {
			return storage.ConsumerKey{}, err
		}
	} else {
		preview = storage.PreviewOf(raw)
		hash = storage.HashKey(raw)
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	k := storage.ConsumerKey{
		ID: newID(), ConsumerID: consumerID, Name: in.Name, KeyPreview: preview, KeyHash: hash,
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
	c := storage.Consumer{
		ID: newID(), Name: in.Name, Enabled: enabled,
		Metadata: in.Metadata, Protocols: in.Protocols, IPAllowlist: in.IPAllowlist, Limits: in.Limits,
		CreatedAt: now, UpdatedAt: now,
	}
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
	routeIDs, err := s.b.resolveRouteIDsByModel(in.Routes)
	if err != nil {
		return storage.Consumer{}, err
	}
	for _, rid := range routeIDs {
		gid := newID()
		s.b.consumerRoutes[gid] = consumerRouteGrant{ConsumerID: c.ID, RouteID: rid}
	}

	for _, qin := range in.Quotas {
		if err := storage.ValidateConsumerQuota(qin); err != nil {
			return storage.Consumer{}, err
		}
	}
	s.b.insertConsumerQuotas(c.ID, in.Quotas)

	out := s.b.consumerWithDetails(c)
	out.Keys = createdKeys
	return out, nil
}

// resolveRouteIDsByModel looks up route IDs for a set of route model names,
// mirroring the by-model matching the database backend performs. It returns a
// single error listing every unknown model name rather than silently skipping
// them, so behavior matches the database backend.
func (b *Backend) resolveRouteIDsByModel(models []string) ([]string, error) {
	ids := make([]string, 0, len(models))
	var unknown []string
	for _, m := range models {
		found := false
		for _, r := range b.routes {
			if r.Model == m {
				ids = append(ids, r.ID)
				found = true
				break
			}
		}
		if !found {
			unknown = append(unknown, m)
		}
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown route model(s): %s", strings.Join(unknown, ", "))
	}
	return ids, nil
}

// insertConsumerQuotas creates the in-memory quota rows for consumerID.
// Callers are responsible for validating quotas beforehand.
func (b *Backend) insertConsumerQuotas(consumerID string, quotas []storage.CreateConsumerQuota) {
	for _, qin := range quotas {
		cq := storage.ConsumerQuota{
			ID: newID(), ConsumerID: consumerID, QuotaType: qin.QuotaType,
			QuotaLimit: qin.QuotaLimit, Window: qin.Window, Currency: qin.Currency,
		}
		b.consumerQuotas[cq.ID] = cq
	}
}

// validateConsumerQuotas validates each quota without mutating any state.
func validateConsumerQuotas(quotas []storage.CreateConsumerQuota) error {
	for _, q := range quotas {
		if err := storage.ValidateConsumerQuota(q); err != nil {
			return err
		}
	}
	return nil
}

// applyConsumerQuotas wholesale-replaces consumerID's quotas. Callers must
// validate quotas beforehand (see validateConsumerQuotas); this method never
// fails.
func (b *Backend) applyConsumerQuotas(consumerID string, quotas []storage.CreateConsumerQuota) {
	for qid, q := range b.consumerQuotas {
		if q.ConsumerID == consumerID {
			delete(b.consumerQuotas, qid)
		}
	}
	b.insertConsumerQuotas(consumerID, quotas)
}

// applyConsumerRoutes wholesale-replaces consumerID's route grants with the
// given (already-resolved) route IDs. Callers must resolve routeModels
// beforehand (see resolveRouteIDsByModel); this method never fails.
func (b *Backend) applyConsumerRoutes(consumerID string, routeIDs []string) {
	for gid, g := range b.consumerRoutes {
		if g.ConsumerID == consumerID {
			delete(b.consumerRoutes, gid)
		}
	}
	for _, rid := range routeIDs {
		gid := newID()
		b.consumerRoutes[gid] = consumerRouteGrant{ConsumerID: consumerID, RouteID: rid}
	}
}

func (s consumerStore) Update(id string, in storage.UpdateConsumer) (storage.Consumer, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	c, ok := s.b.consumers[id]
	if !ok {
		return storage.Consumer{}, ErrNotFound
	}

	// Validate/resolve both dimensions before mutating anything, so a
	// failure in one (e.g. an unknown route model) can never leave the
	// other partially replaced.
	if in.Quotas != nil {
		if err := validateConsumerQuotas(*in.Quotas); err != nil {
			return storage.Consumer{}, err
		}
	}
	var routeIDs []string
	if in.Routes != nil {
		ids, err := s.b.resolveRouteIDsByModel(*in.Routes)
		if err != nil {
			return storage.Consumer{}, err
		}
		routeIDs = ids
	}

	if in.Name != nil {
		c.Name = *in.Name
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	if in.Metadata != nil {
		c.Metadata = *in.Metadata
	}
	if in.Protocols != nil {
		c.Protocols = *in.Protocols
	}
	if in.IPAllowlist != nil {
		c.IPAllowlist = *in.IPAllowlist
	}
	if in.Limits != nil {
		c.Limits = in.Limits
	}
	c.UpdatedAt = nowISO()
	s.b.consumers[id] = c

	if in.Quotas != nil {
		s.b.applyConsumerQuotas(id, *in.Quotas)
	}
	if in.Routes != nil {
		s.b.applyConsumerRoutes(id, routeIDs)
	}

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

// AddKey creates a new key for consumerID, returning it with the one-time
// raw Token populated. Mirrors the database backend's not-found check on the
// owning consumer before delegating to the shared key-creation helper.
func (s consumerStore) AddKey(consumerID string, in storage.CreateConsumerKey) (storage.ConsumerKey, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if _, ok := s.b.consumers[consumerID]; !ok {
		return storage.ConsumerKey{}, ErrNotFound
	}
	return s.b.createConsumerKey(consumerID, in)
}

// UpdateKey partially updates a single key by its own ID. The returned
// ConsumerKey never carries a Token (raw tokens are only exposed at creation).
func (s consumerStore) UpdateKey(keyID string, in storage.UpdateConsumerKey) (storage.ConsumerKey, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	k, ok := s.b.consumerKeys[keyID]
	if !ok {
		return storage.ConsumerKey{}, ErrNotFound
	}
	if in.Name != nil {
		k.Name = *in.Name
	}
	if in.Enabled != nil {
		k.Enabled = *in.Enabled
	}
	if in.ExpiresAt != nil {
		k.ExpiresAt = *in.ExpiresAt
	}
	k.UpdatedAt = nowISO()
	k.Token = ""
	s.b.consumerKeys[keyID] = k
	return k, nil
}

// DeleteKey deletes a single key by its own ID, returning ErrNotFound if no
// such key exists.
func (s consumerStore) DeleteKey(keyID string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	if _, ok := s.b.consumerKeys[keyID]; !ok {
		return ErrNotFound
	}
	delete(s.b.consumerKeys, keyID)
	return nil
}
