package configsync

import (
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// waitFor polls cond until it returns true or the deadline elapses.
func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", d)
}

// newPopulatedStorage builds the shared config-sync fixture: one upstream, two
// routes, and one consumer with an API key, route grant, and requests quota.
func newPopulatedStorage(t *testing.T) (*memory.Backend, storage.Upstream, storage.Route, storage.Route, storage.Consumer, string) {
	t.Helper()
	st := memory.New()
	core := st.Storage()

	upstream, err := core.Upstreams().Create(storage.CreateUpstream{
		Name: "openai", Provider: "openai", Protocol: "openai",
		BaseURL: "https://api.openai.com", CredentialsJSON: []byte(`{"api_key":"sk-upstream"}`),
	})
	if err != nil {
		t.Fatalf("create upstream: %v", err)
	}

	openRoute, err := core.Routes().Create(storage.CreateRoute{
		Model: "gpt-open", Upstreams: []storage.CreateRouteUpstream{
			{UpstreamID: upstream.ID, Model: "gpt-4o", Weight: 1},
		},
	})
	if err != nil {
		t.Fatalf("create open route: %v", err)
	}

	gatedRoute, err := core.Routes().Create(storage.CreateRoute{
		Model: "gpt-gated", EnableAuth: true, Upstreams: []storage.CreateRouteUpstream{
			{UpstreamID: upstream.ID, Model: "gpt-4o-gated", Weight: 1},
		},
	})
	if err != nil {
		t.Fatalf("create gated route: %v", err)
	}

	consumer, err := core.Consumers().Create(storage.CreateConsumer{
		Name:   "alice",
		Keys:   []storage.CreateConsumerKey{{Name: "primary", Token: "nyro_tok_alice_0000"}},
		Routes: []string{gatedRoute.Model},
		Quotas: []storage.CreateConsumerQuota{
			{QuotaType: "requests", QuotaLimit: 100, Window: "1m"},
			{QuotaType: "budget", QuotaLimit: 12, Window: "1mo", Currency: "USD"},
		},
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}

	if err := core.Settings().Set("proxy_enabled", "true"); err != nil {
		t.Fatalf("set proxy_enabled: %v", err)
	}
	if err := core.Settings().Set("proxy_url", "http://proxy.local:8080"); err != nil {
		t.Fatalf("set proxy_url: %v", err)
	}
	return st, upstream, openRoute, gatedRoute, consumer, consumer.Keys[0].Token
}
