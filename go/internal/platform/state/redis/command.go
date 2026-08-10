package redis

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/nyroway/nyro/go/internal/platform/state"
)

type connectionState struct {
	id            int64
	proto         int
	authenticated bool
	name          []byte
	multi         bool
	multiDirty    bool
	queued        [][][]byte
}

func commandName(command [][]byte) string {
	if len(command) == 0 {
		return ""
	}
	return strings.ToLower(string(command[0]))
}

func (s *Server) execute(ctx context.Context, conn *connectionState, command [][]byte) (response, bool, error) {
	name := commandName(command)
	if name == "" {
		return errorResponse("ERR empty command"), false, nil
	}
	if !conn.authenticated && name != "auth" && name != "hello" && name != "quit" {
		return errorResponse("NOAUTH Authentication required."), false, nil
	}

	if conn.multi {
		switch name {
		case "exec":
			return s.executeTransaction(ctx, conn, command)
		case "discard":
			if len(command) != 1 {
				return wrongArguments(name), false, nil
			}
			conn.multi = false
			conn.multiDirty = false
			conn.queued = nil
			return simpleResponse("OK"), false, nil
		case "multi":
			return errorResponse("ERR MULTI calls can not be nested"), false, nil
		case "auth", "hello", "quit":
			conn.multiDirty = true
			return errorResponse("ERR command is not allowed inside MULTI"), false, nil
		default:
			if errReply := validateQueuedCommand(command); errReply != nil {
				conn.multiDirty = true
				return *errReply, false, nil
			}
			conn.queued = append(conn.queued, cloneCommand(command))
			return simpleResponse("QUEUED"), false, nil
		}
	}

	if name == "exec" || name == "discard" {
		return errorResponse("ERR " + strings.ToUpper(name) + " without MULTI"), false, nil
	}
	if name == "multi" {
		if len(command) != 1 {
			return wrongArguments(name), false, nil
		}
		conn.multi = true
		conn.multiDirty = false
		conn.queued = nil
		return simpleResponse("OK"), false, nil
	}
	return s.executeDirect(ctx, conn, s.opts.Store, command)
}

func (s *Server) executeTransaction(ctx context.Context, conn *connectionState, command [][]byte) (response, bool, error) {
	if len(command) != 1 {
		return wrongArguments("exec"), false, nil
	}
	queued := conn.queued
	dirty := conn.multiDirty
	conn.multi = false
	conn.multiDirty = false
	conn.queued = nil
	if dirty {
		return errorResponse("EXECABORT Transaction discarded because of previous errors."), false, nil
	}
	replies := make([]response, 0, len(queued))
	err := s.opts.Store.Update(ctx, func(ops state.Operations) error {
		for _, queuedCommand := range queued {
			reply, closeConnection, err := s.executeDirect(ctx, conn, ops, queuedCommand)
			if err != nil {
				return err
			}
			if closeConnection {
				return errors.New("connection command queued in transaction")
			}
			replies = append(replies, reply)
		}
		return nil
	})
	if err != nil {
		return response{}, false, err
	}
	return arrayResponse(replies), false, nil
}

