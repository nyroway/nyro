package httpserver

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
)

func recoverPanics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		state, observed := observeResponse(writer)
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if recovered == http.ErrAbortHandler || state.committed {
				panic(recovered)
			}
			http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		}()
		next.ServeHTTP(observed, request)
	})
}

type responseState struct {
	http.ResponseWriter
	committed bool
}

func (state *responseState) WriteHeader(status int) {
	if status == http.StatusSwitchingProtocols || status >= http.StatusOK && status <= 999 {
		state.committed = true
	}
	state.ResponseWriter.WriteHeader(status)
}

func (state *responseState) Write(payload []byte) (int, error) {
	state.committed = true
	return state.ResponseWriter.Write(payload)
}

func (state *responseState) Unwrap() http.ResponseWriter {
	return state.ResponseWriter
}

func (state *responseState) flush() {
	state.committed = true
	state.ResponseWriter.(http.Flusher).Flush()
}

func (state *responseState) hijack() (net.Conn, *bufio.ReadWriter, error) {
	connection, buffered, err := state.ResponseWriter.(http.Hijacker).Hijack()
	if err == nil {
		state.committed = true
	}
	return connection, buffered, err
}

func (state *responseState) push(target string, options *http.PushOptions) error {
	return state.ResponseWriter.(http.Pusher).Push(target, options)
}

type flusherResponseWriter struct{ *responseState }

func (writer *flusherResponseWriter) Flush() { writer.flush() }

type hijackerResponseWriter struct{ *responseState }

func (writer *hijackerResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return writer.hijack()
}

type pusherResponseWriter struct{ *responseState }

func (writer *pusherResponseWriter) Push(target string, options *http.PushOptions) error {
	return writer.push(target, options)
}

type flusherHijackerResponseWriter struct{ *responseState }

func (writer *flusherHijackerResponseWriter) Flush() { writer.flush() }
func (writer *flusherHijackerResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return writer.hijack()
}

type flusherPusherResponseWriter struct{ *responseState }

func (writer *flusherPusherResponseWriter) Flush() { writer.flush() }
func (writer *flusherPusherResponseWriter) Push(target string, options *http.PushOptions) error {
	return writer.push(target, options)
}

type hijackerPusherResponseWriter struct{ *responseState }

func (writer *hijackerPusherResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return writer.hijack()
}

func (writer *hijackerPusherResponseWriter) Push(target string, options *http.PushOptions) error {
	return writer.push(target, options)
}

type flusherHijackerPusherResponseWriter struct{ *responseState }

func (writer *flusherHijackerPusherResponseWriter) Flush() { writer.flush() }
func (writer *flusherHijackerPusherResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return writer.hijack()
}

func (writer *flusherHijackerPusherResponseWriter) Push(target string, options *http.PushOptions) error {
	return writer.push(target, options)
}

func observeResponse(writer http.ResponseWriter) (*responseState, http.ResponseWriter) {
	state := &responseState{ResponseWriter: writer}
	_, flushes := writer.(http.Flusher)
	_, hijacks := writer.(http.Hijacker)
	_, pushes := writer.(http.Pusher)
	switch {
	case flushes && hijacks && pushes:
		return state, &flusherHijackerPusherResponseWriter{responseState: state}
	case flushes && hijacks:
		return state, &flusherHijackerResponseWriter{responseState: state}
	case flushes && pushes:
		return state, &flusherPusherResponseWriter{responseState: state}
	case hijacks && pushes:
		return state, &hijackerPusherResponseWriter{responseState: state}
	case flushes:
		return state, &flusherResponseWriter{responseState: state}
	case hijacks:
		return state, &hijackerResponseWriter{responseState: state}
	case pushes:
		return state, &pusherResponseWriter{responseState: state}
	default:
		return state, state
	}
}

func writeStatus(writer http.ResponseWriter, status int, value string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = fmt.Fprintf(writer, `{"status":%q}`, value)
}
