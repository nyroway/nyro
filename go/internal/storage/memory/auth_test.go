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
