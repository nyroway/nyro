package database

import (
	"context"

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
		Protocol:        in.Protocol,
		BaseURL:         in.BaseURL,
		CredentialsJSON: jsonRaw(in.CredentialsJSON),
		ModelsJSON:      jsonRaw(in.ModelsJSON),
		ProxyURL:        in.ProxyURL,
		Enabled:         enabled,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.q.Upstream.WithContext(ctx).Create(m); err != nil {
		return storage.Upstream{}, err
	}
	return upstreamFromModel(m), nil
}

func (s upstreamStore) Update(id string, in storage.UpdateUpstream) (storage.Upstream, error) {
	ctx := context.Background()
	m, err := s.q.Upstream.WithContext(ctx).Where(s.q.Upstream.ID.Eq(id)).First()
	if err != nil {
		return storage.Upstream{}, err
	}
	if in.Name != nil {
		m.Name = *in.Name
	}
	if in.Protocol != nil {
		m.Protocol = *in.Protocol
	}
	if in.BaseURL != nil {
		m.BaseURL = *in.BaseURL
	}
	if in.CredentialsJSON != nil {
		m.CredentialsJSON = jsonRaw(*in.CredentialsJSON)
	}
	if in.ModelsJSON != nil {
		m.ModelsJSON = jsonRaw(*in.ModelsJSON)
	}
	if in.ProxyURL != nil {
		m.ProxyURL = *in.ProxyURL
	}
	if in.Enabled != nil {
		m.Enabled = *in.Enabled
	}
	m.UpdatedAt = nowISO()
	if err := s.q.Upstream.WithContext(ctx).Save(m); err != nil {
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
