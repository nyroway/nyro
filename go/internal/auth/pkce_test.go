package auth

import (
	"testing"
)

func TestPKCEVerifierChallenge(t *testing.T) {
	t.Parallel()
	v := GenerateCodeVerifier()
	if len(v) < 43 || len(v) > 128 {
		t.Errorf("verifier length %d outside RFC 7636 range [43,128]", len(v))
	}
	c := GenerateCodeChallenge(v)
	if len(c) < 43 {
		t.Errorf("challenge too short: %d", len(c))
	}
	// Same verifier → same challenge (deterministic).
	if GenerateCodeChallenge(v) != c {
		t.Error("challenge should be deterministic for same verifier")
	}
	// Different verifier → different challenge.
	v2 := GenerateCodeVerifier()
	if v == v2 {
		t.Error("verifiers should differ")
	}
}

func TestParseCallback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		input string
		code  string
		state string
		ok    bool
	}{
		{"full URL", "https://example.com/cb?code=abc&state=xyz", "abc", "xyz", true},
		{"query string", "code=abc&state=xyz", "abc", "xyz", true},
		{"Claude bare form", "abc#xyz", "abc", "xyz", true},
		{"Claude bare no state", "abc#", "abc", "", true},
		{"no code", "state=xyz", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, state, err := ParseCallback(tc.input)
			if tc.ok {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if code != tc.code {
					t.Errorf("code=%q want %q", code, tc.code)
				}
				if state != tc.state {
					t.Errorf("state=%q want %q", state, tc.state)
				}
			} else {
				if err == nil {
					t.Error("expected error for empty code")
				}
			}
		})
	}
}
