package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	collectlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	"google.golang.org/protobuf/proto"

	"github.com/nyroway/nyro/go/infra/observe"
)

type indexedLogCoordinate struct {
	id                                     int64
	batchID                                int64
	resourceIndex, scopeIndex, recordIndex int
	payload                                []byte
}

func registerLogAttributeIndexes(
	ctx context.Context,
	db *sql.DB,
	requested []observe.AttributeIndex,
) (map[string]observe.AttributeType, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("observe sqlite: begin attribute registration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	registered := make(map[string]observe.AttributeType)
	rows, err := tx.QueryContext(ctx, `SELECT key, value_type FROM otlp_log_attribute_definitions`)
	if err != nil {
		return nil, fmt.Errorf("observe sqlite: read attribute definitions: %w", err)
	}
	for rows.Next() {
		var key string
		var valueType observe.AttributeType
		if err := rows.Scan(&key, &valueType); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("observe sqlite: scan attribute definition: %w", err)
		}
		registered[key] = valueType
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("observe sqlite: iterate attribute definitions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("observe sqlite: close attribute definitions: %w", err)
	}

	newIndexes := make(map[string]observe.AttributeType)
	for _, index := range requested {
		if index.Key == "" {
			return nil, fmt.Errorf("%w: attribute index key is required", observe.ErrInvalidQuery)
		}
		if index.Type != observe.AttributeString && index.Type != observe.AttributeInt64 {
			return nil, fmt.Errorf("%w: unsupported type %q for attribute %q", observe.ErrInvalidQuery, index.Type, index.Key)
		}
		if existing, ok := registered[index.Key]; ok {
			if existing != index.Type {
				return nil, fmt.Errorf("%w: attribute %q is already indexed as %s", observe.ErrInvalidQuery, index.Key, existing)
			}
			continue
		}
		if existing, ok := newIndexes[index.Key]; ok && existing != index.Type {
			return nil, fmt.Errorf("%w: conflicting types for attribute %q", observe.ErrInvalidQuery, index.Key)
		}
		newIndexes[index.Key] = index.Type
	}
	for key, valueType := range newIndexes {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO otlp_log_attribute_definitions(key, value_type) VALUES (?, ?)`, key, valueType,
		); err != nil {
			return nil, fmt.Errorf("observe sqlite: register attribute %q: %w", key, err)
		}
		registered[key] = valueType
	}
	if err := backfillLogAttributes(ctx, tx, newIndexes); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("observe sqlite: commit attribute registration: %w", err)
	}
	return registered, nil
}

func backfillLogAttributes(ctx context.Context, tx *sql.Tx, indexes map[string]observe.AttributeType) error {
	if len(indexes) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
SELECT i.id, i.batch_id, i.resource_idx, i.scope_idx, i.record_idx, b.payload
FROM otlp_log_index i
JOIN otlp_batches b ON b.id = i.batch_id
ORDER BY i.batch_id, i.id`)
	if err != nil {
		return fmt.Errorf("observe sqlite: read logs for attribute backfill: %w", err)
	}
	coordinates := make([]indexedLogCoordinate, 0)
	for rows.Next() {
		var coordinate indexedLogCoordinate
		if err := rows.Scan(
			&coordinate.id, &coordinate.batchID, &coordinate.resourceIndex,
			&coordinate.scopeIndex, &coordinate.recordIndex, &coordinate.payload,
		); err != nil {
			_ = rows.Close()
			return fmt.Errorf("observe sqlite: scan log for attribute backfill: %w", err)
		}
		coordinates = append(coordinates, coordinate)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("observe sqlite: iterate logs for attribute backfill: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("observe sqlite: close logs for attribute backfill: %w", err)
	}

	decoded := make(map[int64]*collectlogs.ExportLogsServiceRequest)
	for _, coordinate := range coordinates {
		request := decoded[coordinate.batchID]
		if request == nil {
			request = &collectlogs.ExportLogsServiceRequest{}
			if err := proto.Unmarshal(coordinate.payload, request); err != nil {
				return fmt.Errorf("observe sqlite: decode log batch %d for attribute backfill: %w", coordinate.batchID, err)
			}
			decoded[coordinate.batchID] = request
		}
		resourceLogs := request.GetResourceLogs()
		if coordinate.resourceIndex >= len(resourceLogs) {
			return errors.New("observe sqlite: corrupt log resource coordinate during attribute backfill")
		}
		scopeLogs := resourceLogs[coordinate.resourceIndex].GetScopeLogs()
		if coordinate.scopeIndex >= len(scopeLogs) {
			return errors.New("observe sqlite: corrupt log scope coordinate during attribute backfill")
		}
		records := scopeLogs[coordinate.scopeIndex].GetLogRecords()
		if coordinate.recordIndex >= len(records) {
			return errors.New("observe sqlite: corrupt log record coordinate during attribute backfill")
		}
		if err := indexLogAttributes(ctx, tx, coordinate.id, records[coordinate.recordIndex].GetAttributes(), indexes); err != nil {
			return err
		}
	}
	return nil
}

func indexLogAttributes(
	ctx context.Context,
	tx *sql.Tx,
	logID int64,
	attributes []*commonv1.KeyValue,
	indexes map[string]observe.AttributeType,
) error {
	for _, attribute := range attributes {
		valueType, ok := indexes[attribute.GetKey()]
		if !ok {
			continue
		}
		var stringValue any
		var intValue any
		switch valueType {
		case observe.AttributeString:
			value, ok := attribute.GetValue().GetValue().(*commonv1.AnyValue_StringValue)
			if !ok {
				continue
			}
			stringValue = value.StringValue
		case observe.AttributeInt64:
			value, ok := attribute.GetValue().GetValue().(*commonv1.AnyValue_IntValue)
			if !ok {
				continue
			}
			intValue = value.IntValue
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO otlp_log_attributes(log_id, key, string_value, int_value)
VALUES (?, ?, ?, ?)
ON CONFLICT(log_id, key) DO UPDATE SET
    string_value = excluded.string_value,
    int_value = excluded.int_value`, logID, attribute.GetKey(), stringValue, intValue); err != nil {
			return fmt.Errorf("observe sqlite: index log attribute %q: %w", attribute.GetKey(), err)
		}
	}
	return nil
}
