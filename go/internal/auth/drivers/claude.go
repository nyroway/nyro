// Package drivers implements the concrete OAuth drivers for upstream providers.
// This file implements the Claude (Anthropic) PKCE authorization-code driver.
package drivers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nyroway/nyro/go/internal/auth"
	"github.com/nyroway/nyro/go/internal/provider"
	"github.com/nyroway/nyro/go/internal/storage"
)

// ClaudeDriver implements auth.AuthDriver for the Anthropic Claude Code OAuth
// flow (PKCE + claude-cli impersonation). Ported from auth/drivers/claude.rs.
type ClaudeDriver struct {
	clientID     string
	redirectURI  string
	tokenURL     string
	authorizeURL string
	scope        string
}

// NewClaudeDriver builds a ClaudeDriver from the vendor preset's claude-code
// channel OAuth config.
func NewClaudeDriver() *ClaudeDriver {
	preset, ok := provider.FindPreset("anthropic")
	if !ok {
		return &ClaudeDriver{}
	}
	for _, ch := range preset.Channels {
		if ch.ID == "claude-code" && ch.OAuth != nil {
			return &ClaudeDriver{
				clientID:     ch.OAuth.ClientID,
				redirectURI:  ch.OAuth.RedirectURI,
				tokenURL:     ch.OAuth.TokenURL,
				authorizeURL: ch.OAuth.AuthorizeURL,
				scope:        ch.OAuth.Scope,
			}
		}
	}
	return &ClaudeDriver{}
}

func (d *ClaudeDriver) Name() string            { return "claude-code" }
func (d *ClaudeDriver) Scheme() auth.AuthScheme { return auth.SchemeOAuthAuthCode }

// Start generates the PKCE pair + authorize URL. The verifier is stored in
// the session (by the admin layer) and consumed in Exchange.
func (d *ClaudeDriver) Start(_ context.Context, _ string) (auth.StartResult, error) {
	verifier := auth.GenerateCodeVerifier()
	challenge := auth.GenerateCodeChallenge(verifier)
	state := auth.GenerateState()

	authURL := fmt.Sprintf("%s?response_type=code&client_id=%s&redirect_uri=%s&scope=%s&code_challenge=%s&code_challenge_method=S256&state=%s&code=true",
		d.authorizeURL, d.clientID, d.redirectURI, d.scope, challenge, state)

	return auth.StartResult{
		AuthURL:  authURL,
		Verifier: verifier,
		State:    state,
	}, nil
}

// Exchange completes the authorization-code flow: POSTs to the token endpoint
// with the code + PKCE verifier and returns the stored credential.
func (d *ClaudeDriver) Exchange(_ context.Context, _ string, code, verifier, state string) (storage.OAuthCredential, error) {
	if code == "" {
		return storage.OAuthCredential{}, errors.New("empty authorization code")
	}
	if verifier == "" {
		return storage.OAuthCredential{}, errors.New("empty PKCE verifier")
	}

	body := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     d.clientID,
		"code":          code,
		"redirect_uri":  d.redirectURI,
		"code_verifier": verifier,
		"state":         state,
	}
	// Anthropic deviates from RFC 6749: the token endpoint requires state echoed
	// back in the JSON body, otherwise it returns 400.

	jsonBody, _ := json.Marshal(body)

	req, _ := http.NewRequest("POST", d.tokenURL, bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	addClaudeCLIHeaders(req)

	resp, err := httpClient.Do(req)
	if err != nil {
		return storage.OAuthCredential{}, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return storage.OAuthCredential{}, fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, string(raw))
	}

	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		return storage.OAuthCredential{}, fmt.Errorf("parse token response: %w", err)
	}

	expiresAt := expiresAtAfter(tok.ExpiresIn)
	return storage.OAuthCredential{
		DriverKey:    "claude-code",
		Scheme:       string(auth.SchemeOAuthAuthCode),
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    expiresAt,
		Status:       "connected",
	}, nil
}

// Refresh exchanges a refresh_token for a new access_token.
func (d *ClaudeDriver) Refresh(_ context.Context, cred storage.OAuthCredential) (storage.OAuthCredential, error) {
	if cred.RefreshToken == "" {
		return storage.OAuthCredential{}, errors.New("no refresh_token")
	}

	body := map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     d.clientID,
		"refresh_token": cred.RefreshToken,
	}
	jsonBody, _ := json.Marshal(body)

	req, _ := http.NewRequest("POST", d.tokenURL, bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	addClaudeCLIHeaders(req)

	resp, err := httpClient.Do(req)
	if err != nil {
		return storage.OAuthCredential{}, fmt.Errorf("token refresh: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return storage.OAuthCredential{}, fmt.Errorf("token refresh failed (%d): %s", resp.StatusCode, string(raw))
	}

	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		return storage.OAuthCredential{}, fmt.Errorf("parse refresh response: %w", err)
	}

	newCred := cred
	newCred.AccessToken = tok.AccessToken
	if tok.RefreshToken != "" {
		newCred.RefreshToken = tok.RefreshToken
	}
	newCred.ExpiresAt = expiresAtAfter(tok.ExpiresIn)
	newCred.Status = "connected"
	newCred.LastError = ""
	return newCred, nil
}

// addClaudeCLIHeaders sets the CLI impersonation headers required by Anthropic's
// OAuth surface (the OAuth-beta endpoint gates on the CLI version string).
func addClaudeCLIHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "claude-cli/1.0.98 (external, cli)")
	req.Header.Set("Referer", "https://claude.ai/")
	req.Header.Set("Origin", "https://claude.ai")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
}

// httpClient is the shared HTTP client for OAuth requests.
var httpClient = &http.Client{Timeout: 30 * time.Second}

// expiresAtAfter computes an RFC3339 expiry from an expires_in seconds value,
// defaulting to 3600s when absent/zero.
func expiresAtAfter(expiresIn int) string {
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	return time.Now().Add(time.Duration(expiresIn) * time.Second).UTC().Format(time.RFC3339)
}

// Verify interface compliance.
var _ auth.AuthDriver = (*ClaudeDriver)(nil)
