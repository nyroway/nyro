package snapshot

// Upstream is the data plane's immutable upstream configuration. It contains
// only runtime-relevant fields, with JSON blobs retained as opaque data.
type Upstream struct {
	ID              string
	Name            string
	Provider        string
	Protocol        string
	BaseURL         string
	CredentialsJSON []byte
	ProxyURL        string
	Enabled         bool
}

// Route is the data plane's client-facing model route.
type Route struct {
	ID            string
	Model         string
	Balance       string
	EnableAuth    bool
	EnablePayload *bool
	Enabled       bool
	Upstreams     []RouteTarget
}

// RouteTarget is one upstream selected by a Route.
type RouteTarget struct {
	ID         string
	RouteID    string
	UpstreamID string
	Model      string
	Weight     int32
	Priority   int32
	Enabled    bool
}

// ConsumerQuota is the quota configuration required for request admission.
type ConsumerQuota struct {
	ID         string
	ConsumerID string
	QuotaType  string
	QuotaLimit int64
	Window     string
	Currency   string
}

// ConsumerAccess is the gateway-facing result of resolving one consumer key.
// It never includes the raw key or its hash.
type ConsumerAccess struct {
	KeyID      string
	ConsumerID string
	Name       string
	KeyPreview string
	Enabled    bool
	ExpiresAt  string
	Routes     []string
	Quotas     []ConsumerQuota
}

type consumerKeyEntry struct {
	ConsumerAccess
	keyHash string
}

func cloneUpstream(in Upstream) Upstream {
	in.CredentialsJSON = append([]byte(nil), in.CredentialsJSON...)
	return in
}

func cloneRoute(in Route) Route {
	if in.EnablePayload != nil {
		value := *in.EnablePayload
		in.EnablePayload = &value
	}
	in.Upstreams = append([]RouteTarget(nil), in.Upstreams...)
	return in
}

func cloneConsumerAccess(in ConsumerAccess) ConsumerAccess {
	in.Routes = append([]string(nil), in.Routes...)
	in.Quotas = append([]ConsumerQuota(nil), in.Quotas...)
	return in
}
