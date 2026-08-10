// Package redis exposes a deliberately limited Redis-compatible RESP2/RESP3
// server over the generic state String-and-TTL interface.
package redis

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nyroway/nyro/go/internal/platform/state"
)

const (
	defaultMaxRequestBytes = 16 << 20
	defaultMaxArguments    = 1024
)

// Options configures the Redis protocol server.
type Options struct {
	Store           state.Store
	Password        string
	Logger          *slog.Logger
	MaxRequestBytes int
	MaxArguments    int
	Now             func() time.Time
}

// Server serves Redis-compatible connections over a caller-provided listener.
type Server struct {
	opts     Options
	mu       sync.Mutex
	listener net.Listener
	conns    map[net.Conn]struct{}
	wg       sync.WaitGroup
	stopping bool
	stopOnce sync.Once
	connID   atomic.Int64
}

// New validates options and returns an idle server.
func New(opts Options) (*Server, error) {
	if opts.Store == nil {
		return nil, errors.New("redis: state store is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.MaxRequestBytes <= 0 {
		opts.MaxRequestBytes = defaultMaxRequestBytes
	}
	if opts.MaxArguments <= 0 {
		opts.MaxArguments = defaultMaxArguments
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Server{opts: opts, conns: make(map[net.Conn]struct{})}, nil
}

// Serve takes serving-lifecycle ownership of listener and accepts connections
// until Shutdown closes it. The caller still chooses and creates the listener.
func (s *Server) Serve(listener net.Listener) error {
	if listener == nil {
		return errors.New("redis: listener is required")
	}
	s.mu.Lock()
	if s.listener != nil || s.stopping {
		s.mu.Unlock()
		return errors.New("redis: server already started or stopped")
	}
	s.listener = listener
	s.mu.Unlock()

	for {
		conn, err := listener.Accept()
		if err != nil {
			s.mu.Lock()
			stopping := s.stopping
			s.mu.Unlock()
			if stopping || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("redis: accept: %w", err)
		}
		s.mu.Lock()
		if s.stopping {
			s.mu.Unlock()
			_ = conn.Close()
			continue
		}
		s.conns[conn] = struct{}{}
		s.wg.Add(1)
		s.mu.Unlock()
		go s.serveConnection(conn)
	}
}

func (s *Server) serveConnection(conn net.Conn) {
	defer func() {
		_ = conn.Close()
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		s.wg.Done()
	}()

	connection := &connectionState{
		id:            s.connID.Add(1),
		proto:         2,
		authenticated: s.opts.Password == "",
	}
	reader := newRESPReader(conn, s.opts.MaxRequestBytes, s.opts.MaxArguments)
	writer := newRESPWriter(conn, connection.proto)
	for {
		command, err := reader.readCommand()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
				writer.proto = connection.proto
				_ = writer.write(errorResponse("ERR " + err.Error()))
				_ = writer.flush()
			}
			return
		}
		reply, closeConnection, err := s.execute(context.Background(), connection, command)
		if err != nil {
			reply = errorResponse("ERR " + err.Error())
		}
		writer.proto = connection.proto
		if err := writer.write(reply); err != nil {
			return
		}
		if err := writer.flush(); err != nil {
			return
		}
		if closeConnection {
			return
		}
	}
}

// Shutdown closes the listener and active connections, then waits for all
// handlers. It does not close the state store.
func (s *Server) Shutdown(ctx context.Context) error {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		s.stopping = true
		listener := s.listener
		connections := make([]net.Conn, 0, len(s.conns))
		for conn := range s.conns {
			connections = append(connections, conn)
		}
		s.mu.Unlock()
		if listener != nil {
			_ = listener.Close()
		}
		for _, conn := range connections {
			_ = conn.Close()
		}
	})
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
