package redis

func (s *Server) watch(conn *connectionState, keys [][]byte) {
	if conn.watched == nil {
		conn.watched = make(map[string]uint64)
	}
	for _, raw := range keys {
		key := string(append([]byte(nil), raw...))
		if _, exists := conn.watched[key]; !exists {
			conn.watched[key] = s.versions[key]
		}
	}
}

func (s *Server) watchedChanged(conn *connectionState) bool {
	for key, version := range conn.watched {
		if s.versions[key] != version {
			return true
		}
	}
	return false
}

func clearWatches(conn *connectionState) {
	conn.watched = nil
}

func (s *Server) bumpVersions(keys []string) {
	for _, key := range keys {
		s.versions[key]++
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
