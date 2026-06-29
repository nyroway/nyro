package proxy

import "strings"

// authHeadersFor returns the upstream auth headers for a provider protocol using
// a static credential (API key or OAuth access token). This is the minimal
// per-provider Vendor layer (P3d expands it with full Vendor hooks). Ported
// conceptually from provider/common pipeline auth-header step.
func authHeadersFor(protocol, credential string) map[string]string {
	if credential == "" {
		return nil
	}
	switch protocol {
	case "google-gemini":
		return map[string]string{"x-goog-api-key": credential}
	case "anthropic-messages":
		return map[string]string{"x-api-key": credential}
	default:
		return map[string]string{"Authorization": "Bearer " + credential}
	}
}

// buildUpstreamURL joins a provider base URL with an egress path, stripping a
// duplicate version segment if both carry one (e.g. base "…/v1" + path
// "/v1/chat/completions" → "…/v1/chat/completions", not "…/v1/v1/…"). Ported
// from provider/common/openai_compat.rs openai_build_url.
func buildUpstreamURL(baseURL, path string) string {
	trimmed := strings.TrimRight(baseURL, "/")
	idx := strings.LastIndex(trimmed, "/")
	if idx < 0 {
		return baseURL + path
	}
	lastSeg := trimmed[idx+1:]
	if isVersionSegment(lastSeg) {
		prefix := "/" + lastSeg + "/"
		if strings.HasPrefix(path, prefix) {
			return baseURL + "/" + path[len(prefix):]
		}
	}
	return baseURL + path
}

// isVersionSegment reports whether s looks like a version path segment
// (v1, v1beta, v2alpha). Rejects "v", "vNext", "vendor". Ported from
// openai_compat.rs is_version_segment.
func isVersionSegment(s string) bool {
	return len(s) > 1 && s[0] == 'v' && s[1] >= '0' && s[1] <= '9'
}
