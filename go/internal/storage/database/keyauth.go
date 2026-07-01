package database

import (
	"context"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/query"
)

type keyAuthStore struct{ q *query.Query }

// FindKey narrows candidates by KeyPrefix (indexed), then compares SHA-256
// hashes to find the exact match — raw tokens are never persisted.
func (s keyAuthStore) FindKey(rawKey string) (*storage.ConsumerKeyAccessRecord, error) {
	ctx := context.Background()
	prefix := storage.PrefixOf(rawKey)
	hash := storage.HashKey(rawKey)

	candidates, err := s.q.ConsumerKey.WithContext(ctx).Where(s.q.ConsumerKey.KeyPrefix.Eq(prefix)).Find()
	if err != nil {
		return nil, err
	}
	var matched *storage.ConsumerKey
	for _, c := range candidates {
		if c.KeyHash == hash {
			ck := consumerKeyFromModel(c)
			matched = &ck
			break
		}
	}
	if matched == nil {
		return nil, nil
	}

	rec := &storage.ConsumerKeyAccessRecord{
		KeyID:      matched.ID,
		ConsumerID: matched.ConsumerID,
		KeyPrefix:  matched.KeyPrefix,
		Enabled:    matched.Enabled,
		ExpiresAt:  matched.ExpiresAt,
	}

	grants, err := s.q.ConsumerRoute.WithContext(ctx).Where(s.q.ConsumerRoute.ConsumerID.Eq(matched.ConsumerID)).Find()
	if err != nil {
		return nil, err
	}
	for _, g := range grants {
		route, err := s.q.Route.WithContext(ctx).Where(s.q.Route.ID.Eq(g.RouteID)).First()
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		rec.Routes = append(rec.Routes, route.Model)
	}

	quotas, err := s.q.ConsumerQuota.WithContext(ctx).Where(s.q.ConsumerQuota.ConsumerID.Eq(matched.ConsumerID)).Find()
	if err != nil {
		return nil, err
	}
	for _, qt := range quotas {
		rec.Quotas = append(rec.Quotas, consumerQuotaFromModel(qt))
	}

	return rec, nil
}
