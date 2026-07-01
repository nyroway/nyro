package database

import (
	"context"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/model"
	"github.com/nyroway/nyro/go/internal/storage/query"
)

type routeStore struct{ q *query.Query }

func (s routeStore) withUpstreams(ctx context.Context, r *model.Route) (storage.Route, error) {
	out := routeFromModel(r)
	targets, err := s.q.RouteUpstream.WithContext(ctx).
		Where(s.q.RouteUpstream.RouteID.Eq(r.ID)).
		Order(s.q.RouteUpstream.Priority, s.q.RouteUpstream.Weight.Desc()).
		Find()
	if err != nil {
		return storage.Route{}, err
	}
	for _, t := range targets {
		out.Upstreams = append(out.Upstreams, routeUpstreamFromModel(t))
	}
	return out, nil
}

func (s routeStore) List() ([]storage.Route, error) {
	ctx := context.Background()
	rows, err := s.q.Route.WithContext(ctx).Order(s.q.Route.Model).Find()
	if err != nil {
		return nil, err
	}
	out := make([]storage.Route, 0, len(rows))
	for _, r := range rows {
		withTargets, err := s.withUpstreams(ctx, r)
		if err != nil {
			return nil, err
		}
		out = append(out, withTargets)
	}
	return out, nil
}

func (s routeStore) Get(id string) (*storage.Route, error) {
	ctx := context.Background()
	m, err := s.q.Route.WithContext(ctx).Where(s.q.Route.ID.Eq(id)).First()
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	out, err := s.withUpstreams(ctx, m)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s routeStore) ByModel(model string) (*storage.Route, error) {
	ctx := context.Background()
	m, err := s.q.Route.WithContext(ctx).Where(s.q.Route.Model.Eq(model)).First()
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	out, err := s.withUpstreams(ctx, m)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (s routeStore) Create(in storage.CreateRoute) (storage.Route, error) {
	ctx := context.Background()
	now := nowISO()
	enablePayload := false
	if in.EnablePayload != nil {
		enablePayload = *in.EnablePayload
	}
	balance := in.Balance
	if balance == "" {
		balance = storage.BalanceWeighted
	}
	r := &model.Route{
		ID:            newID(),
		Model:         in.Model,
		Balance:       string(balance),
		EnableAuth:    in.EnableAuth,
		EnablePayload: enablePayload,
		Enabled:       true,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	var out storage.Route
	err := s.q.Transaction(func(tx *query.Query) error {
		if err := tx.Route.WithContext(ctx).Create(r); err != nil {
			return err
		}
		targets, err := createRouteUpstreams(ctx, tx, r.ID, in.Upstreams)
		if err != nil {
			return err
		}
		out = routeFromModel(r)
		out.Upstreams = targets
		return nil
	})
	return out, err
}

// createRouteUpstreams creates the route_upstreams rows for a route inside an
// existing transaction and returns their storage DTOs.
func createRouteUpstreams(ctx context.Context, tx *query.Query, routeID string, in []storage.CreateRouteUpstream) ([]storage.RouteUpstream, error) {
	now := nowISO()
	out := make([]storage.RouteUpstream, 0, len(in))
	for _, t := range in {
		enabled := true
		if t.Enabled != nil {
			enabled = *t.Enabled
		}
		weight := t.Weight
		if weight == 0 {
			weight = 100
		}
		priority := t.Priority
		if priority == 0 {
			priority = 1
		}
		ru := &model.RouteUpstream{
			ID:         newID(),
			RouteID:    routeID,
			UpstreamID: t.UpstreamID,
			Model:      t.Model,
			Weight:     weight,
			Priority:   priority,
			Enabled:    enabled,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		if err := tx.RouteUpstream.WithContext(ctx).Create(ru); err != nil {
			return nil, err
		}
		out = append(out, routeUpstreamFromModel(ru))
	}
	return out, nil
}

func (s routeStore) Update(id string, in storage.UpdateRoute) (storage.Route, error) {
	ctx := context.Background()
	var out storage.Route
	err := s.q.Transaction(func(tx *query.Query) error {
		r, err := tx.Route.WithContext(ctx).Where(tx.Route.ID.Eq(id)).First()
		if err != nil {
			return err
		}
		if in.Model != nil {
			r.Model = *in.Model
		}
		if in.Balance != nil {
			r.Balance = string(*in.Balance)
		}
		if in.EnableAuth != nil {
			r.EnableAuth = *in.EnableAuth
		}
		if in.EnablePayload != nil {
			r.EnablePayload = *in.EnablePayload
		}
		if in.Enabled != nil {
			r.Enabled = *in.Enabled
		}
		r.UpdatedAt = nowISO()
		if err := tx.Route.WithContext(ctx).Save(r); err != nil {
			return err
		}

		var targets []storage.RouteUpstream
		if in.Upstreams != nil {
			if _, err := tx.RouteUpstream.WithContext(ctx).Where(tx.RouteUpstream.RouteID.Eq(id)).Delete(); err != nil {
				return err
			}
			targets, err = createRouteUpstreams(ctx, tx, id, *in.Upstreams)
			if err != nil {
				return err
			}
		} else {
			rows, err := tx.RouteUpstream.WithContext(ctx).Where(tx.RouteUpstream.RouteID.Eq(id)).Find()
			if err != nil {
				return err
			}
			for _, ru := range rows {
				targets = append(targets, routeUpstreamFromModel(ru))
			}
		}
		out = routeFromModel(r)
		out.Upstreams = targets
		return nil
	})
	return out, err
}

func (s routeStore) Delete(id string) error {
	ctx := context.Background()
	return s.q.Transaction(func(tx *query.Query) error {
		if _, err := tx.RouteUpstream.WithContext(ctx).Where(tx.RouteUpstream.RouteID.Eq(id)).Delete(); err != nil {
			return err
		}
		_, err := tx.Route.WithContext(ctx).Where(tx.Route.ID.Eq(id)).Delete()
		return err
	})
}

func (s routeStore) ExistsByName(model, excludeID string) (bool, error) {
	ctx := context.Background()
	q := s.q.Route.WithContext(ctx).Where(s.q.Route.Model.Eq(model))
	if excludeID != "" {
		q = q.Where(s.q.Route.ID.Neq(excludeID))
	}
	count, err := q.Count()
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
