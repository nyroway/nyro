package memory

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/storage"
)

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }

func TestProviderCRUD(t *testing.T) {
	b := New()
	ps := b.Providers()

	p, err := ps.Create(storage.CreateProvider{
		Name: "OpenAI", Protocol: "openai-compatible", BaseURL: "https://api.openai.com", APIKey: "sk-1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.ID == "" || p.AuthMode != "apikey" || !p.IsEnabled {
		t.Errorf("unexpected provider: %+v", p)
	}

	if found, _ := ps.ExistsByName("OpenAI", ""); !found {
		t.Error("ExistsByName should find OpenAI")
	}
	if found, _ := ps.ExistsByName("OpenAI", p.ID); found {
		t.Error("ExistsByName should exclude self")
	}

	list, _ := ps.List()
	if len(list) != 1 {
		t.Errorf("list len=%d", len(list))
	}

	got, _ := ps.Get(p.ID)
	if got == nil || got.Name != "OpenAI" {
		t.Errorf("get=%+v", got)
	}
	if missing, _ := ps.Get("nope"); missing != nil {
		t.Error("expected nil for missing id")
	}

	updated, err := ps.Update(p.ID, storage.UpdateProvider{Name: strPtr("OpenAI-2"), IsEnabled: boolPtr(false)})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "OpenAI-2" || updated.IsEnabled {
		t.Errorf("updated=%+v", updated)
	}
	// unchanged fields preserved
	if updated.BaseURL != "https://api.openai.com" {
		t.Errorf("base_url not preserved: %q", updated.BaseURL)
	}

	if _, err := ps.Update("missing", storage.UpdateProvider{}); err != ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
	if err := ps.Delete(p.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if list, _ := ps.List(); len(list) != 0 {
		t.Errorf("list after delete len=%d", len(list))
	}
}

func TestModelWithBackends(t *testing.T) {
	b := New()
	ps, ms := b.Providers(), b.Models()

	prov, _ := ps.Create(storage.CreateProvider{Name: "P", Protocol: "openai-compatible", BaseURL: "u", APIKey: "k"})

	m, err := ms.Create(storage.CreateModel{
		Name: "gpt-4o", Balance: storage.BalanceWeighted,
		Targets: []storage.CreateModelBackend{
			{ProviderID: prov.ID, Model: "gpt-4o", Weight: 2},
			{ProviderID: prov.ID, Model: "gpt-4o-mini", Weight: 1, Priority: 1},
		},
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	if len(m.Targets) != 2 {
		t.Fatalf("targets=%d", len(m.Targets))
	}

	got, _ := ms.ByName("gpt-4o")
	if got == nil || len(got.Targets) != 2 {
		t.Errorf("byname targets: %+v", got)
	}
	// backends sorted: priority 0 first (Weight 2), then priority 1.
	if got.Targets[0].Model != "gpt-4o" {
		t.Errorf("order wrong: %+v", got.Targets)
	}

	// replace targets
	upd, _ := ms.Update(got.ID, storage.UpdateModel{
		Targets: &[]storage.CreateModelBackend{{ProviderID: prov.ID, Model: "gpt-4o", Weight: 5}},
	})
	if len(upd.Targets) != 1 || upd.Targets[0].Model != "gpt-4o" {
		t.Errorf("after replace: %+v", upd.Targets)
	}

	// cascade delete backends
	if err := ms.Delete(got.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if bs, _ := b.ModelBackends().ListByModel(got.ID); len(bs) != 0 {
		t.Errorf("backends not cascaded: %d", len(bs))
	}
}

func TestSettings(t *testing.T) {
	b := New()
	ss := b.Settings()

	if v, _ := ss.Get("missing"); v != "" {
		t.Errorf("absent get=%q", v)
	}
	if err := ss.Set("k", "v1"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if v, _ := ss.Get("k"); v != "v1" {
		t.Errorf("get=%q", v)
	}
	_ = ss.Set("k", "v2")
	if v, _ := ss.Get("k"); v != "v2" {
		t.Errorf("overwrite get=%q", v)
	}
	all, _ := ss.ListAll()
	if len(all) != 1 {
		t.Errorf("list len=%d", len(all))
	}
}

func TestBootstrapHealth(t *testing.T) {
	b := New()
	if err := b.Bootstrap().Init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	h, _ := b.Bootstrap().Health()
	if !h.CanConnect || !h.Writable || h.Backend != "memory" {
		t.Errorf("health=%+v", h)
	}
}