func (s *Server) executeDirect(ctx context.Context, conn *connectionState, ops state.Operations, command [][]byte) (response, bool, error) {
	name := commandName(command)
	switch name {
	case "ping":
		if len(command) == 1 {
			return simpleResponse("PONG"), false, nil
		}
		if len(command) == 2 {
			return bulkResponse(command[1]), false, nil
		}
		return wrongArguments(name), false, nil
	case "echo":
		if len(command) != 2 {
			return wrongArguments(name), false, nil
		}
		return bulkResponse(command[1]), false, nil
	case "quit":
		if len(command) != 1 {
			return wrongArguments(name), false, nil
		}
		return simpleResponse("OK"), true, nil
	case "auth":
		return s.executeAuth(conn, command)
	case "hello":
		return s.executeHello(conn, command)
	case "select":
		if len(command) != 2 {
			return wrongArguments(name), false, nil
		}
		if string(command[1]) != "0" {
			return errorResponse("ERR DB index is out of range"), false, nil
		}
		return simpleResponse("OK"), false, nil
	case "client":
		return executeClient(conn, command)
	case "get":
		if len(command) != 2 {
			return wrongArguments(name), false, nil
		}
		value, err := ops.Get(ctx, command[1])
		if err != nil {
			return response{}, false, err
		}
		return valueResponse(value), false, nil
	case "set":
		return s.executeSet(ctx, ops, command)
	case "setnx":
		if len(command) != 3 {
			return wrongArguments(name), false, nil
		}
		result, err := ops.Set(ctx, command[1], command[2], state.SetOptions{Condition: state.SetIfMissing})
		if err != nil {
			return stateError(err)
		}
		return integerResponse(boolInt(result.Applied)), false, nil
	case "setex", "psetex":
		if len(command) != 4 {
			return wrongArguments(name), false, nil
		}
		n, err := parseIntArgument(command[2])
		if err != nil || n <= 0 {
			return errorResponse("ERR invalid expire time in '" + name + "' command"), false, nil
		}
		unit := time.Second
		if name == "psetex" {
			unit = time.Millisecond
		}
		deadline, ok := relativeDeadline(s.opts.Now(), n, unit)
		if !ok {
			return errorResponse("ERR invalid expire time in '" + name + "' command"), false, nil
		}
		if _, err := ops.Set(ctx, command[1], command[3], state.SetOptions{ExpireAt: deadline}); err != nil {
			return stateError(err)
		}
		return simpleResponse("OK"), false, nil
	case "mget":
		if len(command) < 2 {
			return wrongArguments(name), false, nil
		}
		values, err := ops.MGet(ctx, command[1:]...)
		if err != nil {
			return response{}, false, err
		}
		items := make([]response, len(values))
		for i, value := range values {
			items[i] = valueResponse(value)
		}
		return arrayResponse(items), false, nil
	case "mset":
		if len(command) < 3 || len(command)%2 == 0 {
			return wrongArguments(name), false, nil
		}
		pairs := make([]state.Pair, 0, (len(command)-1)/2)
		for i := 1; i < len(command); i += 2 {
			pairs = append(pairs, state.Pair{Key: command[i], Value: command[i+1]})
		}
		if err := ops.MSet(ctx, pairs); err != nil {
			return response{}, false, err
		}
		return simpleResponse("OK"), false, nil
	case "del", "unlink":
		if len(command) < 2 {
			return wrongArguments(name), false, nil
		}
		count, err := ops.Delete(ctx, command[1:]...)
		if err != nil {
			return response{}, false, err
		}
		return integerResponse(count), false, nil
	case "exists":
		if len(command) < 2 {
			return wrongArguments(name), false, nil
		}
		count, err := ops.Exists(ctx, command[1:]...)
		if err != nil {
			return response{}, false, err
		}
		return integerResponse(count), false, nil
	case "type":
		if len(command) != 2 {
			return wrongArguments(name), false, nil
		}
		value, err := ops.Get(ctx, command[1])
		if err != nil {
			return response{}, false, err
		}
		if value.Found {
			return simpleResponse("string"), false, nil
		}
		return simpleResponse("none"), false, nil
	case "incr", "decr":
		if len(command) != 2 {
			return wrongArguments(name), false, nil
		}
		delta := int64(1)
		if name == "decr" {
			delta = -1
		}
		return executeIncrement(ctx, ops, command[1], delta)
	case "incrby", "decrby":
		if len(command) != 3 {
			return wrongArguments(name), false, nil
		}
		delta, err := parseIntArgument(command[2])
		if err != nil {
			return errorResponse("ERR value is not an integer or out of range"), false, nil
		}
		if name == "decrby" {
			if delta == -1<<63 {
				return errorResponse("ERR increment or decrement would overflow"), false, nil
			}
			delta = -delta
		}
		return executeIncrement(ctx, ops, command[1], delta)
	case "expire", "pexpire", "expireat", "pexpireat":
		return s.executeExpire(ctx, ops, command)
	case "ttl", "pttl":
		if len(command) != 2 {
			return wrongArguments(name), false, nil
		}
		ttl, err := ops.TTL(ctx, command[1])
		if err != nil {
			return response{}, false, err
		}
		switch ttl.State {
		case state.TTLMissing:
			return integerResponse(-2), false, nil
		case state.TTLPersistent:
			return integerResponse(-1), false, nil
		default:
			if name == "pttl" {
				return integerResponse(ttl.Remaining.Milliseconds()), false, nil
			}
			return integerResponse(int64(ttl.Remaining / time.Second)), false, nil
		}
	case "persist":
		if len(command) != 2 {
			return wrongArguments(name), false, nil
		}
		persisted, err := ops.Persist(ctx, command[1])
		if err != nil {
			return response{}, false, err
		}
		return integerResponse(boolInt(persisted)), false, nil
	default:
		return errorResponse(fmt.Sprintf("ERR unknown command '%s'", name)), false, nil
	}
}

