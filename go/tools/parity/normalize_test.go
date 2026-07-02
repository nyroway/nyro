package parity

import (
	"testing"
)

func TestNormalizeDropsVolatileAndSortsKeys(t *testing.T) {
	t.Parallel()
	in := []byte(`{"model":"gpt-4o","id":"chatcmpl-xyz","created":1700000000,"choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"object":"chat.completion"}`)
	got, err := NormalizeJSON(in)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	// Sorted-key canonical form, volatile id/created dropped.
	want := `{"choices":[{"index":0,"message":{"content":"hi","role":"assistant"}}],"model":"gpt-4o","object":"chat.completion"}`
	if string(got) != want {
		t.Errorf("normalized:\n got %s\nwant %s", got, want)
	}
}

func TestNormalizeEquivResponsesMatch(t *testing.T) {
	t.Parallel()
	a := []byte(`{"id":"A","content":"hi","created":1}`)
	b := []byte(`{"created":2,"id":"B","content":"hi"}`)
	na, _ := NormalizeJSON(a)
	nb, _ := NormalizeJSON(b)
	if string(na) != string(nb) {
		t.Errorf("equivalent responses differ after normalize:\n %s\n %s", na, nb)
	}
}
