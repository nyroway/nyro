// Package redis adapts Redis counters and Sorted Sets to the quota Store.
//
// Layer: 0 (foundation). It imports only its parent quota contract, standard
// library packages, and go-redis.
package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/nyroway/nyro/go/internal/quota"
)

const usageKeyTTL = quota.MaxWindow + time.Hour

// Options provides deterministic seams for time and lease IDs.
type Options struct {
	Now        func() time.Time
	NewLeaseID func() (string, error)
}

// Store implements quota.Store over a caller-owned Redis client.
type Store struct {
	client     goredis.Cmdable
	now        func() time.Time
	newLeaseID func() (string, error)
}

// New constructs a Redis quota Store. It does not Ping or close client.
func New(client goredis.Cmdable, opts Options) (*Store, error) {
	if client == nil {
		return nil, errors.New("quota redis: client is required")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewLeaseID == nil {
		opts.NewLeaseID = randomLeaseID
	}
	return &Store{client: client, now: opts.Now, newLeaseID: opts.NewLeaseID}, nil
}

// Record atomically increments current minute and hour buckets.
func (s *Store) Record(ctx context.Context, consumerID string, usage quota.Usage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if usage.Requests == 0 && usage.Tokens == 0 {
		return nil
	}
	now := s.now()
	minuteEpoch := now.Unix() / int64(time.Minute/time.Second)
	hourEpoch := now.Unix() / int64(time.Hour/time.Second)

	_, err := s.client.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
		queueUsage := func(quotaType string, amount int64) {
			if amount == 0 {
				return
			}
			minuteKey := usageKey(consumerID, quotaType, "m", minuteEpoch)
			hourKey := usageKey(consumerID, quotaType, "h", hourEpoch)
			pipe.IncrBy(ctx, minuteKey, amount)
			pipe.Expire(ctx, minuteKey, usageKeyTTL)
			pipe.IncrBy(ctx, hourKey, amount)
			pipe.Expire(ctx, hourKey, usageKeyTTL)
		}
		queueUsage("requests", usage.Requests)
		queueUsage("tokens", usage.Tokens)
		return nil
	})
	if err != nil {
		return fmt.Errorf("quota redis: record usage: %w", err)
	}
	return nil
}

// Value sums the exact minute/hour decomposition of a trailing window.
func (s *Store) Value(ctx context.Context, consumerID, quotaType string, window time.Duration) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	currentMinute := s.now().Unix() / int64(time.Minute/time.Second)
	terms := usageTerms(consumerID, quotaType, currentMinute, window)
	keys := make([]string, len(terms))
	for i, term := range terms {
		keys[i] = term.key
	}
	values, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return 0, fmt.Errorf("quota redis: read usage: %w", err)
	}
	var total int64
	for index, value := range values {
		if value == nil {
			continue
		}
		var raw string
		switch typed := value.(type) {
		case string:
			raw = typed
		case []byte:
			raw = string(typed)
		default:
			return 0, fmt.Errorf("quota redis: counter %q has unexpected type %T", keys[index], value)
		}
		amount, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("quota redis: counter %q is malformed: %w", keys[index], err)
		}
		if terms[index].coefficient < 0 {
			if amount == math.MinInt64 {
				return 0, fmt.Errorf("quota redis: usage total overflows int64")
			}
			amount = -amount
		}
		if (amount > 0 && total > math.MaxInt64-amount) || (amount < 0 && total < math.MinInt64-amount) {
			return 0, fmt.Errorf("quota redis: usage total overflows int64")
		}
		total += amount
	}
	return total, nil
}

// Acquire atomically removes expired leases, adds a candidate, and counts the
// live members. A clean denial is distinguished from Redis failure.
func (s *Store) Acquire(ctx context.Context, consumerID string, limit int64, leaseTTL time.Duration) (quota.Lease, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if limit <= 0 {
		return nil, false, errors.New("quota redis: concurrency limit must be positive")
	}
	if leaseTTL <= 0 || leaseTTL > time.Duration(math.MaxInt64)-time.Minute {
		return nil, false, errors.New("quota redis: lease TTL must be positive and representable")
	}
	leaseID, err := s.newLeaseID()
	if err != nil {
		return nil, false, fmt.Errorf("quota redis: create lease ID: %w", err)
	}
	if leaseID == "" {
		return nil, false, errors.New("quota redis: lease ID is empty")
	}

	now := s.now()
	key := concurrencyKey(consumerID)
	deadline := now.Add(leaseTTL)
	var card *goredis.IntCmd
	_, err = s.client.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
		pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(now.UnixMilli(), 10))
		pipe.ZAdd(ctx, key, goredis.Z{Score: float64(deadline.UnixMilli()), Member: leaseID})
		card = pipe.ZCard(ctx, key)
		pipe.Expire(ctx, key, leaseTTL+time.Minute)
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("quota redis: acquire lease: %w", err)
	}
	count, err := card.Result()
	if err != nil {
		return nil, false, fmt.Errorf("quota redis: count leases: %w", err)
	}
	if count > limit {
		if err := s.client.ZRem(ctx, key, leaseID).Err(); err != nil {
			return nil, false, fmt.Errorf("quota redis: clean up denied lease: %w", err)
		}
		return nil, false, nil
	}
	return &lease{client: s.client, key: key, member: leaseID}, true, nil
}

type lease struct {
	client goredis.Cmdable
	key    string
	member string
	once   sync.Once
	err    error
}

func (l *lease) Release(ctx context.Context) error {
	l.once.Do(func() {
		if err := l.client.ZRem(ctx, l.key, l.member).Err(); err != nil {
			l.err = fmt.Errorf("quota redis: release lease: %w", err)
		}
	})
	return l.err
}

func randomLeaseID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

var _ quota.Store = (*Store)(nil)
