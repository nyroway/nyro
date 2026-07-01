package memory

import "github.com/nyroway/nyro/go/internal/storage"

type keyAuthStore struct{ b *Backend }

// FindKey narrows candidates by KeyPrefix, then compares SHA-256 hashes — raw
// tokens are never persisted.
func (s keyAuthStore) FindKey(rawKey string) (*storage.ConsumerKeyAccessRecord, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()

	prefix := storage.PrefixOf(rawKey)
	hash := storage.HashKey(rawKey)

	var matched *storage.ConsumerKey
	for _, k := range s.b.consumerKeys {
		if k.KeyPrefix == prefix && k.KeyHash == hash {
			m := k
			matched = &m
			break
		}
	}
	if matched == nil {
		return nil, nil
	}

	// A key is only usable when both it and its owning consumer are enabled —
	// disabling a consumer must revoke every key it owns, not just the ones
	// individually toggled off.
	consumerEnabled := true
	if c, ok := s.b.consumers[matched.ConsumerID]; ok {
		consumerEnabled = c.Enabled
	}

	rec := &storage.ConsumerKeyAccessRecord{
		KeyID:      matched.ID,
		ConsumerID: matched.ConsumerID,
		KeyPrefix:  matched.KeyPrefix,
		Enabled:    matched.Enabled && consumerEnabled,
		ExpiresAt:  matched.ExpiresAt,
	}
	for _, g := range s.b.consumerRoutes {
		if g.ConsumerID == matched.ConsumerID {
			if r, ok := s.b.routes[g.RouteID]; ok {
				rec.Routes = append(rec.Routes, r.Model)
			}
		}
	}
	for _, q := range s.b.consumerQuotas {
		if q.ConsumerID == matched.ConsumerID {
			rec.Quotas = append(rec.Quotas, q)
		}
	}
	return rec, nil
}
