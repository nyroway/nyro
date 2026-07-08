package database

import (
	"context"
	"fmt"
	"strings"

	"gorm.io/gen/field"

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
		ID:              newID(),
		Name:            in.Name,
		Enabled:         enabled,
		MetadataJSON:    stringMapToJSON(in.Metadata),
		ProtocolsJSON:   stringSliceToJSON(in.Protocols),
		IPAllowlistJSON: stringSliceToJSON(in.IPAllowlist),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if in.Limits != nil {
		c.MaxInputTokens = in.Limits.MaxInputTokens
		c.MaxOutputTokens = in.Limits.MaxOutputTokens
		c.MaxRequestBodyBytes = in.Limits.MaxRequestBodyBytes
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

		routeIDs, err := resolveRouteIDsByModel(ctx, tx, in.Routes)
		if err != nil {
			return err
		}
		for _, routeID := range routeIDs {
			if err := tx.ConsumerRoute.WithContext(ctx).Create(&model.ConsumerRoute{ConsumerID: c.ID, RouteID: routeID}); err != nil {
				return err
			}
		}

		for _, qin := range in.Quotas {
			if err := storage.ValidateConsumerQuota(qin); err != nil {
				return err
			}
		}
		if err := insertConsumerQuotas(ctx, tx, c.ID, in.Quotas); err != nil {
			return err
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
	k := &model.ConsumerKey{
		ID:         newID(),
		ConsumerID: consumerID,
		Name:       in.Name,
		KeyPreview: preview,
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

// resolveRouteIDsByModel looks up route IDs for a set of route model names,
// mirroring the by-model matching Create already performs. It returns a single
// error listing every unknown model name, rather than failing on the first
// miss, so callers get a complete picture in one round trip.
func resolveRouteIDsByModel(ctx context.Context, tx *query.Query, models []string) ([]string, error) {
	ids := make([]string, 0, len(models))
	var unknown []string
	for _, m := range models {
		route, err := tx.Route.WithContext(ctx).Where(tx.Route.Model.Eq(m)).First()
		if err != nil {
			if isNotFound(err) {
				unknown = append(unknown, m)
				continue
			}
			return nil, err
		}
		ids = append(ids, route.ID)
	}
	if len(unknown) > 0 {
		return nil, fmt.Errorf("unknown route model(s): %s", strings.Join(unknown, ", "))
	}
	return ids, nil
}

// insertConsumerQuotas creates the consumer_quotas rows for consumerID inside
// an existing transaction. Callers are responsible for validating quotas
// beforehand.
func insertConsumerQuotas(ctx context.Context, tx *query.Query, consumerID string, quotas []storage.CreateConsumerQuota) error {
	now := nowISO()
	for _, qin := range quotas {
		cq := &model.ConsumerQuota{
			ID:         newID(),
			ConsumerID: consumerID,
			QuotaType:  qin.QuotaType,
			QuotaLimit: qin.QuotaLimit,
			Window:     qin.Window,
			Currency:   qin.Currency,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if err := tx.ConsumerQuota.WithContext(ctx).Create(cq); err != nil {
			return err
		}
	}
	return nil
}

// replaceConsumerQuotas validates, then wholesale-replaces consumerID's
// quotas: it deletes the existing rows and recreates them from quotas.
func replaceConsumerQuotas(ctx context.Context, tx *query.Query, consumerID string, quotas []storage.CreateConsumerQuota) error {
	for _, q := range quotas {
		if err := storage.ValidateConsumerQuota(q); err != nil {
			return err
		}
	}
	if _, err := tx.ConsumerQuota.WithContext(ctx).Where(tx.ConsumerQuota.ConsumerID.Eq(consumerID)).Delete(); err != nil {
		return err
	}
	return insertConsumerQuotas(ctx, tx, consumerID, quotas)
}

// replaceConsumerRoutes resolves routeModels to route IDs, then wholesale-
// replaces consumerID's route grants: it deletes the existing rows and
// recreates them from the resolved IDs.
func replaceConsumerRoutes(ctx context.Context, tx *query.Query, consumerID string, routeModels []string) error {
	ids, err := resolveRouteIDsByModel(ctx, tx, routeModels)
	if err != nil {
		return err
	}
	if _, err := tx.ConsumerRoute.WithContext(ctx).Where(tx.ConsumerRoute.ConsumerID.Eq(consumerID)).Delete(); err != nil {
		return err
	}
	for _, routeID := range ids {
		if err := tx.ConsumerRoute.WithContext(ctx).Create(&model.ConsumerRoute{ConsumerID: consumerID, RouteID: routeID}); err != nil {
			return err
		}
	}
	return nil
}

// Update partially updates a consumer's own row (Name/Enabled), plus any
// wholesale-replaced Quotas/Routes.
//
// The row-level write uses UpdateSimple with explicit column assignments
// rather than Save(model) on a mutated struct: Enabled's `default:true` gorm
// tag makes Save skip writing the column when it holds its Go zero value
// (false), so a struct-level Save can silently fail to persist a disable
// (see UpdateKey below, which hit the identical issue for ConsumerKey).
func (s consumerStore) Update(id string, in storage.UpdateConsumer) (storage.Consumer, error) {
	ctx := context.Background()
	var out storage.Consumer
	err := s.q.Transaction(func(tx *query.Query) error {
		if _, err := tx.Consumer.WithContext(ctx).Where(tx.Consumer.ID.Eq(id)).First(); err != nil {
			return err
		}
		assigns := []field.AssignExpr{tx.Consumer.UpdatedAt.Value(nowISO())}
		if in.Name != nil {
			assigns = append(assigns, tx.Consumer.Name.Value(*in.Name))
		}
		if in.Enabled != nil {
			assigns = append(assigns, tx.Consumer.Enabled.Value(*in.Enabled))
		}
		if in.Metadata != nil {
			assigns = append(assigns, tx.Consumer.MetadataJSON.Value(stringMapToJSON(*in.Metadata)))
		}
		if in.Protocols != nil {
			assigns = append(assigns, tx.Consumer.ProtocolsJSON.Value(stringSliceToJSON(*in.Protocols)))
		}
		if in.IPAllowlist != nil {
			assigns = append(assigns, tx.Consumer.IPAllowlistJSON.Value(stringSliceToJSON(*in.IPAllowlist)))
		}
		if in.Limits != nil {
			assigns = append(assigns,
				tx.Consumer.MaxInputTokens.Value(in.Limits.MaxInputTokens),
				tx.Consumer.MaxOutputTokens.Value(in.Limits.MaxOutputTokens),
				tx.Consumer.MaxRequestBodyBytes.Value(in.Limits.MaxRequestBodyBytes),
			)
		}
		if _, err := tx.Consumer.WithContext(ctx).Where(tx.Consumer.ID.Eq(id)).UpdateSimple(assigns...); err != nil {
			return err
		}
		c, err := tx.Consumer.WithContext(ctx).Where(tx.Consumer.ID.Eq(id)).First()
		if err != nil {
			return err
		}

		if in.Quotas != nil {
			if err := replaceConsumerQuotas(ctx, tx, id, *in.Quotas); err != nil {
				return err
			}
		}
		if in.Routes != nil {
			if err := replaceConsumerRoutes(ctx, tx, id, *in.Routes); err != nil {
				return err
			}
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

// AddKey creates a new key for consumerID, wrapping the existing
// createConsumerKey helper (also used inline by Create) with a not-found
// check on the owning consumer and a transaction.
func (s consumerStore) AddKey(consumerID string, in storage.CreateConsumerKey) (storage.ConsumerKey, error) {
	ctx := context.Background()
	var out storage.ConsumerKey
	err := s.q.Transaction(func(tx *query.Query) error {
		if _, err := tx.Consumer.WithContext(ctx).Where(tx.Consumer.ID.Eq(consumerID)).First(); err != nil {
			return err
		}
		ck, err := createConsumerKey(ctx, tx, consumerID, in)
		if err != nil {
			return err
		}
		out = ck
		return nil
	})
	return out, err
}

// UpdateKey partially updates a single key by its own ID. The returned
// ConsumerKey never carries a Token (raw tokens are only exposed at creation).
//
// This uses UpdateSimple with explicit column assignments rather than
// Save(model) on a mutated struct: Enabled's `default:true` gorm tag makes
// Save skip writing the column when it holds its Go zero value (false), so a
// struct-level Save can silently fail to persist a disable.
func (s consumerStore) UpdateKey(keyID string, in storage.UpdateConsumerKey) (storage.ConsumerKey, error) {
	ctx := context.Background()
	var out storage.ConsumerKey
	err := s.q.Transaction(func(tx *query.Query) error {
		if _, err := tx.ConsumerKey.WithContext(ctx).Where(tx.ConsumerKey.ID.Eq(keyID)).First(); err != nil {
			return err
		}
		assigns := []field.AssignExpr{tx.ConsumerKey.UpdatedAt.Value(nowISO())}
		if in.Name != nil {
			assigns = append(assigns, tx.ConsumerKey.Name.Value(*in.Name))
		}
		if in.Enabled != nil {
			assigns = append(assigns, tx.ConsumerKey.Enabled.Value(*in.Enabled))
		}
		if in.ExpiresAt != nil {
			assigns = append(assigns, tx.ConsumerKey.ExpiresAt.Value(*in.ExpiresAt))
		}
		if _, err := tx.ConsumerKey.WithContext(ctx).Where(tx.ConsumerKey.ID.Eq(keyID)).UpdateSimple(assigns...); err != nil {
			return err
		}
		k, err := tx.ConsumerKey.WithContext(ctx).Where(tx.ConsumerKey.ID.Eq(keyID)).First()
		if err != nil {
			return err
		}
		out = consumerKeyFromModel(k)
		return nil
	})
	return out, err
}

// DeleteKey deletes a single key by its own ID, returning a not-found error
// (via the row-lookup below) if no such key exists.
func (s consumerStore) DeleteKey(keyID string) error {
	ctx := context.Background()
	return s.q.Transaction(func(tx *query.Query) error {
		if _, err := tx.ConsumerKey.WithContext(ctx).Where(tx.ConsumerKey.ID.Eq(keyID)).First(); err != nil {
			return err
		}
		_, err := tx.ConsumerKey.WithContext(ctx).Where(tx.ConsumerKey.ID.Eq(keyID)).Delete()
		return err
	})
}
