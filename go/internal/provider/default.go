package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

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
