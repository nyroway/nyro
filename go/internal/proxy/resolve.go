package proxy

import (
	"github.com/nyroway/nyro/go/internal/storage"
)

// resolveCredential returns the static API key for an api-key provider. The
// OAuth credential resolution / driver refresh infrastructure was removed; cloud
// provider auth (Vertex SA, Bedrock SigV4, Azure AD) will be rebuilt inside
// provider.NewAuthenticator via the vendor SDKs.
func (g *Gateway) resolveCredential(p storage.Provider) string {
	return p.APIKey
}
