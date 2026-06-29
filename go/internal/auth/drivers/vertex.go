package drivers

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nyroway/nyro/go/internal/auth"
	"github.com/nyroway/nyro/go/internal/storage"
)

// VertexDriver handles Google Vertex AI authentication via service-account JWT
// exchange. Ported from provider/vertexai/mod.rs vertex_access_token.
type VertexDriver struct {
	mu    sync.Mutex
	cache map[string]*cachedToken // keyed by hash of SA JSON
}

type cachedToken struct {
	accessToken string
	expires     time.Time
}

type serviceAccountJSON struct {
	Type        string `json:"type"`
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
	ProjectID   string `json:"project_id"`
}

func NewVertexDriver() *VertexDriver {
	return &VertexDriver{cache: map[string]*cachedToken{}}
}

func (d *VertexDriver) Name() string            { return "vertexai" }
func (d *VertexDriver) Scheme() auth.AuthScheme { return auth.SchemeAPIKey }

func (d *VertexDriver) Start(_ context.Context, _ string) (auth.StartResult, error) {
	return auth.StartResult{}, errors.New("Vertex uses static service-account JSON, not interactive OAuth")
}

func (d *VertexDriver) Exchange(_ context.Context, _ string, _, _, _ string) (storage.OAuthCredential, error) {
	return storage.OAuthCredential{}, errors.New("Exchange not supported for Vertex; use Refresh with SA JSON")
}

func (d *VertexDriver) Refresh(ctx context.Context, cred storage.OAuthCredential) (storage.OAuthCredential, error) {
	credJSON := cred.AccessToken
	if !looksLikeSAJSON(credJSON) {
		return storage.OAuthCredential{}, errors.New("Vertex: credential must be a service-account JSON")
	}

	d.mu.Lock()
	cached, ok := d.cache[hashKey(credJSON)]
	d.mu.Unlock()
	if ok && time.Now().Before(cached.expires.Add(-2*time.Minute)) {
		out := cred
		out.AccessToken = cached.accessToken
		out.Status = "connected"
		return out, nil
	}

	var sa serviceAccountJSON
	if err := json.Unmarshal([]byte(credJSON), &sa); err != nil {
		return storage.OAuthCredential{}, fmt.Errorf("Vertex: parse SA JSON: %w", err)
	}

	tokenURI := sa.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}

	token, err := exchangeSAToken(ctx, sa, tokenURI, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return storage.OAuthCredential{}, err
	}

	d.mu.Lock()
	d.cache[hashKey(credJSON)] = &cachedToken{
		accessToken: token.AccessToken,
		expires:     token.Expiry,
	}
	d.mu.Unlock()

	out := cred
	out.AccessToken = token.AccessToken
	out.ExpiresAt = token.Expiry.UTC().Format(time.RFC3339)
	out.Status = "connected"
	return out, nil
}

var _ auth.AuthDriver = (*VertexDriver)(nil)

func looksLikeSAJSON(s string) bool {
	return strings.Contains(s, "client_email") && strings.Contains(s, "private_key")
}

func hashKey(s string) string {
	h := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
	TokenType   string `json:"token_type"`
}

type tokenWithExpiry struct {
	tokenResponse
	Expiry time.Time
}

func exchangeSAToken(ctx context.Context, sa serviceAccountJSON, tokenURI, scope string) (*tokenWithExpiry, error) {
	privateKey, err := parseRSAPrivateKey(sa.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("Vertex: parse RSA private key: %w", err)
	}

	assertion, err := buildJWTAssertion(sa, privateKey, tokenURI, scope)
	if err != nil {
		return nil, fmt.Errorf("Vertex: build JWT assertion: %w", err)
	}

	body := "grant_type=urn:ietf:params:oauth:grant-type:jwt-bearer&assertion=" + assertion
	req, _ := http.NewRequestWithContext(ctx, "POST", tokenURI, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Vertex: token exchange HTTP: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Vertex: token exchange failed (%d): %s", resp.StatusCode, string(raw))
	}

	var tok tokenResponse
	if err := json.Unmarshal(raw, &tok); err != nil {
		return nil, fmt.Errorf("Vertex: parse token response: %w", err)
	}
	expiresIn := tok.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}

	return &tokenWithExpiry{
		tokenResponse: tok,
		Expiry:        time.Now().Add(time.Duration(expiresIn) * time.Second),
	}, nil
}

func parseRSAPrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("no PEM block in private_key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse private key: %w", err)
		}
	}
	rsaKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return rsaKey, nil
}

func buildJWTAssertion(sa serviceAccountJSON, key *rsa.PrivateKey, tokenURI, scope string) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	now := time.Now().Unix()
	claims := map[string]any{
		"iss":   sa.ClientEmail,
		"scope": scope,
		"aud":   tokenURI,
		"iat":   now,
		"exp":   now + 3600,
	}

	headerJSON, _ := json.Marshal(header)
	claimsJSON, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)

	hash := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
