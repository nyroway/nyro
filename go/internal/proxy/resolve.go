package proxy

import (
	"encoding/json"

	"github.com/nyroway/nyro/go/internal/storage"
)

// resolveCredential returns the static API key for an api-key upstream, read
// from CredentialsJSON. The OAuth credential resolution / driver refresh
// infrastructure was removed; cloud provider auth (Vertex SA, Bedrock SigV4,
// Azure AD) will be rebuilt inside provider.NewAuthenticator via the vendor
// SDKs — this is a minimal extraction that keeps the existing string-header
// injection path (authHeadersFor) working for api-key upstreams in the meantime.
func (g *Gateway) resolveCredential(u storage.Upstream) string {
	var c struct {
		APIKey string `json:"api_key"`
	}
	_ = json.Unmarshal(u.CredentialsJSON, &c)
	return c.APIKey
}
