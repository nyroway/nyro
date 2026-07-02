package provider

import "strings"

// BuildURL joins a provider base URL with an egress path, stripping a
// duplicate version segment if both carry one (e.g. base "…/v1" + path
// "/v1/chat/completions" → "…/v1/chat/completions", not "…/v1/v1/…").
func BuildURL(baseURL, path string) string {
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
	return isVersionSegment(baseURL[idx+1:])
}

// isVersionSegment recognizes v\d+(\w*): v1, v4, v1beta, v2alpha. Rejects v,
// vNext, vendor.
func isVersionSegment(s string) bool {
	if len(s) < 2 || s[0] != 'v' || s[1] < '0' || s[1] > '9' {
		return false
	}
	return true
}
