package admin

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/nyroway/nyro/go/internal/platform/state"
)

const publicGatewayURLSettingKey = "gateway.public_url"

func normalizeSettingValue(key, value string) (string, error) {
	switch key {
	case state.SettingTypeKey:
		value = strings.TrimSpace(value)
		if value == "" || value == string(state.KindMemory) || value == string(state.KindRedis) {
			return value, nil
		}
		return "", fmt.Errorf("state.type must be memory or redis")
	case state.SettingURLKey:
		value = strings.TrimSpace(value)
		if value == "" {
			return "", nil
		}
		if err := state.ValidateURL(value); err != nil {
			return "", err
		}
		return value, nil
	case publicGatewayURLSettingKey:
		return normalizePublicGatewayURL(value)
	default:
		return value, nil
	}
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
