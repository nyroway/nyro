package memory

import (
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/internal/storage"
)

func TestCoreUpstreamCRUD(t *testing.T) {
	s := New().Storage()

	created, err := s.Upstreams().Create(storage.CreateUpstream{
		Name: "openai-main", Protocol: "openai-chat",
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

	exists, _ := s.Upstreams().ExistsByName("openai-main", "")
	if !exists {
		t.Fatal("ExistsByName should be true")
	}
	exists, _ = s.Upstreams().ExistsByName("openai-main", created.ID)
	if exists {
		t.Fatal("ExistsByName with excludeID should be false")
	}

	if err := s.Upstreams().Delete(created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, _ := s.Upstreams().Get(created.ID); got != nil {
		t.Fatalf("Get after delete = %+v; want nil", got)
	}
}

func TestCoreRouteCreateWithNestedUpstreams(t *testing.T) {
	s := New().Storage()

	up, err := s.Upstreams().Create(storage.CreateUpstream{Name: "u1"})
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
		t.Fatalf("route.Upstreams = %+v", route.Upstreams)
	}

	byModel, err := s.Routes().ByModel("gpt-4o")
	if err != nil || byModel == nil || len(byModel.Upstreams) != 1 {
		t.Fatalf("ByModel = %+v, %v", byModel, err)
	}

	newTargets := []storage.CreateRouteUpstream{{UpstreamID: up.ID, Model: "gpt-4o-mini", Weight: 50, Priority: 2}}
	updated, err := s.Routes().Update(route.ID, storage.UpdateRoute{Upstreams: &newTargets})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(updated.Upstreams) != 1 || updated.Upstreams[0].Model != "gpt-4o-mini" {
		t.Fatalf("updated.Upstreams = %+v", updated.Upstreams)
	}

	if err := s.Routes().Delete(route.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got, _ := s.Routes().Get(route.ID); got != nil {
		t.Fatal("Get after delete should be nil")
	}
}

func TestCoreConsumerCreateWithKeysRoutesQuotas(t *testing.T) {
	s := New().Storage()

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
		t.Fatalf("consumer.Keys = %+v", consumer.Keys)
	}
	raw := consumer.Keys[0].Token
	if raw == "" {
		t.Fatal("created key Token (raw) should be populated once at creation")
	}
	if len(consumer.Routes) != 1 || consumer.Routes[0] != "gpt-4o" {
		t.Fatalf("consumer.Routes = %+v", consumer.Routes)
	}
	if len(consumer.Quotas) != 1 || consumer.Quotas[0].QuotaLimit != 60 {
		t.Fatalf("consumer.Quotas = %+v", consumer.Quotas)
	}

	got, err := s.Consumers().Get(consumer.ID)
	if err != nil || got == nil || len(got.Keys) != 1 {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if got.Keys[0].Token != "" {
		t.Fatalf("Get().Keys[0].Token = %q; want empty", got.Keys[0].Token)
	}

	rec, err := s.Auth().FindKey(raw)
	if err != nil || rec == nil {
		t.Fatalf("FindKey: %+v, %v", rec, err)
	}
	if rec.ConsumerID != consumer.ID {
		t.Fatalf("rec.ConsumerID = %q, want %q", rec.ConsumerID, consumer.ID)
	}
	if len(rec.Routes) != 1 || rec.Routes[0] != "gpt-4o" {
		t.Fatalf("rec.Routes = %+v", rec.Routes)
	}

	if rec, err := s.Auth().FindKey("nyro_wrong"); err != nil || rec != nil {
		t.Fatalf("FindKey(wrong) = %+v, %v; want nil,nil", rec, err)
	}
}

func TestCoreConsumerUpdateReplacesQuotasWholesale(t *testing.T) {
	s := New().Storage()

	consumer, err := s.Consumers().Create(storage.CreateConsumer{
		Name:   "acme",
		Quotas: []storage.CreateConsumerQuota{{QuotaType: "requests", QuotaLimit: 60, Window: "1m"}},
	})
	if err != nil {
		t.Fatalf("Create consumer: %v", err)
	}

	newQuotas := []storage.CreateConsumerQuota{
		{QuotaType: "tokens", QuotaLimit: 1000, Window: "1h"},
		{QuotaType: "concurrency", QuotaLimit: 5},
	}
	updated, err := s.Consumers().Update(consumer.ID, storage.UpdateConsumer{Quotas: &newQuotas})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(updated.Quotas) != 2 {
		t.Fatalf("updated.Quotas = %+v; want 2 (wholesale replace)", updated.Quotas)
	}

	name := "acme2"
	updated2, err := s.Consumers().Update(consumer.ID, storage.UpdateConsumer{Name: &name})
	if err != nil {
		t.Fatalf("Update (nil Quotas): %v", err)
	}
	if len(updated2.Quotas) != 2 {
		t.Fatalf("updated2.Quotas = %+v; want unchanged (2)", updated2.Quotas)
	}

	empty := []storage.CreateConsumerQuota{}
	updated3, err := s.Consumers().Update(consumer.ID, storage.UpdateConsumer{Quotas: &empty})
	if err != nil {
		t.Fatalf("Update (empty Quotas): %v", err)
	}
	if len(updated3.Quotas) != 0 {
		t.Fatalf("updated3.Quotas = %+v; want empty", updated3.Quotas)
	}
}

func TestCoreConsumerUpdateRejectsInvalidQuota(t *testing.T) {
	s := New().Storage()

	consumer, err := s.Consumers().Create(storage.CreateConsumer{Name: "acme"})
	if err != nil {
		t.Fatalf("Create consumer: %v", err)
	}

	cases := []struct {
		name  string
		quota storage.CreateConsumerQuota
	}{
		{"bad quota_type", storage.CreateConsumerQuota{QuotaType: "bogus", QuotaLimit: 1}},
		{"non-positive limit", storage.CreateConsumerQuota{QuotaType: "requests", QuotaLimit: 0}},
		{"concurrency with window", storage.CreateConsumerQuota{QuotaType: "concurrency", QuotaLimit: 5, Window: "1m"}},
		{"bad window format", storage.CreateConsumerQuota{QuotaType: "requests", QuotaLimit: 1, Window: "bogus"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			quotas := []storage.CreateConsumerQuota{tc.quota}
			if _, err := s.Consumers().Update(consumer.ID, storage.UpdateConsumer{Quotas: &quotas}); err == nil {
				t.Fatalf("Update with %+v: want error, got nil", tc.quota)
			}
		})
	}
}

func TestCoreConsumerUpdateReplacesRoutesByModelName(t *testing.T) {
	s := New().Storage()

	if _, err := s.Routes().Create(storage.CreateRoute{Model: "gpt-4o"}); err != nil {
		t.Fatalf("create route gpt-4o: %v", err)
	}
	if _, err := s.Routes().Create(storage.CreateRoute{Model: "gpt-4o-mini"}); err != nil {
		t.Fatalf("create route gpt-4o-mini: %v", err)
	}

	consumer, err := s.Consumers().Create(storage.CreateConsumer{
		Name:   "acme",
		Routes: []string{"gpt-4o"},
	})
	if err != nil {
		t.Fatalf("Create consumer: %v", err)
	}

	newRoutes := []string{"gpt-4o-mini"}
	updated, err := s.Consumers().Update(consumer.ID, storage.UpdateConsumer{Routes: &newRoutes})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(updated.Routes) != 1 || updated.Routes[0] != "gpt-4o-mini" {
		t.Fatalf("updated.Routes = %+v; want [gpt-4o-mini]", updated.Routes)
	}

	badRoutes := []string{"does-not-exist"}
	if _, err := s.Consumers().Update(consumer.ID, storage.UpdateConsumer{Routes: &badRoutes}); err == nil {
		t.Fatal("Update with unknown route model: want error, got nil")
	} else if !strings.Contains(err.Error(), "does-not-exist") {
		t.Fatalf("error = %v; want it to mention the unknown model name", err)
	}

	got, err := s.Consumers().Get(consumer.ID)
	if err != nil || got == nil || len(got.Routes) != 1 || got.Routes[0] != "gpt-4o-mini" {
		t.Fatalf("Get after failed update = %+v, %v; want unchanged [gpt-4o-mini]", got, err)
	}
}

func TestCoreSettingsUpsert(t *testing.T) {
	s := New().Storage()

	if err := s.Settings().Set("config_epoch", "1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, err := s.Settings().Get("config_epoch")
	if err != nil || v != "1" {
		t.Fatalf("Get = %q, %v", v, err)
	}
	if err := s.Settings().Set("config_epoch", "2"); err != nil {
		t.Fatalf("Set (upsert): %v", err)
	}
	v, _ = s.Settings().Get("config_epoch")
	if v != "2" {
		t.Fatalf("Get after upsert = %q; want 2", v)
	}
	all, err := s.Settings().ListAll()
	if err != nil || len(all) != 1 {
		t.Fatalf("ListAll = %v, %v; want 1 row", all, err)
	}
}
