package drivers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/nyroway/nyro/go/internal/auth"
	"github.com/nyroway/nyro/go/internal/provider"
	"github.com/nyroway/nyro/go/internal/storage"
)

// CodexDriver implements auth.AuthDriver for the OpenAI/Codex device-code flow.
// Unlike Claude's PKCE flow, Codex uses OAuth device-code (polling) + form-urlencoded
// token requests. Ported from auth/drivers/openai.rs.
type CodexDriver struct {
	clientID     string
	authorizeURL string
	tokenURL     string
	scope        string
}

func NewCodexDriver() *CodexDriver {
	preset, ok := provider.FindPreset("openai")
	if !ok {
		return &CodexDriver{}
	}
	for _, ch := range preset.Channels {
		if ch.ID == "codex" && ch.OAuth != nil {
			return &CodexDriver{
				clientID:     ch.OAuth.ClientID,
				authorizeURL: ch.OAuth.AuthorizeURL,
				tokenURL:     ch.OAuth.TokenURL,
				scope:        ch.OAuth.Scope,
			}
		}
	}
	return &CodexDriver{}
}

func (d *CodexDriver) Name() string            { return "codex" }
func (d *CodexDriver) Scheme() auth.AuthScheme { return auth.SchemeOAuthDeviceCode }

// Start initiates the device-code flow by POSTing to the authorize endpoint
// (form-urlencoded, unlike Claude's JSON).
func (d *CodexDriver) Start(_ context.Context, _ string) (auth.StartResult, error) {
	form := url.Values{
		"client_id":                  {d.clientID},
		"scope":                      {d.scope},
		"id_token_add_organizations": {"true"},
		"codex_cli_simplified_flow":  {"true"},
	}

	req, _ := http.NewRequest("POST", d.authorizeURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return auth.StartResult{}, fmt.Errorf("device-code start: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return auth.StartResult{}, fmt.Errorf("device-code start failed (%d): %s", resp.StatusCode, string(raw))
	}

	var dc struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURI         string `json:"verification_uri"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		ExpiresIn               int    `json:"expires_in"`
		Interval                int    `json:"interval"`
	}
	if err := json.Unmarshal(raw, &dc); err != nil {
		return auth.StartResult{}, fmt.Errorf("parse device-code response: %w", err)
	}

	start := auth.StartResult{
		DeviceCode: dc.DeviceCode,
		UserCode:   dc.UserCode,
	}
	if dc.VerificationURIComplete != "" {
		start.AuthURL = dc.VerificationURIComplete
	} else if dc.VerificationURI != "" {
		start.AuthURL = dc.VerificationURI
	}
	if dc.ExpiresIn > 0 {
		start.ExpiresAt = expiresAtAfter(dc.ExpiresIn)
	}
	return start, nil
}

// PollWithDeviceCode checks the device-code authorization status with the
// device_code from Start. Returns a credential when authorized.
func (d *CodexDriver) PollWithDeviceCode(_ context.Context, deviceCode string) (auth.SessionStatusResult, error) {
	if deviceCode == "" {
		return auth.SessionStatusResult{Status: auth.StatusError, Error: "no device_code"}, nil
	}

	form := url.Values{
		"client_id":   {d.clientID},
		"device_code": {deviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	}

	req, _ := http.NewRequest("POST", d.tokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return auth.SessionStatusResult{Status: auth.StatusError, Error: err.Error()}, nil
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode == http.StatusOK {
		var tok struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
			IDToken      string `json:"id_token"`
		}
		if err := json.Unmarshal(raw, &tok); err != nil {
			return auth.SessionStatusResult{Status: auth.StatusError, Error: "parse error"}, nil
		}
		return auth.SessionStatusResult{
			Status: auth.StatusComplete,
			Credential: &storage.OAuthCredential{
				DriverKey:    "codex",
				Scheme:       string(auth.SchemeOAuthDeviceCode),
				AccessToken:  tok.AccessToken,
				RefreshToken: tok.RefreshToken,
				ExpiresAt:    expiresAtAfter(tok.ExpiresIn),
				Status:       "connected",
			},
		}, nil
	}

	// Check for "authorization_pending" error.
	var errResp struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &errResp)
	if errResp.Error == "authorization_pending" || errResp.Error == "slow_down" {
		return auth.SessionStatusResult{Status: auth.StatusPending}, nil
	}
	if errResp.Error == "expired_token" {
		return auth.SessionStatusResult{Status: auth.StatusError, Error: "device code expired"}, nil
	}
	return auth.SessionStatusResult{Status: auth.StatusError, Error: fmt.Sprintf("poll failed: %s", string(raw))}, nil
}

// Exchange is not used for device-code flow (Poll handles authorization).
func (d *CodexDriver) Exchange(_ context.Context, _ string, _, _, _ string) (storage.OAuthCredential, error) {
	return storage.OAuthCredential{}, errors.New("Exchange not supported for device-code flow; use Poll")
}

// Refresh exchanges a refresh_token for a new access_token (form-urlencoded,
// scope re-sent).
func (d *CodexDriver) Refresh(_ context.Context, cred storage.OAuthCredential) (storage.OAuthCredential, error) {
	if cred.RefreshToken == "" {
		return storage.OAuthCredential{}, errors.New("no refresh_token")
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {d.clientID},
		"refresh_token": {cred.RefreshToken},
		"scope":         {d.scope},
	}

	req, _ := http.NewRequest("POST", d.tokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

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

var _ auth.AuthDriver = (*CodexDriver)(nil)
