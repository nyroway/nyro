package memory

import (
	"sort"

	"github.com/nyroway/nyro/go/internal/storage"
)

type coreSettingsStore struct{ b *Backend }

func (s coreSettingsStore) Get(key string) (string, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	return s.b.coreSettings[key], nil
}

func (s coreSettingsStore) Set(key, value string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	s.b.coreSettings[key] = value
	return nil
}

func (s coreSettingsStore) ListAll() ([]storage.CoreSetting, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	out := make([]storage.CoreSetting, 0, len(s.b.coreSettings))
	for k, v := range s.b.coreSettings {
		out = append(out, storage.CoreSetting{Key: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
