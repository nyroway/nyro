package memory

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/storage"
)

func TestCoreUpstreamCRUD(t *testing.T) {
	s := New().Storage()

	created, err := s.Upstreams().Create(storage.CreateUpstream{
		Name: "openai-main", Provider: "openai", Protocol: "openai-chatcompletions",
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
