package memory

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/storage"
)

func TestAPIKeyCreateHonorsExplicitToken(t *testing.T) {
	st := New()
	created, err := st.APIKeys().Create(storage.CreateApiKey{Name: "standalone", Token: "nyro-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Token != "nyro-secret" {
		t.Errorf("Token = %q, want nyro-secret (explicit token must be honored)", created.Token)
	}
	rec, err := st.Auth().FindAPIKey("nyro-secret")
	if err != nil || rec == nil {
		t.Errorf("FindAPIKey(nyro-secret) = %v %v; the explicit token must be discoverable", rec, err)
	}
}

func TestOAuthListAll(t *testing.T) {
	st := New()
	// Empty store returns an empty (non-nil) slice.
	all, err := st.OAuthCredentials().ListAll()
	if err != nil {
		t.Fatalf("ListAll empty: %v", err)
	}
	if len(all) != 0 {
		t.Errorf("ListAll empty = %d; want 0", len(all))
	}
	if _, err := st.OAuthCredentials().Upsert("provB", storage.UpsertOAuthCredential{
		DriverKey: "drv", AccessToken: "b",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.OAuthCredentials().Upsert("provA", storage.UpsertOAuthCredential{
		DriverKey: "drv", AccessToken: "a",
	}); err != nil {
		t.Fatal(err)
	}
	all, err = st.OAuthCredentials().ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAll = %d items; want 2", len(all))
	}
	// Sorted by provider ID for stable snapshot ordering.
	if all[0].ProviderID != "provA" || all[1].ProviderID != "provB" {
		t.Errorf("ListAll order = %s,%s; want provA,provB", all[0].ProviderID, all[1].ProviderID)
	}
}
