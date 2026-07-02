package storage

// Setting is one row of the settings table (dot-key/value pairs flattened
// from the settings YAML tree).
type Setting struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// SettingsStore is the config-schema settings store (key column).
type SettingsStore interface {
	Get(key string) (string, error)
	Set(key, value string) error
	ListAll() ([]Setting, error)
}
