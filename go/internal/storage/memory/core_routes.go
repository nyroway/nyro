package memory

import (
	"sort"

	"github.com/nyroway/nyro/go/internal/storage"
)

type routeStore struct{ b *Backend }

// routeWithUpstreams attaches r's targets, sorted by priority asc then weight
// desc (matching the legacy modelWithTargets ordering).
func (b *Backend) routeWithUpstreams(r storage.Route) storage.Route {
	out := r
	out.Upstreams = nil
	for _, ru := range b.routeUpstreams {
		if ru.RouteID == r.ID {
			out.Upstreams = append(out.Upstreams, ru)
		}
	}
	sort.Slice(out.Upstreams, func(i, j int) bool {
		if out.Upstreams[i].Priority != out.Upstreams[j].Priority {
			return out.Upstreams[i].Priority < out.Upstreams[j].Priority
		}
		return out.Upstreams[i].Weight >= out.Upstreams[j].Weight
	})
	return out
}

// replaceRouteUpstreams deletes r's existing targets and inserts in, returning
// the new target DTOs.
func (b *Backend) replaceRouteUpstreams(routeID string, in []storage.CreateRouteUpstream) []storage.RouteUpstream {
	for id, ru := range b.routeUpstreams {
		if ru.RouteID == routeID {
			delete(b.routeUpstreams, id)
		}
	}
	now := nowISO()
	var out []storage.RouteUpstream
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
		ru := storage.RouteUpstream{
			ID: newID(), RouteID: routeID, UpstreamID: t.UpstreamID, Model: t.Model,
			Weight: weight, Priority: priority, Enabled: enabled, CreatedAt: now,
		}
		b.routeUpstreams[ru.ID] = ru
		out = append(out, ru)
	}
	return out
}

func (s routeStore) List() ([]storage.Route, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	out := make([]storage.Route, 0, len(s.b.routes))
	for _, r := range s.b.routes {
		out = append(out, s.b.routeWithUpstreams(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	return out, nil
}

func (s routeStore) Get(id string) (*storage.Route, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	r, ok := s.b.routes[id]
	if !ok {
		return nil, nil
	}
	out := s.b.routeWithUpstreams(r)
	return &out, nil
}

func (s routeStore) ByModel(model string) (*storage.Route, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	for _, r := range s.b.routes {
		if r.Model == model {
			out := s.b.routeWithUpstreams(r)
			return &out, nil
		}
	}
	return nil, nil
}

func (s routeStore) Create(in storage.CreateRoute) (storage.Route, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	now := nowISO()
	enablePayload := false
	if in.EnablePayload != nil {
		enablePayload = *in.EnablePayload
	}
	balance := in.Balance
	if balance == "" {
		balance = storage.BalanceWeighted
	}
	r := storage.Route{
		ID: newID(), Model: in.Model, Balance: balance, EnableAuth: in.EnableAuth,
		EnablePayload: &enablePayload, Enabled: true, CreatedAt: now, UpdatedAt: now,
	}
	s.b.routes[r.ID] = r
	r.Upstreams = s.b.replaceRouteUpstreams(r.ID, in.Upstreams)
	return r, nil
}

func (s routeStore) Update(id string, in storage.UpdateRoute) (storage.Route, error) {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	r, ok := s.b.routes[id]
	if !ok {
		return storage.Route{}, ErrNotFound
	}
	if in.Model != nil {
		r.Model = *in.Model
	}
	if in.Balance != nil {
		r.Balance = *in.Balance
	}
	if in.EnableAuth != nil {
		r.EnableAuth = *in.EnableAuth
	}
	if in.EnablePayload != nil {
		r.EnablePayload = in.EnablePayload
	}
	if in.Enabled != nil {
		r.Enabled = *in.Enabled
	}
	r.UpdatedAt = nowISO()
	s.b.routes[id] = r
	if in.Upstreams != nil {
		r.Upstreams = s.b.replaceRouteUpstreams(id, *in.Upstreams)
	} else {
		r = s.b.routeWithUpstreams(r)
	}
	return r, nil
}

func (s routeStore) Delete(id string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	delete(s.b.routes, id)
	for rid, ru := range s.b.routeUpstreams {
		if ru.RouteID == id {
			delete(s.b.routeUpstreams, rid)
		}
	}
	return nil
}

func (s routeStore) ExistsByName(model, excludeID string) (bool, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	for _, r := range s.b.routes {
		if r.Model == model && r.ID != excludeID {
			return true, nil
		}
	}
	return false, nil
}
