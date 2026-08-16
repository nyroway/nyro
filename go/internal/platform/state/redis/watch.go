package redis

import (
	"context"

	"github.com/nyroway/nyro/go/internal/platform/state"
)

type watchedKey struct {
	version uint64
	found   bool
}

func (s *Server) watch(ctx context.Context, conn *connectionState, keys [][]byte) error {
	newKeys := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, raw := range keys {
		key := string(append([]byte(nil), raw...))
		if _, exists := conn.watched[key]; exists {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		newKeys = append(newKeys, key)
	}
	found := make(map[string]bool, len(newKeys))
	if err := s.opts.Store.Update(ctx, func(ops state.Operations) error {
		for _, key := range newKeys {
			value, err := ops.Get(ctx, []byte(key))
			if err != nil {
				return err
			}
			found[key] = value.Found
		}
		return nil
	}); err != nil {
		return err
	}
	if conn.watched == nil {
		conn.watched = make(map[string]watchedKey)
	}
	for _, key := range newKeys {
		s.trackWatchLocked(conn, key, found[key])
	}
	return nil
}

func (s *Server) trackWatchLocked(conn *connectionState, key string, found bool) {
	if conn.watched == nil {
		conn.watched = make(map[string]watchedKey)
	}
	if _, exists := conn.watched[key]; exists {
		return
	}
	if s.watchers == nil {
		s.watchers = make(map[string]uint64)
	}
	if s.versions == nil {
		s.versions = make(map[string]uint64)
	}
	s.watchers[key]++
	conn.watched[key] = watchedKey{version: s.versions[key], found: found}
}

func (s *Server) watchedChanged(conn *connectionState) bool {
	for key, watched := range conn.watched {
		if s.versions[key] != watched.version {
			return true
		}
	}
	return false
}

func (s *Server) watchedExistenceChanged(ctx context.Context, conn *connectionState, ops state.Operations) (bool, error) {
	for key, watched := range conn.watched {
		value, err := ops.Get(ctx, []byte(key))
		if err != nil {
			return false, err
		}
		if value.Found != watched.found {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) clearWatches(conn *connectionState) {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	s.clearWatchesLocked(conn)
}

func (s *Server) clearWatchesLocked(conn *connectionState) {
	for key := range conn.watched {
		if s.watchers[key] <= 1 {
			delete(s.watchers, key)
			delete(s.versions, key)
			continue
		}
		s.watchers[key]--
	}
	conn.watched = nil
}

func (s *Server) bumpVersions(keys []string) {
	for _, key := range keys {
		if s.watchers[key] > 0 {
			s.versions[key]++
		}
	}
}

func mutationKeys(command [][]byte) []string {
	if len(command) == 0 {
		return nil
	}
	switch commandName(command) {
	case "set":
		if len(command) < 3 {
			return nil
		}
	case "setnx", "incrby", "decrby":
		if len(command) != 3 {
			return nil
		}
	case "setex", "psetex", "zadd", "zremrangebyscore":
		if len(command) != 4 {
			return nil
		}
	case "incr", "decr", "persist":
		if len(command) != 2 {
			return nil
		}
	case "expire", "pexpire", "expireat", "pexpireat":
		if len(command) != 3 && len(command) != 4 {
			return nil
		}
	case "zrem":
		if len(command) < 3 {
			return nil
		}
	case "mset":
		if len(command) < 3 || len(command)%2 == 0 {
			return nil
		}
		keys := make([]string, 0, (len(command)-1)/2)
		for i := 1; i < len(command); i += 2 {
			keys = append(keys, string(command[i]))
		}
		return keys
	case "del", "unlink":
		if len(command) < 2 {
			return nil
		}
		keys := make([]string, 0, len(command)-1)
		for _, key := range command[1:] {
			keys = append(keys, string(key))
		}
		return keys
	default:
		return nil
	}
	return []string{string(command[1])}
}
