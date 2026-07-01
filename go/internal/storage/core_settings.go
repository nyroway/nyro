package storage

// CoreSetting is one row of the settings table under the config-schema column
// naming (key, not the legacy name). It coexists with the legacy Setting until
// callers migrate.
type CoreSetting struct {
	Key       string `json:"key"`
	Value     string `json:"value"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// CoreSettingsStore is the config-schema settings store (key column). It mirrors
// the legacy SettingsStore API but over CoreSetting.
type CoreSettingsStore interface {
	Get(key string) (string, error)
	Set(key, value string) error
	ListAll() ([]CoreSetting, error)
}
