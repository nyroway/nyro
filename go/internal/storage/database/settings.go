package database

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	"gorm.io/gorm"
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

func (s coreSettingsStore) SetManyAndIncrement(values map[string]string, counterKey string) (int64, error) {
	if _, exists := values[counterKey]; exists {
		return 0, fmt.Errorf("counter key %q must not also be a setting value", counterKey)
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var counter int64
	err := s.q.Transaction(func(tx *query.Query) error {
		store := coreSettingsStore{q: tx}
		for _, key := range keys {
			if err := store.Set(key, values[key]); err != nil {
				return err
			}
		}

		row := &model.Setting{Key: counterKey, Value: "1", UpdatedAt: nowISO()}
		if err := tx.UnderlyingDB().WithContext(context.Background()).Clauses(
			clause.OnConflict{
				Columns: []clause.Column{{Name: "key"}},
				DoUpdates: clause.Assignments(map[string]any{
					"value":      gorm.Expr("CAST(settings.value AS BIGINT) + 1"),
					"updated_at": row.UpdatedAt,
				}),
			},
			clause.Returning{Columns: []clause.Column{{Name: "value"}}},
		).Create(row).Error; err != nil {
			return err
		}
		var err error
		counter, err = strconv.ParseInt(row.Value, 10, 64)
		if err != nil {
			return fmt.Errorf("parse incremented %s: %w", counterKey, err)
		}
		return nil
	})
	return counter, err
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
