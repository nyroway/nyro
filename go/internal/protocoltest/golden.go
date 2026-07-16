package protocoltest

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nyroway/nyro/go/tools/parity"
)

// updateGolden regenerates golden files (and, in record mode, is paired with
// re-recording cassettes). Run: `go test ./internal/protocoltest/... -update`.
var updateGolden = flag.Bool("update", false, "rewrite golden files from current output")

// canonJSON normalizes a JSON body for stable comparison: parity.NormalizeJSON
// drops volatile fields (id/created/timestamps/…) and sorts keys, then we
// indent for a human-reviewable golden diff.
func canonJSON(body []byte) ([]byte, error) {
	norm, err := parity.NormalizeJSON(body)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, norm, "", "  "); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// canonSSE normalizes a raw SSE stream body: each data-frame's JSON is
// canonicalized independently and the frames are rejoined, so event order is
// asserted but volatile per-event fields are ignored.
func canonSSE(body string) ([]byte, error) {
	var out bytes.Buffer
	for i, frame := range sseFrames(body) {
		c, err := canonJSON([]byte(frame))
		if err != nil {
			// Non-JSON frame (rare) — compare verbatim.
			c = []byte(frame)
		}
		if i > 0 {
			out.WriteString("\n---\n")
		}
		out.Write(c)
	}
	return out.Bytes(), nil
}

// assertGolden compares got against the golden file at path (relative to the
// test package's testdata dir is the caller's responsibility — pass a full
// path). With -update it rewrites the file instead. got must already be
// canonicalized.
func assertGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir golden dir: %v", err)
		}
		if err := os.WriteFile(path, append(bytes.TrimRight(got, "\n"), '\n'), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run `-update` to create it)", path, err)
	}
	gotN := strings.TrimRight(string(got), "\n")
	wantN := strings.TrimRight(string(want), "\n")
	if gotN != wantN {
		t.Errorf("golden mismatch: %s\n%s", path, lineDiff(wantN, gotN))
	}
}

// lineDiff renders a compact line-by-line diff (want = -, got = +) for golden
// mismatches without pulling in an external diff dependency.
func lineDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	n := max(len(wl), len(gl))
	for i := range n {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w == g {
			continue
		}
		if i < len(wl) {
			b.WriteString("- " + w + "\n")
		}
		if i < len(gl) {
			b.WriteString("+ " + g + "\n")
		}
	}
	return b.String()
}
