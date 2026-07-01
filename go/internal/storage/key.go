package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// keyPrefixLen is the number of raw-key characters persisted as KeyPrefix, used
// to narrow candidates before a hash compare in KeyAuthStore.FindKey.
const keyPrefixLen = 12

// GenerateKey creates a new random API key. raw is returned to the caller
// exactly once (at creation time) and must never be persisted; prefix and hash
// are the values a ConsumerKey store persists.
func GenerateKey() (raw, prefix, hash string, err error) {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", "", "", fmt.Errorf("storage: generate key: %w", err)
	}
	raw = "nyro_" + hex.EncodeToString(buf[:])
	return raw, PrefixOf(raw), HashKey(raw), nil
}

// PrefixOf returns the persisted prefix of a raw key (the whole key if shorter
// than the prefix length).
func PrefixOf(raw string) string {
	if len(raw) <= keyPrefixLen {
		return raw
	}
	return raw[:keyPrefixLen]
}

// HashKey returns the persisted hash of a raw key (hex-encoded SHA-256). API
// keys are high-entropy random strings, so a fast hash is sufficient — unlike
// user passwords, they don't need a slow, salted KDF to resist brute force.
func HashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
