package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

// GenerateCodeVerifier generates a cryptographically random PKCE code_verifier
// (43-128 chars, RFC 7636 §4.1). We use 32 random bytes → 43 base64url chars.
func GenerateCodeVerifier() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// GenerateCodeChallenge computes the S256 code_challenge from a code_verifier
// (SHA-256, base64url, no padding).
func GenerateCodeChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

// GenerateState generates a random OAuth state parameter (32 hex chars).
func GenerateState() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ParseCallback extracts the authorization code and state from a callback input.
// Accepts: a full URL, a query string (code=…&state=…), or the Claude
// `code=true` bare form `<code>#<state>`. Ported from auth/drivers/shared.rs.
func ParseCallback(input string) (code, state string, err error) {
	input = strings.TrimSpace(input)

	// Full URL with host.
	if u, parseErr := url.Parse(input); parseErr == nil && u.Host != "" {
		code = u.Query().Get("code")
		state = u.Query().Get("state")
		// Claude `code=true` flow returns the code in the fragment as <code>#<state>.
		if code == "" && u.Fragment != "" {
			if idx := strings.Index(u.Fragment, "#"); idx >= 0 {
				code = u.Fragment[:idx]
				state = u.Fragment[idx+1:]
			} else {
				code = u.Fragment
			}
		}
	}

	// Query string form.
	if code == "" {
		if vals, parseErr := url.ParseQuery(input); parseErr == nil {
			code = vals.Get("code")
			state = vals.Get("state")
		}
	}

	// Bare form: <code>#<state>.
	if code == "" {
		if idx := strings.Index(input, "#"); idx > 0 {
			code = input[:idx]
			state = input[idx+1:]
		}
	}

	if code == "" {
		return "", "", errors.New("no authorization code found in callback input")
	}
	return code, state, nil
}
