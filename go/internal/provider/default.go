package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

// DefaultProvider provides common Provider behavior for providers that only
// need to override specific hooks such as authentication or request
// preparation. It aggregates one Definition for static data and returns a
// no-op authenticator by default.
type DefaultProvider struct {
	Def Definition
}

// Definition returns the provider's static description.
func (p DefaultProvider) Definition() Definition { return p.Def }

// NewAuthenticator returns a no-op authenticator by default; providers
// override it to apply real authentication or request signing.
func (p DefaultProvider) NewAuthenticator(context.Context, UpstreamRuntime) (Authenticator, error) {
	return NoopAuthenticator{}, nil
}

// NoopAuthenticator leaves outbound requests unchanged.
type NoopAuthenticator struct{}

func (NoopAuthenticator) Apply(context.Context, *http.Request) error { return nil }

type apiKeyCredentials struct {
	APIKey string `json:"api_key"`
}

type bearerAuthenticator struct {
	apiKey string
}

// NewBearerAuthenticator returns an authenticator that writes an Authorization
// bearer token from a credentials JSON object containing api_key.
func NewBearerAuthenticator(credentials json.RawMessage) (Authenticator, error) {
	var c apiKeyCredentials
	if err := json.Unmarshal(credentials, &c); err != nil {
		return nil, err
	}
	if c.APIKey == "" {
		return nil, errors.New("provider: api_key is required")
	}
	return bearerAuthenticator{apiKey: c.APIKey}, nil
}

func (a bearerAuthenticator) Apply(_ context.Context, req *http.Request) error {
	req.Header.Set("Authorization", "Bearer "+a.apiKey)
	return nil
}
