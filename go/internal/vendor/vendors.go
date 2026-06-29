package vendor

import (
	"encoding/json"
	"strings"

	"github.com/nyroway/nyro/go/internal/protocol/ids"
	"github.com/nyroway/nyro/go/internal/protocol/ir"
)

// init registers all vendors. Ported from each provider/*/mod.rs.
func init() {
	g := Global()

	// ── OpenAI-compatible vendors (GenericOpenAICompatibleAdapter) ──
	for _, id := range []string{
		"openai", "deepseek", "moonshotai", "zhipuai", "xai", "openrouter",
		"nvidia", "minimax", "zai", "ollama", "custom",
	} {
		v := NewOpenAICompatVendor(id)
		g.Register(v, ids.ProtocolOpenAICompatible)
	}

	// Anthropic: x-api-key + anthropic-version header, no mutations.
	g.Register(&AnthropicVendor{}, ids.ProtocolAnthropicMessages)

	// Google: x-goog-api-key, no mutations.
	g.Register(&GoogleVendor{}, ids.ProtocolGoogleGemini)
}

// AnthropicVendor implements the Anthropic-specific auth (x-api-key +
// anthropic-version header) with no request/response mutations (passthrough
// eligible). Ported from provider/anthropic/mod.rs.
type AnthropicVendor struct {
	BaseVendor
}

func (AnthropicVendor) ID() string { return "anthropic" }

func (AnthropicVendor) AuthHeaders(ctx *VendorCtx) map[string]string {
	h := map[string]string{"anthropic-version": "2023-06-01"}
	if ctx.APIKey != "" {
		h["x-api-key"] = ctx.APIKey
	}
	return h
}

func (AnthropicVendor) BuildURL(_ *VendorCtx, baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + path
}

func (AnthropicVendor) DeclaredRequestMutations() bool  { return false }
func (AnthropicVendor) DeclaredResponseMutations() bool { return false }

func (AnthropicVendor) MapError(status int, body json.RawMessage) *ir.AiError {
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
		msg = "anthropic upstream error"
	}
	return ir.AiErrorFromStatus(uint16(status), msg).WithRaw(body)
}

// GoogleVendor implements Google Gemini auth (x-goog-api-key) with no mutations.
// Ported from provider/google/mod.rs.
type GoogleVendor struct {
	BaseVendor
}

func (GoogleVendor) ID() string { return "google" }

func (GoogleVendor) AuthHeaders(ctx *VendorCtx) map[string]string {
	if ctx.APIKey == "" {
		return nil
	}
	return map[string]string{"x-goog-api-key": ctx.APIKey}
}

func (GoogleVendor) BuildURL(_ *VendorCtx, baseURL, path string) string {
	return strings.TrimRight(baseURL, "/") + path
}

func (GoogleVendor) DeclaredRequestMutations() bool  { return false }
func (GoogleVendor) DeclaredResponseMutations() bool { return false }

func (GoogleVendor) MapError(status int, body json.RawMessage) *ir.AiError {
	return ir.AiErrorFromStatus(uint16(status), "google upstream error").WithRaw(body)
}
