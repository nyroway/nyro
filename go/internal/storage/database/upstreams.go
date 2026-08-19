package database

import (
	"context"

	"gorm.io/gen/field"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/model"
	"github.com/nyroway/nyro/go/internal/storage/query"
)

type upstreamStore struct{ q *query.Query }

func (s upstreamStore) List() ([]storage.Upstream, error) {
	ctx := context.Background()
	rows, err := s.q.Upstream.WithContext(ctx).Order(s.q.Upstream.Name).Find()
	if err != nil {
		return nil, err
	}
	out := make([]storage.Upstream, 0, len(rows))
	for _, r := range rows {
		out = append(out, upstreamFromModel(r))
	}
	return out, nil
}

func (s upstreamStore) Get(id string) (*storage.Upstream, error) {
	ctx := context.Background()
	m, err := s.q.Upstream.WithContext(ctx).Where(s.q.Upstream.ID.Eq(id)).First()
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	out := upstreamFromModel(m)
	return &out, nil
}

func (s upstreamStore) Create(in storage.CreateUpstream) (storage.Upstream, error) {
	ctx := context.Background()
	now := nowISO()
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	m := &model.Upstream{
		ID:              newID(),
		Name:            in.Name,
		Provider:        in.Provider,
		Protocol:        in.Protocol,
		BaseURL:         in.BaseURL,
		CredentialsJSON: jsonRaw(in.CredentialsJSON),
		ModelsJSON:      jsonRaw(in.ModelsJSON),
		ModelsURL:       in.ModelsURL,
		ProxyURL:        in.ProxyURL,
		Enabled:         enabled,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	var out storage.Upstream
	err := s.q.Transaction(func(tx *query.Query) error {
		q := tx.Upstream
		if err := q.WithContext(ctx).Create(m); err != nil {
			return err
		}
		// gorm-gen's Create skips bool zero values with a default:true tag. Apply
		// Enabled=false explicitly in the same transaction so a failed follow-up
		// write cannot leave an enabled upstream behind.
		if !enabled {
			if _, err := q.WithContext(ctx).
				Where(q.ID.Eq(m.ID)).
				UpdateSimple(q.Enabled.Value(false)); err != nil {
				return err
			}
			m.Enabled = false
		}
		out = upstreamFromModel(m)
		return nil
	})
	return out, err
}

func (s upstreamStore) Update(id string, in storage.UpdateUpstream) (storage.Upstream, error) {
	ctx := context.Background()
	// Use UpdateSimple with explicit column assignments instead of Save(m) on a
	// mutated struct. Save skips columns whose value equals the Go zero value,
	// so Enabled=false (zero for bool, DB default:true) would be silently
	// omitted — leaving the upstream enabled even after an explicit disable.
	if _, err := s.q.Upstream.WithContext(ctx).Where(s.q.Upstream.ID.Eq(id)).First(); err != nil {
		return storage.Upstream{}, err
	}
	q := s.q.Upstream
	assigns := []field.AssignExpr{q.UpdatedAt.Value(nowISO())}
	if in.Name != nil {
		assigns = append(assigns, q.Name.Value(*in.Name))
	}
	if in.Provider != nil {
		assigns = append(assigns, q.Provider.Value(*in.Provider))
	}
	if in.Protocol != nil {
		assigns = append(assigns, q.Protocol.Value(*in.Protocol))
	}
	if in.BaseURL != nil {
		assigns = append(assigns, q.BaseURL.Value(*in.BaseURL))
	}
	if in.CredentialsJSON != nil {
		assigns = append(assigns, q.CredentialsJSON.Value(jsonRaw(*in.CredentialsJSON)))
	}
	if in.ModelsJSON != nil {
		assigns = append(assigns, q.ModelsJSON.Value(jsonRaw(*in.ModelsJSON)))
	}
	if in.ModelsURL != nil {
		assigns = append(assigns, q.ModelsURL.Value(*in.ModelsURL))
	}
	if in.ProxyURL != nil {
		assigns = append(assigns, q.ProxyURL.Value(*in.ProxyURL))
	}
	if in.Enabled != nil {
		assigns = append(assigns, q.Enabled.Value(*in.Enabled))
	}
	if _, err := q.WithContext(ctx).Where(q.ID.Eq(id)).UpdateSimple(assigns...); err != nil {
		return storage.Upstream{}, err
	}
	m, err := q.WithContext(ctx).Where(q.ID.Eq(id)).First()
	if err != nil {
		return storage.Upstream{}, err
	}
	return upstreamFromModel(m), nil
}

func (s upstreamStore) Delete(id string) error {
	ctx := context.Background()
	_, err := s.q.Upstream.WithContext(ctx).Where(s.q.Upstream.ID.Eq(id)).Delete()
	return err
}

func (s upstreamStore) ExistsByName(name, excludeID string) (bool, error) {
	ctx := context.Background()
	q := s.q.Upstream.WithContext(ctx).Where(s.q.Upstream.Name.Eq(name))
	if excludeID != "" {
		q = q.Where(s.q.Upstream.ID.Neq(excludeID))
	}
	count, err := q.Count()
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
