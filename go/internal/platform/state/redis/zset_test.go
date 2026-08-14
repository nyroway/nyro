package redis

import (
	"errors"
	"testing"

	"github.com/nyroway/nyro/go/internal/platform/state"
)

func TestZSetEncodingIsDeterministicAndBinarySafe(t *testing.T) {
	members := map[string]float64{
		string([]byte{0, 1, 255}): 2,
		"alpha":                   1,
	}
	got, err := encodeZSet(members)
	if err != nil {
		t.Fatal(err)
	}
	want := append(
		[]byte{0, 'n', 'y', 'r', 'o', ':', 'z', 's', 'e', 't', ':', 'v', '1', 0},
		[]byte("[{\"member\":\"AAH/\",\"score\":2},{\"member\":\"YWxwaGE\",\"score\":1}]")...,
	)
	if string(got) != string(want) {
		t.Fatalf("encodeZSet() = %q, want %q", got, want)
	}

	decoded, found, err := decodeZSetValue(state.Value{Bytes: got, Found: true})
	if err != nil {
		t.Fatal(err)
	}
	if !found || decoded[string([]byte{0, 1, 255})] != 2 || decoded["alpha"] != 1 {
		t.Fatalf("decodeZSetValue() = %#v, %v", decoded, found)
	}
}

func TestDecodeZSetRejectsStringValueAsWrongType(t *testing.T) {
	_, _, err := decodeZSetValue(state.Value{Bytes: []byte("counter"), Found: true})
	if !errors.Is(err, errWrongType) {
		t.Fatalf("decodeZSetValue() error = %v, want errWrongType", err)
	}
}
