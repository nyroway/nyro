package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// keyPreviewLeadLen and keyPreviewTrailLen are the number of raw-key
// characters persisted as KeyPreview (leading+trailing, concatenated). The
// leading slice alone is also what KeyAuthStore.FindKey uses to narrow
// candidates before a hash compare — auth time always has the full raw key,
// so it derives the identical leading+trailing slice and does an exact-match
// lookup against the stored value, preserving the same indexed-equality
// lookup behavior as before this field also carried a trailing slice.
const (
	keyPreviewLeadLen  = 12
	keyPreviewTrailLen = 6
)

// GenerateKey creates a new random API key. raw is returned to the caller
// exactly once (at creation time) and must never be persisted; preview and
// hash are the values a ConsumerKey store persists.
func GenerateKey() (raw, preview, hash string, err error) {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", "", "", fmt.Errorf("storage: generate key: %w", err)
	}
	raw = "sk-" + hex.EncodeToString(buf[:])
	return raw, PreviewOf(raw), HashKey(raw), nil
}

// PreviewOf returns the persisted preview of a raw key: its leading
// keyPreviewLeadLen characters concatenated with its trailing
// keyPreviewTrailLen characters (the whole key if it's shorter than that
// combined length). This is never enough to reconstruct the key — it exists
// so a masked "sk-abc123...xyz789" can be rendered without ever persisting
// (or re-exposing) the full plaintext.
func PreviewOf(raw string) string {
	if len(raw) <= keyPreviewLeadLen+keyPreviewTrailLen {
		return raw
	}
	return raw[:keyPreviewLeadLen] + raw[len(raw)-keyPreviewTrailLen:]
}

// HashKey returns the persisted hash of a raw key (hex-encoded SHA-256). API
// keys are high-entropy random strings, so a fast hash is sufficient — unlike
// user passwords, they don't need a slow, salted KDF to resist brute force.
func HashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
