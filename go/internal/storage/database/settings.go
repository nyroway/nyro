package database

import (
	"context"
	"sort"

	"gorm.io/gorm/clause"

	"github.com/nyroway/nyro/go/internal/storage"
	"github.com/nyroway/nyro/go/internal/storage/model"
	"github.com/nyroway/nyro/go/internal/storage/query"
)

type coreSettingsStore struct{ q *query.Query }

func (s coreSettingsStore) Get(key string) (string, error) {
	ctx := context.Background()
	row, err := s.q.Setting.WithContext(ctx).Where(s.q.Setting.Key.Eq(key)).First()
	if err != nil {
		if isNotFound(err) {
			return "", nil
		}
		return "", err
	}
	return row.Value, nil
}

func (s coreSettingsStore) Set(key, value string) error {
	ctx := context.Background()
	row := &model.Setting{Key: key, Value: value, UpdatedAt: nowISO()}
	return s.q.Setting.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
	}).Create(row)
}

func (s coreSettingsStore) SetMany(values map[string]string) error {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	return s.q.Transaction(func(tx *query.Query) error {
		store := coreSettingsStore{q: tx}
		for _, key := range keys {
			if err := store.Set(key, values[key]); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s coreSettingsStore) ListAll() ([]storage.Setting, error) {
	ctx := context.Background()
	rows, err := s.q.Setting.WithContext(ctx).Order(s.q.Setting.Key).Find()
	if err != nil {
		return nil, err
	}
	out := make([]storage.Setting, 0, len(rows))
	for _, r := range rows {
		out = append(out, storage.Setting{Key: r.Key, Value: r.Value, UpdatedAt: r.UpdatedAt})
	}
	return out, nil
}
