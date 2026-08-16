package redis

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/nyroway/nyro/go/internal/quota"
)

const probeConsumerPrefix = "__nyro_probe__:"

// Probe verifies every Redis capability required by quota before runtime
// installs the Store. Probe data is isolated under a unique consumer ID.
func (s *Store) Probe(ctx context.Context) (resultErr error) {
	probeID, err := s.newLeaseID()
	if err != nil {
		return fmt.Errorf("quota redis: create probe ID: %w", err)
	}
	if probeID == "" {
		return errors.New("quota redis: probe ID is empty")
	}
	consumerID := probeConsumerPrefix + probeID
	cleanupKeys := make(map[string]struct{})
	trackKeys := func() {
		for _, key := range probeKeys(consumerID, s.now()) {
			cleanupKeys[key] = struct{}{}
		}
	}
	defer func() {
		trackKeys()
		keys := make([]string, 0, len(cleanupKeys))
		for key := range cleanupKeys {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if cleanupErr := s.client.Del(ctx, keys...).Err(); cleanupErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("quota redis: probe cleanup: %w", cleanupErr))
		}
	}()

	limits := []quota.RequestLimit{{Limit: 1, Window: time.Minute}}
	trackKeys()
	allowed, err := s.AdmitRequest(ctx, consumerID, limits)
	trackKeys()
	if err != nil {
		return fmt.Errorf("quota redis: probe request admission: %w", err)
	}
	if !allowed {
		return errors.New("quota redis: probe first request was denied")
	}
	allowed, err = s.AdmitRequest(ctx, consumerID, limits)
	trackKeys()
	if err != nil {
		return fmt.Errorf("quota redis: probe request denial: %w", err)
	}
	if allowed {
		return errors.New("quota redis: probe second request was admitted")
	}

	trackKeys()
	if err := s.RecordTokens(ctx, consumerID, 7); err != nil {
		return fmt.Errorf("quota redis: probe record tokens: %w", err)
	}
	trackKeys()
	tokens, err := s.TokenValue(ctx, consumerID, time.Minute)
	if err != nil {
		return fmt.Errorf("quota redis: probe read tokens: %w", err)
	}
	if tokens != 7 {
		return fmt.Errorf("quota redis: probe token total = %d, want 7", tokens)
	}

	trackKeys()
	lease, allowed, err := s.Acquire(ctx, consumerID, 1, time.Minute)
	trackKeys()
	if err != nil {
		return fmt.Errorf("quota redis: probe acquire lease: %w", err)
	}
	if !allowed || lease == nil {
		return errors.New("quota redis: probe concurrency lease was denied")
	}
	if err := lease.Release(ctx); err != nil {
		return fmt.Errorf("quota redis: probe release lease: %w", err)
	}
	return nil
}

func probeKeys(consumerID string, now time.Time) []string {
	minuteEpoch := now.Unix() / int64(time.Minute/time.Second)
	hourEpoch := floorHour(minuteEpoch)
	return []string{
		usageKey(consumerID, "requests", "m", minuteEpoch),
		usageKey(consumerID, "requests", "h", hourEpoch),
		usageKey(consumerID, "tokens", "m", minuteEpoch),
		usageKey(consumerID, "tokens", "h", hourEpoch),
		concurrencyKey(consumerID),
	}
}
