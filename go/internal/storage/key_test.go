package storage

import "testing"

// TestPreviewOfFormat locks the persisted preview shape: the "sk-" prefix plus
// the first 6 key characters, concatenated with the trailing 6 characters
// (15 chars total, no mask characters — the UI adds those at display time).
func TestPreviewOfFormat(t *testing.T) {
	raw := "sk-0123456789abcdef0123456789abcdef" // len 35 > lead+trail
	got := PreviewOf(raw)
	want := raw[:9] + raw[len(raw)-6:]
	if got != want {
		t.Fatalf("PreviewOf(%q) = %q, want %q", raw, got, want)
	}
	if len(got) != keyPreviewLeadLen+keyPreviewTrailLen {
		t.Fatalf("preview len = %d, want %d", len(got), keyPreviewLeadLen+keyPreviewTrailLen)
	}
	if got[:3] != "sk-" {
		t.Fatalf("preview should keep the sk- prefix, got %q", got)
	}
}

// TestPreviewOfShortKey returns the whole string when it is no longer than the
// combined lead+trail length (nothing to mask).
func TestPreviewOfShortKey(t *testing.T) {
	raw := "sk-123456" // len 9 <= 15
	if got := PreviewOf(raw); got != raw {
		t.Fatalf("PreviewOf(%q) = %q, want the whole key", raw, got)
	}
}

// TestGenerateKeyPreviewMatchesHashLookup proves the preview a freshly
// generated key persists is exactly the preview recomputed from its raw value
// — the invariant KeyAuthStore.FindKey relies on to narrow candidates.
func TestGenerateKeyPreviewMatchesHashLookup(t *testing.T) {
	raw, preview, hash, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if preview != PreviewOf(raw) {
		t.Errorf("stored preview %q != PreviewOf(raw) %q", preview, PreviewOf(raw))
	}
	if hash != HashKey(raw) {
		t.Errorf("stored hash != HashKey(raw)")
	}
}
