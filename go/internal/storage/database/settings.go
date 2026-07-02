package database

import (
	"context"

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
