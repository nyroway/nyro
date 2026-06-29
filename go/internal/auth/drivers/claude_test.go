package drivers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClaudeStartPopulatesState asserts the PKCE flow generates and carries a
// state value: it must be in StartResult.State (so the complete step can echo
// it) AND embedded in the authorize URL (CSRF). The prior session dropped it.
func TestClaudeStartPopulatesState(t *testing.T) {
	d := &ClaudeDriver{
		clientID:     "test-client",
		authorizeURL: "https://authorize.example/auth",
		redirectURI:  "https://app.example/cb",
		scope:        "org:create_api_key",
	}
	res, err := d.Start(context.Background(), "")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if res.State == "" {
		t.Fatal("Start: StartResult.State is empty; PKCE flow must carry state for CSRF + Anthropic echo")
	}
	if !strings.Contains(res.AuthURL, "state="+res.State) {
		t.Errorf("Start: AuthURL does not embed state=%q; got %s", res.State, res.AuthURL)
	}
}

// TestClaudeExchangeSendsState asserts the token exchange echoes the state
// back in the request body. Anthropic's token endpoint deviates from RFC 6749:
// it requires state in the JSON body or returns 400 (per the Rust reference).
func TestClaudeExchangeSendsState(t *testing.T) {
	var captured string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		captured = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"at","refresh_token":"rt","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer ts.Close()

	d := &ClaudeDriver{clientID: "test-client", tokenURL: ts.URL}
	if _, err := d.Exchange(context.Background(), "", "auth-code", "verifier", "csrf-state-123"); err != nil {
		t.Fatalf("Exchange: %v", err)
	}

	var body map[string]string
	if err := json.Unmarshal([]byte(captured), &body); err != nil {
		t.Fatalf("parse token body: %v\nraw: %s", err, captured)
	}
	if body["state"] != "csrf-state-123" {
		t.Errorf("token body state = %q, want %q (Anthropic requires state echoed back)", body["state"], "csrf-state-123")
	}
}
