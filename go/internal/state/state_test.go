package state

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	prev := homeDirFunc
	homeDirFunc = func() (string, error) { return dir, nil }
	t.Cleanup(func() { homeDirFunc = prev })
	return dir
}

func TestWriteReadRoundTrip(t *testing.T) {
	withTempHome(t)

	want := ServerState{
		PID:         os.Getpid(),
		Listen:      "127.0.0.1:19531",
		ProxyListen: "127.0.0.1:19530",
		SyncListen:  "",
		StartedAt:   time.Now().UTC().Truncate(time.Second),
		AdminToken:  "test-token",
	}
	if err := Write(want); err != nil {
		t.Fatalf("Write: %v", err)
	}

	path, err := StatePath()
	if err != nil {
		t.Fatalf("StatePath: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat state file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode = %o, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("state dir mode = %o, want 0700", dirInfo.Mode().Perm())
	}

	got, err := Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.PID != want.PID || got.Listen != want.Listen || got.ProxyListen != want.ProxyListen || got.SyncListen != want.SyncListen || got.AdminToken != want.AdminToken {
		t.Fatalf("Read() = %+v, want %+v", got, want)
	}
	if got.StartedAt.Unix() != want.StartedAt.Unix() {
		t.Fatalf("StartedAt = %v, want %v", got.StartedAt, want.StartedAt)
	}
}

func TestReadMissingFile(t *testing.T) {
	withTempHome(t)
	_, err := Read()
	if err == nil || err.Error() != "nyro server is not running" {
		t.Fatalf("Read() error = %v, want nyro server is not running", err)
	}
}

func TestReadStalePIDRemovesFile(t *testing.T) {
	withTempHome(t)

	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	deadPID := cmd.Process.Pid
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()

	if err := Write(ServerState{
		PID:       deadPID,
		Listen:    "127.0.0.1:19531",
		StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	path, _ := StatePath()

	_, err := Read()
	if err == nil || err.Error() != "nyro server is not running (stale state file removed)" {
		t.Fatalf("Read() error = %v, want stale state message", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale state file still present: %v", err)
	}
}

func TestRemoveIdempotent(t *testing.T) {
	withTempHome(t)
	Remove() // missing file
	if err := Write(ServerState{PID: os.Getpid(), Listen: "127.0.0.1:1", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	Remove()
	Remove() // second call must not panic
	path, _ := StatePath()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("state file still present after Remove")
	}
}

func TestAdminBaseURL(t *testing.T) {
	cases := []struct {
		listen string
		want   string
	}{
		{"127.0.0.1:19531", "http://127.0.0.1:19531"},
		{"0.0.0.0:19531", "http://127.0.0.1:19531"},
		{"0.0.0.0", "http://127.0.0.1"},
		{"http://example.com:8080", "http://example.com:8080"},
		{"", "http://127.0.0.1:19531"},
		{"[::]:19531", "http://127.0.0.1:19531"},
	}
	for _, tc := range cases {
		got := (ServerState{Listen: tc.listen}).AdminBaseURL()
		if got != tc.want {
			t.Fatalf("AdminBaseURL(%q) = %q, want %q", tc.listen, got, tc.want)
		}
	}
}
