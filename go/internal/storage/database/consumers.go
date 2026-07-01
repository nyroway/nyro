package database

import (
	"context"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/model"
	"github.com/nyroway/nyro/go/internal/storage/query"
)

type consumerStore struct{ q *query.Query }

func (s consumerStore) loadDetails(ctx context.Context, tx *query.Query, c *model.Consumer) (storage.Consumer, error) {
	out := consumerFromModel(c)

	keys, err := tx.ConsumerKey.WithContext(ctx).Where(tx.ConsumerKey.ConsumerID.Eq(c.ID)).Order(tx.ConsumerKey.Name).Find()
	if err != nil {
		return storage.Consumer{}, err
	}
	for _, k := range keys {
		out.Keys = append(out.Keys, consumerKeyFromModel(k))
	}

	grants, err := tx.ConsumerRoute.WithContext(ctx).Where(tx.ConsumerRoute.ConsumerID.Eq(c.ID)).Find()
	if err != nil {
		return storage.Consumer{}, err
	}
	for _, g := range grants {
		route, err := tx.Route.WithContext(ctx).Where(tx.Route.ID.Eq(g.RouteID)).First()
		if err != nil {
			if isNotFound(err) {
				continue
			}
			return storage.Consumer{}, err
		}
		out.Routes = append(out.Routes, route.Model)
	}

	quotas, err := tx.ConsumerQuota.WithContext(ctx).Where(tx.ConsumerQuota.ConsumerID.Eq(c.ID)).Find()
	if err != nil {
		return storage.Consumer{}, err
	}
	for _, qt := range quotas {
		out.Quotas = append(out.Quotas, consumerQuotaFromModel(qt))
	}

	return out, nil
}

func (s consumerStore) List() ([]storage.Consumer, error) {
	ctx := context.Background()
	rows, err := s.q.Consumer.WithContext(ctx).Order(s.q.Consumer.Name).Find()
	if err != nil {
		return nil, err
	}
	out := make([]storage.Consumer, 0, len(rows))
	for _, c := range rows {
		withDetails, err := s.loadDetails(ctx, s.q, c)
		if err != nil {
			return nil, err
		}
		out = append(out, withDetails)
	}
	return out, nil
}

func (s consumerStore) Get(id string) (*storage.Consumer, error) {
	ctx := context.Background()
	c, err := s.q.Consumer.WithContext(ctx).Where(s.q.Consumer.ID.Eq(id)).First()
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	out, err := s.loadDetails(ctx, s.q, c)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s consumerStore) ByName(name string) (*storage.Consumer, error) {
	ctx := context.Background()
	c, err := s.q.Consumer.WithContext(ctx).Where(s.q.Consumer.Name.Eq(name)).First()
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	out, err := s.loadDetails(ctx, s.q, c)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s consumerStore) Create(in storage.CreateConsumer) (storage.Consumer, error) {
	ctx := context.Background()
	now := nowISO()
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	c := &model.Consumer{
		ID:        newID(),
		Name:      in.Name,
		Enabled:   enabled,
		CreatedAt: now,
		UpdatedAt: now,
	}
	var out storage.Consumer
	err := s.q.Transaction(func(tx *query.Query) error {
		if err := tx.Consumer.WithContext(ctx).Create(c); err != nil {
			return err
		}

		// Collect the created keys directly (with their one-time raw Token);
		// re-reading via loadDetails below would return them without Token,
		// since raw tokens are never persisted.
		createdKeys := make([]storage.ConsumerKey, 0, len(in.Keys))
		for _, k := range in.Keys {
			ck, err := createConsumerKey(ctx, tx, c.ID, k)
			if err != nil {
				return err
			}
			createdKeys = append(createdKeys, ck)
		}

		for _, routeModel := range in.Routes {
			route, err := tx.Route.WithContext(ctx).Where(tx.Route.Model.Eq(routeModel)).First()
			if err != nil {
				return err
			}
			if err := tx.ConsumerRoute.WithContext(ctx).Create(&model.ConsumerRoute{ConsumerID: c.ID, RouteID: route.ID}); err != nil {
				return err
			}
		}

		for _, qin := range in.Quotas {
			cq := &model.ConsumerQuota{
				ID:         newID(),
				ConsumerID: c.ID,
				QuotaType:  qin.QuotaType,
				QuotaLimit: qin.QuotaLimit,
				Window:     qin.Window,
				CreatedAt:  now,
				UpdatedAt:  now,
			}
			if err := tx.ConsumerQuota.WithContext(ctx).Create(cq); err != nil {
				return err
			}
		}

		details, err := s.loadDetails(ctx, tx, c)
		if err != nil {
			return err
		}
		details.Keys = createdKeys
		out = details
		return nil
	})
	return out, err
}

// createConsumerKey generates (or accepts) a raw token, persists only its
// prefix+hash, and returns the DTO with Token populated (the one-time plaintext
// exposure at creation).
func createConsumerKey(ctx context.Context, tx *query.Query, consumerID string, in storage.CreateConsumerKey) (storage.ConsumerKey, error) {
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
	k := &model.ConsumerKey{
		ID:         newID(),
		ConsumerID: consumerID,
		Name:       in.Name,
		KeyPrefix:  prefix,
		KeyHash:    hash,
		Enabled:    enabled,
		ExpiresAt:  in.ExpiresAt,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := tx.ConsumerKey.WithContext(ctx).Create(k); err != nil {
		return storage.ConsumerKey{}, err
	}
	out := consumerKeyFromModel(k)
	out.Token = raw
	return out, nil
}

func (s consumerStore) Update(id string, in storage.UpdateConsumer) (storage.Consumer, error) {
	ctx := context.Background()
	var out storage.Consumer
	err := s.q.Transaction(func(tx *query.Query) error {
		c, err := tx.Consumer.WithContext(ctx).Where(tx.Consumer.ID.Eq(id)).First()
		if err != nil {
			return err
		}
		if in.Name != nil {
			c.Name = *in.Name
		}
		if in.Enabled != nil {
			c.Enabled = *in.Enabled
		}
		c.UpdatedAt = nowISO()
		if err := tx.Consumer.WithContext(ctx).Save(c); err != nil {
			return err
		}
		details, err := s.loadDetails(ctx, tx, c)
		if err != nil {
			return err
		}
		out = details
		return nil
	})
	return out, err
}

func (s consumerStore) Delete(id string) error {
	ctx := context.Background()
	return s.q.Transaction(func(tx *query.Query) error {
		if _, err := tx.ConsumerKey.WithContext(ctx).Where(tx.ConsumerKey.ConsumerID.Eq(id)).Delete(); err != nil {
			return err
		}
		if _, err := tx.ConsumerRoute.WithContext(ctx).Where(tx.ConsumerRoute.ConsumerID.Eq(id)).Delete(); err != nil {
			return err
		}
		if _, err := tx.ConsumerQuota.WithContext(ctx).Where(tx.ConsumerQuota.ConsumerID.Eq(id)).Delete(); err != nil {
			return err
		}
		_, err := tx.Consumer.WithContext(ctx).Where(tx.Consumer.ID.Eq(id)).Delete()
		return err
	})
}
