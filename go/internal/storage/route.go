package storage

// Route is a client-facing model route (table: routes). It replaces the legacy
// Model: its targets are RouteUpstreams carrying weight/priority per upstream.
type Route struct {
	ID            string          `json:"id"`
	Model         string          `json:"model"`
	Balance       ModelBalance    `json:"balance"`
	EnableAuth    bool            `json:"enable_auth"`
	EnablePayload *bool           `json:"enable_payload,omitempty"` // nil = unset (three-state)
	Enabled       bool            `json:"enabled"`
	Upstreams     []RouteUpstream `json:"upstreams,omitempty"`
	CreatedAt     string          `json:"created_at,omitempty"`
	UpdatedAt     string          `json:"updated_at,omitempty"`
}

// RouteUpstream is one target of a route (table: route_upstreams).
type RouteUpstream struct {
	ID         string `json:"id"`
	RouteID    string `json:"route_id"`
	UpstreamID string `json:"upstream_id"`
	Model      string `json:"model"`
	Weight     int32  `json:"weight"`
	Priority   int32  `json:"priority"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  string `json:"created_at,omitempty"`
}

// CreateRoute is the write DTO for creating a route with its upstream targets.
type CreateRoute struct {
	Model         string                `json:"model"`
	Balance       ModelBalance          `json:"balance"`
	EnableAuth    bool                  `json:"enable_auth"`
	EnablePayload *bool                 `json:"enable_payload,omitempty"`
	Upstreams     []CreateRouteUpstream `json:"upstreams"`
}

// CreateRouteUpstream is one target within a CreateRoute.
type CreateRouteUpstream struct {
	UpstreamID string `json:"upstream_id"`
	Model      string `json:"model"`
	Weight     int32  `json:"weight,omitempty"`
	Priority   int32  `json:"priority,omitempty"`
	Enabled    *bool  `json:"enabled,omitempty"`
}

// UpdateRoute is the partial-update DTO; nil fields mean "unchanged". A non-nil
// Upstreams slice replaces the route's targets wholesale.
type UpdateRoute struct {
	Model         *string                `json:"model,omitempty"`
	Balance       *ModelBalance          `json:"balance,omitempty"`
	EnableAuth    *bool                  `json:"enable_auth,omitempty"`
	EnablePayload *bool                  `json:"enable_payload,omitempty"`
	Enabled       *bool                  `json:"enabled,omitempty"`
	Upstreams     *[]CreateRouteUpstream `json:"upstreams,omitempty"`
}

// RouteStore is the CRUD store for routes. ByModel looks up a route by its
// client-facing model name (replaces legacy ModelStore.ByName).
type RouteStore interface {
	List() ([]Route, error)
	Get(id string) (*Route, error)
	ByModel(model string) (*Route, error)
	Create(in CreateRoute) (Route, error)
	Update(id string, in UpdateRoute) (Route, error)
	Delete(id string) error
	ExistsByName(model, excludeID string) (bool, error)
}
