package vendor

import (
	"encoding/json"
	"strings"

	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// OpenAICompatVendor is the generic vendor used by all OpenAI-compatible
// providers (deepseek, moonshot, zhipu, xai, openrouter, nvidia, minimax,
// zai, ollama, custom). Provides Bearer auth + version-aware URL.
// Ported from provider/common/openai_compat.rs GenericOpenAICompatibleAdapter.
type OpenAICompatVendor struct {
	BaseVendor
	id string
}

func NewOpenAICompatVendor(id string) *OpenAICompatVendor {
	return &OpenAICompatVendor{id: id}
}

func (v *OpenAICompatVendor) ID() string { return v.id }

func (v *OpenAICompatVendor) AuthHeaders(ctx *VendorCtx) map[string]string {
	if ctx.APIKey == "" {
		return nil
	}
	return map[string]string{"Authorization": "Bearer " + ctx.APIKey}
}

func (v *OpenAICompatVendor) BuildURL(_ *VendorCtx, baseURL, path string) string {
	return OpenAIBuildURL(baseURL, path)
}

func (v *OpenAICompatVendor) DeclaredRequestMutations() bool  { return false }
func (v *OpenAICompatVendor) DeclaredResponseMutations() bool { return false }

func (v *OpenAICompatVendor) MapError(status int, body json.RawMessage) *ir.AiError {
	msg := ""
	if len(body) > 0 {
		var obj map[string]any
		if json.Unmarshal(body, &obj) == nil {
			if e, ok := obj["error"].(map[string]any); ok {
				if m, ok := e["message"].(string); ok {
					msg = m
				}
			}
		}
	}
	if msg == "" {
		msg = "upstream HTTP error"
	}
	return ir.AiErrorFromStatus(uint16(status), msg).WithRaw(body)
}

// OpenAIBuildURL constructs the upstream URL, stripping duplicate version
// segments (e.g. base "…/v1" + path "/v1/chat/completions" → "…/v1/chat/completions").
func OpenAIBuildURL(baseURL, path string) string {
	base := strings.TrimRight(baseURL, "/")
	if endsWithVersionSegment(base) && strings.HasPrefix(path, "/v1/") {
		return base + path[3:]
	}
	return base + path
}

// endsWithVersionSegment reports whether the URL's last path segment is a
// version segment (v1, v4, v1beta, etc.).
func endsWithVersionSegment(baseURL string) bool {
	idx := strings.LastIndex(strings.TrimRight(baseURL, "/"), "/")
	if idx < 0 {
		return false
	}
	last := baseURL[idx+1:]
	return isVersionSegment(last)
}

// isVersionSegment recognizes v\d+(\w*): v1, v4, v1beta, v2alpha. Rejects v,
// vNext, vendor.
func isVersionSegment(s string) bool {
	if len(s) < 2 || s[0] != 'v' || s[1] < '0' || s[1] > '9' {
		return false
	}
	return true
}
