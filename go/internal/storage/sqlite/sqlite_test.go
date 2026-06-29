package sqlite

import (
	"testing"
	"time"

	"github.com/nyroway/nyro/go/internal/storage"
)

func newTestBackend(t *testing.T) *Backend {
	t.Helper()
	b, err := New("")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := b.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return b
}

func TestSQLiteProviderModelSettings(t *testing.T) {
	b := newTestBackend(t)
	st := b.Storage()

	// provider
	p, err := st.Providers().Create(storage.CreateProvider{
		Name: "OpenAI", Protocol: "openai-compatible", BaseURL: "https://api.openai.com", APIKey: "sk",
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	if p.ID == "" || p.AuthMode != "apikey" {
		t.Errorf("provider: %+v", p)
	}
	if list, _ := st.Providers().List(); len(list) != 1 {
		t.Errorf("list len=%d", len(list))
	}
	got, _ := st.Providers().Get(p.ID)
	if got == nil || got.Name != "OpenAI" {
		t.Errorf("get: %+v", got)
	}

	// model + backends
	m, err := st.Models().Create(storage.CreateModel{
		Name:    "gpt-4o",
		Targets: []storage.CreateModelBackend{{ProviderID: p.ID, Model: "gpt-4o", Weight: 2}},
	})
	if err != nil {
		t.Fatalf("create model: %v", err)
	}
	byName, _ := st.Models().ByName("gpt-4o")
	if byName == nil || len(byName.Targets) != 1 || byName.Targets[0].Model != "gpt-4o" {
		t.Errorf("byname: %+v", byName)
	}

	// api-key + bindings
	k, err := st.APIKeys().Create(storage.CreateApiKey{Name: "test", ModelIDs: []string{m.ID}})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	if k.Token == "" || len(k.ModelIDs) != 1 {
		t.Errorf("key: %+v", k)
	}
	rec, _ := st.Auth().FindAPIKey(k.Token)
	if rec == nil || rec.ID != k.ID {
		t.Errorf("find key: %+v", rec)
	}
	if bound, _ := st.Auth().ModelBindingExists(k.ID, m.ID); !bound {
		t.Error("binding should exist")
	}

	// settings upsert
	_ = st.Settings().Set("k", "v1")
	if v, _ := st.Settings().Get("k"); v != "v1" {
		t.Errorf("get: %q", v)
	}
	_ = st.Settings().Set("k", "v2")
	if v, _ := st.Settings().Get("k"); v != "v2" {
		t.Errorf("upsert: %q", v)
	}

	// logging + quota counting
	_ = st.Logs().AppendBatch([]storage.RequestLog{{
		ID: "r1", CreatedAt: time.Now().UnixMilli(), APIKeyID: k.ID, ModelID: m.ID,
		InputTokens: 10, OutputTokens: 5,
	}})
	if n, _ := st.Auth().RequestCountSince(k.ID, storage.WindowMinute); n != 1 {
		t.Errorf("request count=%d", n)
	}
	if tok, _ := st.Auth().TokenCountSince(k.ID, storage.WindowMinute); tok != 15 {
		t.Errorf("token count=%d", tok)
	}

	// cascade delete
	_ = st.Models().Delete(m.ID)
	if bs, _ := st.ModelBackends().ListByModel(m.ID); len(bs) != 0 {
		t.Errorf("backends not cascaded: %d", len(bs))
	}

	// not found
	if _, err := st.Providers().Update("missing", storage.UpdateProvider{}); err != storage.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestSQLiteHealth(t *testing.T) {
	b := newTestBackend(t)
	h, _ := b.Health()
	if !h.CanConnect || h.Backend != "sqlite" {
		t.Errorf("health=%+v", h)
	}
}