func (s *Server) executeAuth(conn *connectionState, command [][]byte) (response, bool, error) {
	if len(command) != 2 && len(command) != 3 {
		return wrongArguments("auth"), false, nil
	}
	password := command[len(command)-1]
	if len(command) == 3 && string(command[1]) != "default" {
		return errorResponse("WRONGPASS invalid username-password pair or user is disabled."), false, nil
	}
	if s.opts.Password == "" || subtle.ConstantTimeCompare(password, []byte(s.opts.Password)) != 1 {
		return errorResponse("WRONGPASS invalid username-password pair or user is disabled."), false, nil
	}
	conn.authenticated = true
	return simpleResponse("OK"), false, nil
}

func (s *Server) executeHello(conn *connectionState, command [][]byte) (response, bool, error) {
	if len(command) == 1 {
		return helloResponse(conn), false, nil
	}
	proto, err := strconv.Atoi(string(command[1]))
	if err != nil || (proto != 2 && proto != 3) {
		return errorResponse("NOPROTO unsupported protocol version"), false, nil
	}
	for i := 2; i < len(command); {
		switch strings.ToLower(string(command[i])) {
		case "auth":
			if i+2 >= len(command) {
				return errorResponse("ERR syntax error"), false, nil
			}
			reply, _, _ := s.executeAuth(conn, [][]byte{[]byte("auth"), command[i+1], command[i+2]})
			if reply.kind == responseError {
				return reply, false, nil
			}
			i += 3
		case "setname":
			if i+1 >= len(command) {
				return errorResponse("ERR syntax error"), false, nil
			}
			conn.name = append(conn.name[:0], command[i+1]...)
			i += 2
		default:
			return errorResponse("ERR syntax error"), false, nil
		}
	}
	if s.opts.Password != "" && !conn.authenticated {
		return errorResponse("NOAUTH Authentication required."), false, nil
	}
	conn.proto = proto
	return helloResponse(conn), false, nil
}

func helloResponse(conn *connectionState) response {
	return mapResponse(
		bulkResponse([]byte("server")), bulkResponse([]byte("redis")),
		bulkResponse([]byte("version")), bulkResponse([]byte("7.2.0")),
		bulkResponse([]byte("proto")), integerResponse(int64(conn.proto)),
		bulkResponse([]byte("id")), integerResponse(conn.id),
		bulkResponse([]byte("mode")), bulkResponse([]byte("standalone")),
		bulkResponse([]byte("role")), bulkResponse([]byte("master")),
		bulkResponse([]byte("modules")), arrayResponse(nil),
	)
}

func executeClient(conn *connectionState, command [][]byte) (response, bool, error) {
	if len(command) < 2 {
		return wrongArguments("client"), false, nil
	}
	switch strings.ToLower(string(command[1])) {
	case "setinfo":
		if len(command) != 4 {
			return wrongArguments("client setinfo"), false, nil
		}
		return simpleResponse("OK"), false, nil
	case "setname":
		if len(command) != 3 {
			return wrongArguments("client setname"), false, nil
		}
		conn.name = append(conn.name[:0], command[2]...)
		return simpleResponse("OK"), false, nil
	case "getname":
		if len(command) != 2 {
			return wrongArguments("client getname"), false, nil
		}
		if conn.name == nil {
			return nullResponse(), false, nil
		}
		return bulkResponse(conn.name), false, nil
	case "id":
		if len(command) != 2 {
			return wrongArguments("client id"), false, nil
		}
		return integerResponse(conn.id), false, nil
	case "maint_notifications":
		return simpleResponse("OK"), false, nil
	default:
		return errorResponse("ERR unknown subcommand '" + string(command[1]) + "'. Try CLIENT HELP."), false, nil
	}
}

