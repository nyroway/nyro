package state

import (
	"fmt"
	"net/url"
	"strings"
)

// Kind identifies the configured State backend.
type Kind string

const (
	KindMemory Kind = "memory"
	KindRedis  Kind = "redis"

	SettingTypeKey = "state.type"
	SettingURLKey  = "state.url"
)

// Config is the validated State backend configuration.
type Config struct {
	Kind Kind
	URL  string
}

// LoadConfig resolves and validates State settings. Missing settings select the
// process-local Memory backend.
func LoadConfig(get func(string) (string, error)) (Config, error) {
	if get == nil {
		return Config{}, fmt.Errorf("state: settings getter is required")
	}

	rawKind, err := get(SettingTypeKey)
	if err != nil {
		return Config{}, fmt.Errorf("load %s: %w", SettingTypeKey, err)
	}
	rawURL, err := get(SettingURLKey)
	if err != nil {
		return Config{}, fmt.Errorf("load %s: %w", SettingURLKey, err)
	}

	rawKind = strings.TrimSpace(rawKind)
	rawURL = strings.TrimSpace(rawURL)
	if rawKind == "" && rawURL == "" {
		return Config{Kind: KindMemory}, nil
	}
	return ValidateDeclared(rawKind, rawURL)
}

// ValidateDeclared validates a State backend declaration after trimming its
// scalar values.
func ValidateDeclared(kind, rawURL string) (Config, error) {
	kind = strings.TrimSpace(kind)
	rawURL = strings.TrimSpace(rawURL)

	switch Kind(kind) {
	case "":
		return Config{}, fmt.Errorf("%s is required when State is declared", SettingTypeKey)
	case KindMemory:
		if rawURL != "" {
			return Config{}, fmt.Errorf("%s is not allowed when %s is memory", SettingURLKey, SettingTypeKey)
		}
		return Config{Kind: KindMemory}, nil
	case KindRedis:
		if rawURL == "" {
			return Config{}, fmt.Errorf("%s is required when %s is redis", SettingURLKey, SettingTypeKey)
		}
		if err := ValidateURL(rawURL); err != nil {
			return Config{}, err
		}
		return Config{Kind: KindRedis, URL: rawURL}, nil
	default:
		return Config{}, fmt.Errorf("unknown state type %q", kind)
	}
}

// ValidateURL requires an absolute Redis URL with a network host. Fragments
// are rejected because they are not Redis connection options.
func ValidateURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "redis" || parsed.Host == "" ||
		parsed.Hostname() == "" || parsed.Fragment != "" || strings.Contains(rawURL, "#") {
		return fmt.Errorf("%s must be a valid redis:// URL", SettingURLKey)
	}
	return nil
}

// RedactedURL returns a URL safe for diagnostics. It never returns malformed
// secret-bearing input.
func RedactedURL(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || parsed.Scheme == "" {
		return "<invalid>"
	}
	// Query options are not needed to identify the endpoint and may contain
	// operator-supplied secrets that url.URL.Redacted does not mask.
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	return parsed.Redacted()
}

// IsSettingKey reports whether key belongs to the State backend contract.
func IsSettingKey(key string) bool {
	return key == SettingTypeKey || key == SettingURLKey
}
