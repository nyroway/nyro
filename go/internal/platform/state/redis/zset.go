package redis

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/nyroway/nyro/go/internal/platform/state"
)

var (
	zsetMagic    = []byte{0, 'n', 'y', 'r', 'o', ':', 'z', 's', 'e', 't', ':', 'v', '1', 0}
	errWrongType = errors.New("redis: wrong value type")
)

type zsetMember struct {
	Member string  `json:"member"`
	Score  float64 `json:"score"`
}

func encodeZSet(members map[string]float64) ([]byte, error) {
	encoded := make([]zsetMember, 0, len(members))
	for member, score := range members {
		if math.IsNaN(score) || math.IsInf(score, 0) {
			return nil, fmt.Errorf("redis: invalid sorted-set score")
		}
		encoded = append(encoded, zsetMember{
			Member: base64.RawStdEncoding.EncodeToString([]byte(member)),
			Score:  score,
		})
	}
	sort.Slice(encoded, func(i, j int) bool {
		return encoded[i].Member < encoded[j].Member
	})
	payload, err := json.Marshal(encoded)
	if err != nil {
		return nil, fmt.Errorf("redis: encode sorted set: %w", err)
	}
	value := make([]byte, 0, len(zsetMagic)+len(payload))
	value = append(value, zsetMagic...)
	value = append(value, payload...)
	return value, nil
}

func decodeZSetValue(value state.Value) (map[string]float64, bool, error) {
	if !value.Found {
		return make(map[string]float64), false, nil
	}
	if !isZSetValue(value.Bytes) {
		return nil, true, errWrongType
	}
	var encoded []zsetMember
	if err := json.Unmarshal(value.Bytes[len(zsetMagic):], &encoded); err != nil {
		return nil, true, fmt.Errorf("redis: decode sorted set: %w", err)
	}
	members := make(map[string]float64, len(encoded))
	for _, item := range encoded {
		if math.IsNaN(item.Score) || math.IsInf(item.Score, 0) {
			return nil, true, fmt.Errorf("redis: invalid stored sorted-set score")
		}
		member, err := base64.RawStdEncoding.DecodeString(item.Member)
		if err != nil {
			return nil, true, fmt.Errorf("redis: decode sorted-set member: %w", err)
		}
		members[string(member)] = item.Score
	}
	return members, true, nil
}

func isZSetValue(value []byte) bool {
	return bytes.HasPrefix(value, zsetMagic)
}

func loadZSet(ctx context.Context, ops state.Operations, key []byte) (map[string]float64, bool, error) {
	value, err := ops.Get(ctx, key)
	if err != nil {
		return nil, false, err
	}
	return decodeZSetValue(value)
}

func saveZSet(ctx context.Context, ops state.Operations, key []byte, members map[string]float64) error {
	if len(members) == 0 {
		_, err := ops.Delete(ctx, key)
		return err
	}
	value, err := encodeZSet(members)
	if err != nil {
		return err
	}
	_, err = ops.Set(ctx, key, value, state.SetOptions{KeepTTL: true})
	return err
}

func wrongTypeResponse() response {
	return errorResponse("WRONGTYPE Operation against a key holding the wrong kind of value")
}

func zsetStateError(err error) (response, bool, error) {
	if errors.Is(err, errWrongType) {
		return wrongTypeResponse(), false, nil
	}
	return response{}, false, err
}
