package memory

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/nyroway/nyro/go/internal/storage"
)

type coreSettingsStore struct{ b *Backend }

func (s coreSettingsStore) Get(key string) (string, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	return s.b.settings[key], nil
}

func (s coreSettingsStore) Set(key, value string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	s.b.settings[key] = value
	return nil
}

func (s coreSettingsStore) SetMany(values map[string]string) error {
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	for key, value := range values {
		s.b.settings[key] = value
	}
	return nil
}

func (s coreSettingsStore) SetManyAndIncrement(values map[string]string, counterKey string) (int64, error) {
	if _, exists := values[counterKey]; exists {
		return 0, fmt.Errorf("counter key %q must not also be a setting value", counterKey)
	}
	s.b.mu.Lock()
	defer s.b.mu.Unlock()

	for key, value := range values {
		s.b.settings[key] = value
	}
	counter, _ := strconv.ParseInt(s.b.settings[counterKey], 10, 64)
	counter++
	s.b.settings[counterKey] = strconv.FormatInt(counter, 10)
	return counter, nil
}

func (s coreSettingsStore) ListAll() ([]storage.Setting, error) {
	s.b.mu.RLock()
	defer s.b.mu.RUnlock()
	out := make([]storage.Setting, 0, len(s.b.settings))
	for k, v := range s.b.settings {
		out = append(out, storage.Setting{Key: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
