package provider

import "strings"

func BuildURL(baseURL, path string) string {
	base := strings.TrimRight(baseURL, "/")
	if endsWithVersionSegment(base) && strings.HasPrefix(path, "/v1/") {
		return base + path[3:]
	}
	return base + path
}

func endsWithVersionSegment(baseURL string) bool {
	index := strings.LastIndex(strings.TrimRight(baseURL, "/"), "/")
	return index >= 0 && isVersionSegment(baseURL[index+1:])
}

func isVersionSegment(value string) bool {
	return len(value) >= 2 && value[0] == 'v' && value[1] >= '0' && value[1] <= '9'
}
