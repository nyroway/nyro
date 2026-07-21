package memory

import (
	"testing"

	"github.com/nyroway/nyro/go/internal/storage"
)

// TestPlaintextKeysRoundTrip proves that with SetPlaintextKeys(true), a created
// key's raw value is recoverable on read paths (List/Get) via Token, while the
// hash/preview are still persisted for auth.
func TestPlaintextKeysRoundTrip(t *testing.T) {
	b := New()
	b.SetPlaintextKeys(true)
	st := b.Storage()

	created, err := st.Consumers().Create(storage.CreateConsumer{
		Name: "acme",
		Keys: []storage.CreateConsumerKey{{Name: "primary"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Keys) != 1 || created.Keys[0].Token == "" {
		t.Fatalf("create did not expose raw token: %+v", created.Keys)
	}
	raw := created.Keys[0].Token

	// Read back — plaintext mode re-exposes the same raw key via Token.
	got, err := st.Consumers().Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Keys) != 1 {
		t.Fatalf("want 1 key, got %d", len(got.Keys))
	}
	if got.Keys[0].Token != raw {
		t.Errorf("read Token = %q, want recovered raw %q", got.Keys[0].Token, raw)
	}
	// Auth still works off the hash, and the preview follows the sk-+6 / +6 rule.
	if got.Keys[0].KeyHash != storage.HashKey(raw) {
		t.Errorf("persisted hash must match HashKey(raw)")
	}
	if got.Keys[0].KeyPreview != storage.PreviewOf(raw) {
		t.Errorf("persisted preview must match PreviewOf(raw)")
	}
}

// TestHashOnlyKeysDefault proves the default (no SetPlaintextKeys) never
// re-exposes the raw key on read paths — Token is empty after creation.
func TestHashOnlyKeysDefault(t *testing.T) {
	b := New()
	st := b.Storage()

	created, err := st.Consumers().Create(storage.CreateConsumer{
		Name: "acme",
		Keys: []storage.CreateConsumerKey{{Name: "primary"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Keys[0].Token == "" {
		t.Fatal("create should still expose the one-time raw token")
	}

	got, err := st.Consumers().Get(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Keys[0].Token != "" {
		t.Errorf("hash-only read must not expose a raw token, got %q", got.Keys[0].Token)
	}
	if got.Keys[0].KeyPlaintext != "" {
		t.Errorf("hash-only key must not persist plaintext")
	}
}
