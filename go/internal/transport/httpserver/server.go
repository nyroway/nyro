// Package httpserver owns the process-level northbound HTTP server skeleton.
// It depends only on net/http-facing infrastructure and handlers explicitly
// supplied by a composition root.
package httpserver

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"
)

const defaultReadHeaderTimeout = 10 * time.Second

// Options configures one process-level HTTP server.
type Options struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	Ready             func() bool
}

// Handler mounts one explicitly supplied HTTP handler at Pattern.
type Handler struct {
	Pattern string
	Handler http.Handler
}

// Server owns a net/http Server and its generic health, readiness, recovery,
// and handler-mounting behavior.
type Server struct {
	server *http.Server
}

// New constructs an HTTP Server without opening a listener.
func New(options Options, handlers ...Handler) *Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writeStatus(writer, http.StatusOK, "ok")
	})
	mux.HandleFunc("GET /readyz", func(writer http.ResponseWriter, _ *http.Request) {
		if options.Ready == nil || !options.Ready() {
			writeStatus(writer, http.StatusServiceUnavailable, "unready")
			return
		}
		writeStatus(writer, http.StatusOK, "ready")
	})
	for _, mounted := range handlers {
		if mounted.Pattern == "" || mounted.Handler == nil {
			continue
		}
		mux.Handle(mounted.Pattern, mounted.Handler)
	}
	timeout := options.ReadHeaderTimeout
	if timeout <= 0 {
		timeout = defaultReadHeaderTimeout
	}
	return &Server{server: &http.Server{
		Addr:              options.Addr,
		Handler:           recoverPanics(mux),
		ReadHeaderTimeout: timeout,
	}}
}

// Handler returns the complete mounted handler tree.
func (server *Server) Handler() http.Handler {
	if server == nil || server.server == nil {
		return http.NotFoundHandler()
	}
	return server.server.Handler
}

// Serve accepts requests from listener until shutdown.
func (server *Server) Serve(listener net.Listener) error {
	if server == nil || server.server == nil {
		return errors.New("http server is not configured")
	}
	err := server.server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// ListenAndServe opens the configured address and serves until shutdown.
func (server *Server) ListenAndServe() error {
	if server == nil || server.server == nil {
		return errors.New("http server is not configured")
	}
	err := server.server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown gracefully stops accepting requests and drains active handlers.
func (server *Server) Shutdown(ctx context.Context) error {
	if server == nil || server.server == nil {
		return nil
	}
	return server.server.Shutdown(ctx)
}
