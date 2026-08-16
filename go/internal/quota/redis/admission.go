package redis

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/nyroway/nyro/go/internal/quota"
)

// AdmitRequest atomically checks every request window and counts one request
// only when all limits allow it.
func (s *Store) AdmitRequest(ctx context.Context, consumerID string, limits []quota.RequestLimit) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(limits) == 0 {
		return true, nil
	}
	if err := validateRequestLimits(limits); err != nil {
		return false, err
	}

	currentMinute := s.now().Unix() / int64(time.Minute/time.Second)
	termSets, watchKeys := requestTermsAndKeys(consumerID, currentMinute, limits)
	minuteKey := usageKey(consumerID, "requests", "m", currentMinute)
	hourKey := usageKey(consumerID, "requests", "h", floorHour(currentMinute))
	watchKeys = appendUniqueSorted(watchKeys, minuteKey, hourKey)

	allowed, err := retryAdmission(s.maxAdmissionRetries, func() (bool, error) {
		admitted := false
		err := s.client.Watch(ctx, func(tx *goredis.Tx) error {
			if err := requireStringCounters(ctx, tx, watchKeys); err != nil {
				return err
			}
			values, err := tx.MGet(ctx, watchKeys...).Result()
			if err != nil {
				return err
			}
			if len(values) != len(watchKeys) {
				return errors.New("quota redis: counter result length mismatch")
			}
			indexed := indexValues(watchKeys, values)
			for i, limit := range limits {
				termValues := valuesForTerms(indexed, termSets[i])
				total, err := sumUsageTerms(termValues, termSets[i])
				if err != nil {
					return err
				}
				if total >= limit.Limit {
					return nil
				}
			}

			_, err = tx.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
				pipe.IncrBy(ctx, minuteKey, 1)
				pipe.Expire(ctx, minuteKey, usageKeyTTL)
				pipe.IncrBy(ctx, hourKey, 1)
				pipe.Expire(ctx, hourKey, usageKeyTTL)
				return nil
			})
			if err == nil {
				admitted = true
			}
			return err
		}, watchKeys...)
		return admitted, err
	})
	if err != nil {
		if errors.Is(err, quota.ErrAdmissionContended) {
			return false, err
		}
		return false, fmt.Errorf("quota redis: admit request: %w", err)
	}
	return allowed, nil
}

func requireStringCounters(ctx context.Context, tx *goredis.Tx, keys []string) error {
	commands := make([]*goredis.StatusCmd, len(keys))
	if _, err := tx.Pipelined(ctx, func(pipe goredis.Pipeliner) error {
		for i, key := range keys {
			commands[i] = pipe.Type(ctx, key)
		}
		return nil
	}); err != nil {
		return err
	}
	for i, command := range commands {
		kind, err := command.Result()
		if err != nil {
			return err
		}
		if kind != "none" && kind != "string" {
			return fmt.Errorf("quota redis: counter %q has type %s, want string", keys[i], kind)
		}
	}
	return nil
}

func validateRequestLimits(limits []quota.RequestLimit) error {
	for _, limit := range limits {
		if limit.Limit <= 0 || limit.Window <= 0 {
			return errors.New("quota redis: request limit and window must be positive")
		}
	}
	return nil
}

func retryAdmission(maxAttempts int, attempt func() (bool, error)) (bool, error) {
	for i := 0; i < maxAttempts; i++ {
		allowed, err := attempt()
		if err == nil {
			return allowed, nil
		}
		if !errors.Is(err, goredis.TxFailedErr) {
			return false, err
		}
	}
	return false, quota.ErrAdmissionContended
}

func requestTermsAndKeys(consumerID string, currentMinute int64, limits []quota.RequestLimit) ([][]usageTerm, []string) {
	termSets := make([][]usageTerm, len(limits))
	unique := make(map[string]struct{})
	for i, limit := range limits {
		terms := usageTerms(consumerID, "requests", currentMinute, limit.Window)
		termSets[i] = terms
		for _, term := range terms {
			unique[term.key] = struct{}{}
		}
	}
	keys := make([]string, 0, len(unique))
	for key := range unique {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return termSets, keys
}

func appendUniqueSorted(keys []string, additions ...string) []string {
	unique := make(map[string]struct{}, len(keys)+len(additions))
	for _, key := range keys {
		unique[key] = struct{}{}
	}
	for _, key := range additions {
		unique[key] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for key := range unique {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func indexValues(keys []string, values []any) map[string]any {
	indexed := make(map[string]any, len(keys))
	for i, key := range keys {
		if i < len(values) {
			indexed[key] = values[i]
		}
	}
	return indexed
}

func valuesForTerms(indexed map[string]any, terms []usageTerm) []any {
	values := make([]any, len(terms))
	for i, term := range terms {
		values[i] = indexed[term.key]
	}
	return values
}