func (s *Server) executeSet(ctx context.Context, ops state.Operations, command [][]byte) (response, bool, error) {
	if len(command) < 3 {
		return wrongArguments("set"), false, nil
	}
	opts := state.SetOptions{}
	var expirationSet bool
	for i := 3; i < len(command); i++ {
		switch strings.ToLower(string(command[i])) {
		case "nx":
			if opts.Condition != state.SetAlways {
				return errorResponse("ERR syntax error"), false, nil
			}
			opts.Condition = state.SetIfMissing
		case "xx":
			if opts.Condition != state.SetAlways {
				return errorResponse("ERR syntax error"), false, nil
			}
			opts.Condition = state.SetIfPresent
		case "get":
			if opts.GetPrevious {
				return errorResponse("ERR syntax error"), false, nil
			}
			opts.GetPrevious = true
		case "keepttl":
			if expirationSet || opts.KeepTTL {
				return errorResponse("ERR syntax error"), false, nil
			}
			opts.KeepTTL = true
		case "ex", "px", "exat", "pxat":
			if expirationSet || opts.KeepTTL || i+1 >= len(command) {
				return errorResponse("ERR syntax error"), false, nil
			}
			n, err := parseIntArgument(command[i+1])
			if err != nil || n <= 0 {
				return errorResponse("ERR invalid expire time in 'set' command"), false, nil
			}
			expirationSet = true
			switch strings.ToLower(string(command[i])) {
			case "ex":
				deadline, ok := relativeDeadline(s.opts.Now(), n, time.Second)
				if !ok {
					return errorResponse("ERR invalid expire time in 'set' command"), false, nil
				}
				opts.ExpireAt = deadline
			case "px":
				deadline, ok := relativeDeadline(s.opts.Now(), n, time.Millisecond)
				if !ok {
					return errorResponse("ERR invalid expire time in 'set' command"), false, nil
				}
				opts.ExpireAt = deadline
			case "exat":
				deadline, ok := absoluteDeadline(s.opts.Now(), n, 1000)
				if !ok {
					return errorResponse("ERR invalid expire time in 'set' command"), false, nil
				}
				opts.ExpireAt = deadline
			case "pxat":
				deadline, ok := absoluteDeadline(s.opts.Now(), n, 1)
				if !ok {
					return errorResponse("ERR invalid expire time in 'set' command"), false, nil
				}
				opts.ExpireAt = deadline
			}
			i++
		default:
			return errorResponse("ERR syntax error"), false, nil
		}
	}
	result, err := ops.Set(ctx, command[1], command[2], opts)
	if err != nil {
		return stateError(err)
	}
	if opts.GetPrevious {
		return valueResponse(result.Previous), false, nil
	}
	if !result.Applied {
		return nullResponse(), false, nil
	}
	return simpleResponse("OK"), false, nil
}

func (s *Server) executeExpire(ctx context.Context, ops state.Operations, command [][]byte) (response, bool, error) {
	if len(command) < 3 || len(command) > 4 {
		return wrongArguments(commandName(command)), false, nil
	}
	n, err := parseIntArgument(command[2])
	if err != nil {
		return errorResponse("ERR value is not an integer or out of range"), false, nil
	}
	name := commandName(command)
	var deadline time.Time
	var valid bool
	switch name {
	case "expire":
		deadline, valid = relativeDeadline(s.opts.Now(), n, time.Second)
	case "pexpire":
		deadline, valid = relativeDeadline(s.opts.Now(), n, time.Millisecond)
	case "expireat":
		deadline, valid = absoluteDeadline(s.opts.Now(), n, 1000)
	case "pexpireat":
		deadline, valid = absoluteDeadline(s.opts.Now(), n, 1)
	}
	if !valid {
		return errorResponse("ERR invalid expire time in '" + name + "' command"), false, nil
	}
	opts := state.ExpireOptions{}
	if len(command) == 4 {
		switch strings.ToLower(string(command[3])) {
		case "nx":
			opts.Condition = state.ExpireIfNoExpiry
		case "xx":
			opts.Condition = state.ExpireIfHasExpiry
		case "gt":
			opts.Condition = state.ExpireIfGreater
		case "lt":
			opts.Condition = state.ExpireIfLess
		default:
			return errorResponse("ERR Unsupported option " + string(command[3])), false, nil
		}
	}
	applied, err := ops.Expire(ctx, command[1], deadline, opts)
	if err != nil {
		return stateError(err)
	}
	return integerResponse(boolInt(applied)), false, nil
}

