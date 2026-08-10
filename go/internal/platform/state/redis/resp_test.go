package redis

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

type oneByteReader struct{ reader io.Reader }

func (r oneByteReader) Read(buffer []byte) (int, error) {
	if len(buffer) > 1 {
		buffer = buffer[:1]
	}
	return r.reader.Read(buffer)
}

func TestRESPReaderParsesBinaryPipeline(t *testing.T) {
	input := "*2\r\n$3\r\nSET\r\n$4\r\na\x00b\xff\r\n*2\r\n$3\r\nGET\r\n$1\r\na\r\n"
	reader := newRESPReader(strings.NewReader(input), 1024, 8)
	first, err := reader.readCommand()
	if err != nil {
		t.Fatalf("first readCommand() error = %v", err)
	}
	if len(first) != 2 || string(first[0]) != "SET" || !bytes.Equal(first[1], []byte{'a', 0, 'b', 255}) {
		t.Fatalf("first command = %#v", first)
	}
	second, err := reader.readCommand()
	if err != nil {
		t.Fatalf("second readCommand() error = %v", err)
	}
	if len(second) != 2 || string(second[0]) != "GET" || string(second[1]) != "a" {
		t.Fatalf("second command = %#v", second)
	}
}

func TestRESPReaderParsesFragmentedFrame(t *testing.T) {
	input := oneByteReader{reader: strings.NewReader("*2\r\n$4\r\nECHO\r\n$5\r\nhello\r\n")}
	command, err := newRESPReader(input, 1024, 8).readCommand()
	if err != nil {
		t.Fatalf("readCommand() error = %v", err)
	}
	if len(command) != 2 || string(command[0]) != "ECHO" || string(command[1]) != "hello" {
		t.Fatalf("command = %#v", command)
	}
}

func TestRESPReaderRejectsLimitsAndMalformedFrames(t *testing.T) {
	for name, input := range map[string]string{
		"arguments": "*3\r\n$1\r\na\r\n$1\r\nb\r\n$1\r\nc\r\n",
		"request":   "*1\r\n$9\r\n123456789\r\n",
		"trailer":   "*1\r\n$1\r\naXX",
		"not array": "+PING\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			reader := newRESPReader(strings.NewReader(input), 16, 2)
			if _, err := reader.readCommand(); err == nil {
				t.Fatal("readCommand() error = nil")
			}
		})
	}
}

func TestRESPWriterUsesProtocolSpecificNullAndMap(t *testing.T) {
	value := mapResponse(bulkResponse([]byte("proto")), integerResponse(3))
	for _, tt := range []struct {
		proto int
		want  string
	}{
		{proto: 2, want: "*2\r\n$5\r\nproto\r\n:3\r\n$-1\r\n"},
		{proto: 3, want: "%1\r\n$5\r\nproto\r\n:3\r\n_\r\n"},
	} {
		var output bytes.Buffer
		writer := newRESPWriter(&output, tt.proto)
		if err := writer.write(value); err != nil {
			t.Fatalf("write map: %v", err)
		}
		if err := writer.write(nullResponse()); err != nil {
			t.Fatalf("write null: %v", err)
		}
		if err := writer.flush(); err != nil {
			t.Fatalf("flush: %v", err)
		}
		if got := output.String(); got != tt.want {
			t.Fatalf("protocol %d output = %q, want %q", tt.proto, got, tt.want)
		}
	}
}

func FuzzRESPReader(f *testing.F) {
	f.Add("*1\r\n$4\r\nPING\r\n")
	f.Add("*2\r\n$3\r\nGET\r\n$1\r\na\r\n")
	f.Fuzz(func(t *testing.T, input string) {
		reader := newRESPReader(strings.NewReader(input), 4096, 32)
		_, _ = reader.readCommand()
	})
}
