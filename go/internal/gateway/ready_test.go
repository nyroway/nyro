package gateway

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	configsnapshot "github.com/nyroway/nyro/go/internal/config/snapshot"
	"github.com/nyroway/nyro/go/internal/quota"
	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/memory"
)

// TestReadyz verifies the readiness probe is gated on config-cache fill (P3c:
// the gateway no longer holds a storage handle, so readiness is "has a snapshot
// been published?"). Empty cache → 503 unready; published snapshot → 200 ready.
func TestReadyz(t *testing.T) {
	// Empty cache → unready.
	gw := NewGateway(testProtocolCatalog(t), testProviderCatalog(t))
	r := NewRouter(gw)
	req := httptest.NewRequest("GET", "/readyz", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("empty cache: /readyz → %d, want 503; body %s", rec.Code, rec.Body.String())
	}

	// Published snapshot → ready.
	st := memory.New()
	core := st.Storage()
	up, _ := core.Upstreams().Create(storage.CreateUpstream{
		Name: "p", Protocol: "openai-chatcompletions", BaseURL: "http://up",
		CredentialsJSON: []byte(`{"api_key":"k"}`),
	})
	_, _ = core.Routes().Create(storage.CreateRoute{
		Model: "m", Upstreams: []storage.CreateRouteUpstream{{UpstreamID: up.ID, Model: "m"}},
	})
	gw2 := NewGateway(testProtocolCatalog(t), testProviderCatalog(t))
	if err := storage.LoadAndSwap(gw2.Cache, core); err != nil {
		t.Fatalf("load cache: %v", err)
	}
	r2 := NewRouter(gw2)
	req2 := httptest.NewRequest("GET", "/readyz", nil)
	rec2 := httptest.NewRecorder()
	r2.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("filled cache: /readyz → %d, want 200; body %s", rec2.Code, rec2.Body.String())
	}
	if !bytes.Contains(rec2.Body.Bytes(), []byte(`"status":"ready"`)) {
		t.Errorf("/readyz body: %s", rec2.Body.String())
	}

	// Sanity: a hand-built snapshot via Swap also flips ready (the config-sync path).
	gw3 := NewGateway(testProtocolCatalog(t), testProviderCatalog(t))
	gw3.Cache.Swap((&configsnapshot.Builder{}).Build())
	r3 := NewRouter(gw3)
	req3 := httptest.NewRequest("GET", "/readyz", nil)
	rec3 := httptest.NewRecorder()
	r3.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("swapped snapshot: /readyz → %d, want 200", rec3.Code)
	}

	// A filled config cache is still unready when quota State is unavailable.
	cache := &configsnapshot.Cache{}
	cache.Swap((&configsnapshot.Builder{}).Build())
	quotaSwitch := quota.NewUnavailableSwitch()
	gw4 := NewGatewayWithCache(cache, quotaSwitch, testProtocolCatalog(t), testProviderCatalog(t))
	r4 := NewRouter(gw4)
	req4 := httptest.NewRequest("GET", "/readyz", nil)
	rec4 := httptest.NewRecorder()
	r4.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable State: /readyz → %d, want 503", rec4.Code)
	}

	quotaSwitch.Swap(quota.NewMemory(), nil, 0)
	req5 := httptest.NewRequest("GET", "/readyz", nil)
	rec5 := httptest.NewRecorder()
	r4.ServeHTTP(rec5, req5)
	if rec5.Code != http.StatusOK {
		t.Fatalf("restored State: /readyz → %d, want 200", rec5.Code)
	}
}