func executeIncrement(ctx context.Context, ops state.Operations, key []byte, delta int64) (response, bool, error) {
	value, err := ops.IncrBy(ctx, key, delta)
	if err != nil {
		return stateError(err)
	}
	return integerResponse(value), false, nil
}

func stateError(err error) (response, bool, error) {
	switch {
	case errors.Is(err, state.ErrNotInteger):
		return errorResponse("ERR value is not an integer or out of range"), false, nil
	case errors.Is(err, state.ErrOverflow):
		return errorResponse("ERR increment or decrement would overflow"), false, nil
	case errors.Is(err, state.ErrInvalidOptions):
		return errorResponse("ERR syntax error"), false, nil
	default:
		return response{}, false, err
	}
}

func valueResponse(value state.Value) response {
	if !value.Found {
		return nullResponse()
	}
	return bulkResponse(value.Bytes)
}

func parseIntArgument(value []byte) (int64, error) {
	return strconv.ParseInt(string(value), 10, 64)
}

func relativeDeadline(now time.Time, value int64, unit time.Duration) (time.Time, bool) {
	if value <= 0 {
		return now.Add(-time.Millisecond), true
	}
	if value > math.MaxInt64/int64(unit) {
		return time.Time{}, false
	}
	return now.Add(time.Duration(value) * unit), true
}

func absoluteDeadline(now time.Time, value, unitMillis int64) (time.Time, bool) {
	if value <= 0 {
		return now.Add(-time.Millisecond), true
	}
	if value > math.MaxInt64/unitMillis {
		return time.Time{}, false
	}
	targetMillis := value * unitMillis
	nowMillis := now.UnixMilli()
	if targetMillis > nowMillis {
		delta := uint64(targetMillis) - uint64(nowMillis)
		if delta > uint64(time.Duration(math.MaxInt64).Milliseconds()) {
			return time.Time{}, false
		}
	}
	return time.UnixMilli(targetMillis), true
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func wrongArguments(command string) response {
	return errorResponse("ERR wrong number of arguments for '" + command + "' command")
}

func cloneCommand(command [][]byte) [][]byte {
	cloned := make([][]byte, len(command))
	for i := range command {
		cloned[i] = append([]byte(nil), command[i]...)
	}
	return cloned
}

func validateQueuedCommand(command [][]byte) *response {
	name := commandName(command)
	valid := false
	switch name {
	case "ping":
		valid = len(command) == 1 || len(command) == 2
	case "echo", "select", "get", "type", "incr", "decr", "ttl", "pttl", "persist":
		valid = len(command) == 2
	case "set":
		valid = len(command) >= 3
	case "setnx", "incrby", "decrby":
		valid = len(command) == 3
	case "setex", "psetex":
		valid = len(command) == 4
	case "mget", "del", "unlink", "exists":
		valid = len(command) >= 2
	case "mset":
		valid = len(command) >= 3 && len(command)%2 == 1
	case "expire", "pexpire", "expireat", "pexpireat":
		valid = len(command) == 3 || len(command) == 4
	case "client":
		if len(command) >= 2 {
			switch strings.ToLower(string(command[1])) {
			case "setinfo":
				valid = len(command) == 4
			case "setname":
				valid = len(command) == 3
			case "getname", "id":
				valid = len(command) == 2
			case "maint_notifications":
				valid = true
			}
		}
	default:
		reply := errorResponse(fmt.Sprintf("ERR unknown command '%s'", name))
		return &reply
	}
	if !valid {
		reply := wrongArguments(name)
		return &reply
	}
	return nil
}
