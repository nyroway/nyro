package database

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/storage"
)

func newTestBackend(t *testing.T) *Backend {
	t.Helper()
	b, err := NewSQLite(":memory:")
	if err != nil {
		t.Fatalf("NewSQLite: %v", err)
	}
	if err := b.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return b
}

func TestUpstreamCRUD(t *testing.T) {
	b := newTestBackend(t)
	var s storage.Storage = b

	created, err := s.Upstreams().Create(storage.CreateUpstream{
		Name: "openai-main", Provider: "openai", Protocol: "openai-chatcompletions",
		BaseURL: "https://api.openai.com/v1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" || !created.Enabled {
		t.Fatalf("created = %+v; want ID set and Enabled=true", created)
	}

	got, err := s.Upstreams().Get(created.ID)
	if err != nil || got == nil || got.Name != "openai-main" {
		t.Fatalf("Get = %+v, %v", got, err)
	}

	exists, err := s.Upstreams().ExistsByName("openai-main", "")
	if err != nil || !exists {
		t.Fatalf("ExistsByName = %v, %v; want true", exists, err)
	}
	exists, err = s.Upstreams().ExistsByName("openai-main", created.ID)
	if err != nil || exists {
		t.Fatalf("ExistsByName excludeID = %v, %v; want false", exists, err)
	}

	newBase := "https://api.openai.com/v2"
	updated, err := s.Upstreams().Update(created.ID, storage.UpdateUpstream{BaseURL: &newBase})
	if err != nil || updated.BaseURL != newBase {
		t.Fatalf("Update = %+v, %v", updated, err)
	}

	list, err := s.Upstreams().List()
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v; want 1 item", list, err)
	}

	if err := s.Upstreams().Delete(created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	got, err = s.Upstreams().Get(created.ID)
	if err != nil || got != nil {
		t.Fatalf("Get after delete = %+v, %v; want nil,nil", got, err)
	}
}

func TestRouteCreateWithNestedUpstreams(t *testing.T) {
	b := newTestBackend(t)
	var s storage.Storage = b

	up, err := s.Upstreams().Create(storage.CreateUpstream{Name: "u1", Provider: "openai"})
	if err != nil {
		t.Fatalf("create upstream: %v", err)
	}

	route, err := s.Routes().Create(storage.CreateRoute{
		Model: "gpt-4o", EnableAuth: true,
		Upstreams: []storage.CreateRouteUpstream{
			{UpstreamID: up.ID, Model: "gpt-4o", Weight: 100, Priority: 1},
		},
	})
	if err != nil {
		t.Fatalf("Create route: %v", err)
	}
	if len(route.Upstreams) != 1 || route.Upstreams[0].UpstreamID != up.ID {
		t.Fatalf("route.Upstreams = %+v; want 1 target for %s", route.Upstreams, up.ID)
	}
	if route.Balance != storage.BalanceWeighted {
		t.Fatalf("Balance = %q; want default weighted", route.Balance)
	}

	byModel, err := s.Routes().ByModel("gpt-4o")
	if err != nil || byModel == nil || len(byModel.Upstreams) != 1 {
		t.Fatalf("ByModel = %+v, %v", byModel, err)
	}

	// Update replaces targets wholesale.
	newTargets := []storage.CreateRouteUpstream{{UpstreamID: up.ID, Model: "gpt-4o-mini", Weight: 50, Priority: 2}}
	updated, err := s.Routes().Update(route.ID, storage.UpdateRoute{Upstreams: &newTargets})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(updated.Upstreams) != 1 || updated.Upstreams[0].Model != "gpt-4o-mini" {
		t.Fatalf("updated.Upstreams = %+v; want replaced target", updated.Upstreams)
	}

	if err := s.Routes().Delete(route.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, err := s.Routes().Get(route.ID); err != nil || got != nil {
		t.Fatalf("Get after delete = %+v, %v", got, err)
	}
}

func TestConsumerCreateWithKeysRoutesQuotas(t *testing.T) {
	b := newTestBackend(t)
	var s storage.Storage = b

	if _, err := s.Routes().Create(storage.CreateRoute{Model: "gpt-4o"}); err != nil {
		t.Fatalf("create route: %v", err)
	}

	consumer, err := s.Consumers().Create(storage.CreateConsumer{
		Name:   "acme",
		Keys:   []storage.CreateConsumerKey{{Name: "primary"}},
		Routes: []string{"gpt-4o"},
		Quotas: []storage.CreateConsumerQuota{{QuotaType: "requests", QuotaLimit: 60, Window: "1m"}},
	})
	if err != nil {
		t.Fatalf("Create consumer: %v", err)
	}
	if len(consumer.Keys) != 1 {
		t.Fatalf("consumer.Keys = %+v; want 1", consumer.Keys)
	}
	raw := consumer.Keys[0].Token
	if raw == "" {
		t.Fatal("created key Token (raw) should be populated once at creation")
	}
	if len(consumer.Routes) != 1 || consumer.Routes[0] != "gpt-4o" {
		t.Fatalf("consumer.Routes = %+v; want [gpt-4o]", consumer.Routes)
	}
	if len(consumer.Quotas) != 1 || consumer.Quotas[0].QuotaLimit != 60 {
		t.Fatalf("consumer.Quotas = %+v", consumer.Quotas)
	}

	// Re-reading via Get must NOT expose the raw token.
	got, err := s.Consumers().Get(consumer.ID)
	if err != nil || got == nil || len(got.Keys) != 1 {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if got.Keys[0].Token != "" {
		t.Fatalf("Get().Keys[0].Token = %q; want empty (raw token only exposed at creation)", got.Keys[0].Token)
	}

	// FindKey resolves the raw token to the consumer's grants.
	rec, err := s.Auth().FindKey(raw)
	if err != nil || rec == nil {
		t.Fatalf("FindKey: %+v, %v", rec, err)
	}
	if rec.ConsumerID != consumer.ID {
		t.Fatalf("rec.ConsumerID = %q, want %q", rec.ConsumerID, consumer.ID)
	}
	if len(rec.Routes) != 1 || rec.Routes[0] != "gpt-4o" {
		t.Fatalf("rec.Routes = %+v; want [gpt-4o]", rec.Routes)
	}
	if len(rec.Quotas) != 1 || rec.Quotas[0].QuotaLimit != 60 {
		t.Fatalf("rec.Quotas = %+v", rec.Quotas)
	}

	// Wrong key must not match.
	if rec, err := s.Auth().FindKey("nyro_wrong"); err != nil || rec != nil {
		t.Fatalf("FindKey(wrong) = %+v, %v; want nil,nil", rec, err)
	}
}

func TestCoreSettingsUpsert(t *testing.T) {
	b := newTestBackend(t)
	var s storage.Storage = b

	if err := s.Settings().Set("config_epoch", "1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, err := s.Settings().Get("config_epoch")
	if err != nil || v != "1" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	// Set again must update, not duplicate (OnConflict upsert on the key column).
	if err := s.Settings().Set("config_epoch", "2"); err != nil {
		t.Fatalf("Set (upsert): %v", err)
	}
	v, err = s.Settings().Get("config_epoch")
	if err != nil || v != "2" {
		t.Fatalf("Get after upsert = %q, %v; want 2", v, err)
	}
	all, err := s.Settings().ListAll()
	if err != nil || len(all) != 1 {
		t.Fatalf("ListAll = %v, %v; want 1 row (no duplicate)", all, err)
	}
}
