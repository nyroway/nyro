// Package vendor implements the Vendor trait + 7-step pipeline + per-vendor
// auth/URL/hooks. Ported from crates/nyro-core/src/provider/{vendor,vendor_ext,
// common/pipeline,common/openai_compat,registry,*/mod}.rs.
//
// The Vendor interface is the central abstraction over upstream providers.
// Each vendor defines its auth headers, URL construction, and optional
// pre/post encode/parse hooks. The pipeline (BuildRequest/ParseResponse) wires
// them together with the egress codec.
package vendor

import (
	"encoding/json"

	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// VendorCtx is the lightweight context passed to extension hooks.
type VendorCtx struct {
	APIKey      string
	ActualModel string
}

// ProviderCtx is the full runtime context for orchestration methods.
type ProviderCtx struct {
	Provider    VendorProvider
	APIKey      string
	ActualModel string
}

// VendorProvider is the subset of storage.Provider the pipeline needs.
type VendorProvider struct {
	ID       string
	Vendor   string
	Protocol string
	BaseURL  string
	AuthMode string
}

// Vendor is the per-provider abstraction (auth, URL, hooks). Most vendors
// embed OpenAICompatVendor and override nothing.
type Vendor interface {
	// ID is the canonical vendor key ("openai", "anthropic", "google", etc.).
	ID() string
	// AuthHeaders returns the upstream auth headers for this vendor.
	AuthHeaders(ctx *VendorCtx) map[string]string
	// BuildURL constructs the upstream URL from the base URL + egress path.
	BuildURL(ctx *VendorCtx, baseURL, path string) string
	// PreEncode mutates the IR request before the egress codec encodes it.
	PreEncode(ctx *VendorCtx, req *ir.AiRequest) error
	// PostEncode mutates the encoded body + headers after the codec.
	PostEncode(ctx *VendorCtx, body json.RawMessage, headers map[string]string) (json.RawMessage, map[string]string, error)
	// PreParse mutates the raw upstream response before the codec decodes it.
	PreParse(ctx *VendorCtx, body json.RawMessage) (json.RawMessage, error)
	// PostParse mutates the decoded AiResponse.
	PostParse(ctx *VendorCtx, resp *ir.AiResponse) error
	// DeclaredRequestMutations: false → passthrough eligible (no hooks mutate).
	DeclaredRequestMutations() bool
	// DeclaredResponseMutations: false → passthrough eligible.
	DeclaredResponseMutations() bool
	// MapError classifies a non-2xx upstream error.
	MapError(status int, body json.RawMessage) *ir.AiError
}

// BaseVendor provides default no-op implementations for all hooks. Concrete
// vendors embed this and override only what they need.
type BaseVendor struct{}

func (BaseVendor) PreEncode(_ *VendorCtx, _ *ir.AiRequest) error { return nil }
func (BaseVendor) PostEncode(_ *VendorCtx, body json.RawMessage, h map[string]string) (json.RawMessage, map[string]string, error) {
	return body, h, nil
}
func (BaseVendor) PreParse(_ *VendorCtx, body json.RawMessage) (json.RawMessage, error) {
	return body, nil
}
func (BaseVendor) PostParse(_ *VendorCtx, _ *ir.AiResponse) error { return nil }
func (BaseVendor) DeclaredRequestMutations() bool                 { return true }
func (BaseVendor) DeclaredResponseMutations() bool                { return true }
