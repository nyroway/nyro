package redis

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
)

type respReader struct {
	r       *bufio.Reader
	maxSize int
	maxArgs int
}

func newRESPReader(r io.Reader, maxSize, maxArgs int) *respReader {
	return &respReader{r: bufio.NewReader(r), maxSize: maxSize, maxArgs: maxArgs}
}

func (r *respReader) readCommand() ([][]byte, error) {
	line, size, err := r.readLine(r.maxSize)
	if err != nil {
		return nil, err
	}
	if len(line) == 0 || line[0] != '*' {
		return nil, errors.New("redis: protocol error: expected array command")
	}
	n, err := parseRESPInt(line[1:])
	if err != nil || n <= 0 || n > int64(r.maxArgs) {
		return nil, errors.New("redis: protocol error: invalid argument count")
	}
	command := make([][]byte, int(n))
	for i := range command {
		line, consumed, err := r.readLine(r.maxSize - size)
		size += consumed
		if err != nil {
			return nil, err
		}
		if len(line) == 0 || line[0] != '$' {
			return nil, errors.New("redis: protocol error: expected bulk string")
		}
		length, err := parseRESPInt(line[1:])
		if err != nil || length < 0 || length > int64(r.maxSize-size-2) {
			return nil, errors.New("redis: protocol error: invalid bulk length")
		}
		value := make([]byte, int(length))
		if _, err := io.ReadFull(r.r, value); err != nil {
			return nil, fmt.Errorf("redis: protocol error: read bulk string: %w", err)
		}
		var trailer [2]byte
		if _, err := io.ReadFull(r.r, trailer[:]); err != nil {
			return nil, fmt.Errorf("redis: protocol error: read bulk trailer: %w", err)
		}
		if trailer != [2]byte{'\r', '\n'} {
			return nil, errors.New("redis: protocol error: invalid bulk trailer")
		}
		size += int(length) + 2
		command[i] = value
	}
	return command, nil
}

func (r *respReader) readLine(remaining int) ([]byte, int, error) {
	if remaining <= 0 {
		return nil, 0, errors.New("redis: protocol error: request too large")
	}
	var line []byte
	for {
		part, err := r.r.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > remaining {
			return nil, len(line), errors.New("redis: protocol error: request too large")
		}
		if err == nil {
			break
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, len(line), err
		}
	}
	if len(line) < 2 || line[len(line)-2] != '\r' {
		return nil, len(line), errors.New("redis: protocol error: line is not CRLF terminated")
	}
	return line[:len(line)-2], len(line), nil
}

func parseRESPInt(value []byte) (int64, error) {
	return strconv.ParseInt(string(value), 10, 64)
}

type responseKind uint8

const (
	responseSimple responseKind = iota
	responseError
	responseBulk
	responseNull
	responseInteger
	responseArray
	responseMap
)

type response struct {
	kind    responseKind
	text    string
	bytes   []byte
	integer int64
	items   []response
	mapData []response
}

func simpleResponse(value string) response { return response{kind: responseSimple, text: value} }
func errorResponse(value string) response  { return response{kind: responseError, text: value} }
func bulkResponse(value []byte) response {
	return response{kind: responseBulk, bytes: append([]byte(nil), value...)}
}
func nullResponse() response                  { return response{kind: responseNull} }
func integerResponse(value int64) response    { return response{kind: responseInteger, integer: value} }
func arrayResponse(items []response) response { return response{kind: responseArray, items: items} }
func mapResponse(pairs ...response) response  { return response{kind: responseMap, mapData: pairs} }

type respWriter struct {
	w     *bufio.Writer
	proto int
}

func newRESPWriter(w io.Writer, proto int) *respWriter {
	return &respWriter{w: bufio.NewWriter(w), proto: proto}
}

func (w *respWriter) write(value response) error {
	switch value.kind {
	case responseSimple:
		_, _ = fmt.Fprintf(w.w, "+%s\r\n", value.text)
	case responseError:
		_, _ = fmt.Fprintf(w.w, "-%s\r\n", value.text)
	case responseBulk:
		_, _ = fmt.Fprintf(w.w, "$%d\r\n", len(value.bytes))
		_, _ = w.w.Write(value.bytes)
		_, _ = w.w.WriteString("\r\n")
	case responseNull:
		if w.proto == 3 {
			_, _ = w.w.WriteString("_\r\n")
		} else {
			_, _ = w.w.WriteString("$-1\r\n")
		}
	case responseInteger:
		_, _ = fmt.Fprintf(w.w, ":%d\r\n", value.integer)
	case responseArray:
		_, _ = fmt.Fprintf(w.w, "*%d\r\n", len(value.items))
		for _, item := range value.items {
			if err := w.write(item); err != nil {
				return err
			}
		}
	case responseMap:
		if len(value.mapData)%2 != 0 {
			return errors.New("redis: internal response map has an odd item count")
		}
		if w.proto == 3 {
			_, _ = fmt.Fprintf(w.w, "%%%d\r\n", len(value.mapData)/2)
		} else {
			_, _ = fmt.Fprintf(w.w, "*%d\r\n", len(value.mapData))
		}
		for _, item := range value.mapData {
			if err := w.write(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *respWriter) flush() error { return w.w.Flush() }
