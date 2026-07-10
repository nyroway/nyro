package admin

import (
	"fmt"
	"net/url"
	"strings"
)

const publicGatewayURLSettingKey = "gateway.public_url"

func normalizeSettingValue(key, value string) (string, error) {
	if key != publicGatewayURLSettingKey {
		return value, nil
	}
	return normalizePublicGatewayURL(value)
}

func normalizePublicGatewayURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}

	u, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("public gateway URL must be a valid absolute HTTP(S) URL")
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("public gateway URL must be an absolute HTTP(S) URL")
	}
	if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(value, "#") {
		return "", fmt.Errorf("public gateway URL must not include credentials, a path, query, or fragment")
	}

	u.Path = ""
	return u.String(), nil
}
