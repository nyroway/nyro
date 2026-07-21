package database

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/storage"
)

// TestPlaintextKeysRoundTripSQL mirrors the memory-backend test on the SQL
// backend: with SetPlaintextKeys(true), the raw key is stored (column
// key_plaintext) and recovered on read paths via Token; the default leaves it
// hash-only.
func TestPlaintextKeysRoundTripSQL(t *testing.T) {
	b := newTestBackend(t)
	b.SetPlaintextKeys(true)
	var st storage.Storage = b

	created, err := st.Consumers().Create(storage.CreateConsumer{
		Name: "acme",
		Keys: []storage.CreateConsumerKey{{Name: "primary"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := created.Keys[0].Token
	if raw == "" {
		t.Fatal("create did not expose raw token")
	}

	got, err := st.Consumers().Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Keys[0].Token != raw {
		t.Errorf("read Token = %q, want recovered raw %q", got.Keys[0].Token, raw)
	}

	// The recovered raw key still authenticates via the hash path.
	rec, err := st.Auth().FindKey(raw)
	if err != nil || rec == nil {
		t.Fatalf("FindKey(raw) after plaintext round-trip: rec=%v err=%v", rec, err)
	}
}

func TestHashOnlyKeysDefaultSQL(t *testing.T) {
	b := newTestBackend(t) // no SetPlaintextKeys
	var st storage.Storage = b

	created, err := st.Consumers().Create(storage.CreateConsumer{
		Name: "acme",
		Keys: []storage.CreateConsumerKey{{Name: "primary"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := created.Keys[0].Token

	got, err := st.Consumers().Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Keys[0].Token != "" {
		t.Errorf("hash-only read must not expose a raw token, got %q", got.Keys[0].Token)
	}
	// Auth still works (hash path unaffected by storage mode).
	if rec, err := st.Auth().FindKey(raw); err != nil || rec == nil {
		t.Fatalf("FindKey(raw) hash-only: rec=%v err=%v", rec, err)
	}
}
