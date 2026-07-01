package provider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/nyroway/nyro/go/internal/provider"
)

func TestNoopAuthenticatorLeavesRequestUnchanged(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	noop := provider.NoopAuthenticator{}
	if err := noop.Apply(context.Background(), req); err != nil {
		t.Fatalf("Noop Apply: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Noop set Authorization = %q, want empty", got)
	}
}

func TestNewBearerAuthenticatorRequiresAPIKey(t *testing.T) {
	if _, err := provider.NewBearerAuthenticator(json.RawMessage(`{}`)); err == nil {
		t.Error("NewBearerAuthenticator with empty api_key should error")
	}
}

func TestNewBearerAuthenticatorAppliesBearerToken(t *testing.T) {
	auth, err := provider.NewBearerAuthenticator(json.RawMessage(`{"api_key":"sk-x"}`))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, "https://example.com", nil)
	if err := auth.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer sk-x" {
		t.Errorf("Authorization = %q, want Bearer sk-x", got)
	}
}
