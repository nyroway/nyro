// Package state manages the server runtime state file (~/.nyro/server.json).
//
// Layer: 0 (foundation) — stdlib only; shared by cmd/server and cmd/ctl.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nyroway/nyro/go/internal/defaults"
)

// ServerState records the runtime listen addresses resolved after server flag
// parsing. Written by `nyro server` and read by CLI commands to locate the
// Admin API.
type ServerState struct {
	PID         int       `json:"pid"`
	Listen      string    `json:"listen"`
	ProxyListen string    `json:"proxy_listen"`
	SyncListen  string    `json:"sync_listen"`
	StartedAt   time.Time `json:"started_at"`
	AdminToken  string    `json:"admin_token,omitempty"`
}

// homeDirFunc is swapped in tests to redirect ~/.nyro without changing HOME
// for the whole process.
var homeDirFunc = os.UserHomeDir

// StatePath returns the absolute path to ~/.nyro/server.json.
func StatePath() (string, error) {
	home, err := homeDirFunc()
	if err != nil || home == "" {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".nyro", "server.json"), nil
}

// Write serializes state to the state file with mode 0600, creating the
// parent directory (0700) when missing.
func Write(state ServerState) error {
	path, err := StatePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal server state: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write server state: %w", err)
	}
	return nil
}

// Read loads the state file and verifies the recorded PID is still alive.
// When the PID is dead it removes the stale file and returns an error.
func Read() (ServerState, error) {
	path, err := StatePath()
	if err != nil {
		return ServerState{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ServerState{}, errors.New("nyro server is not running")
		}
		return ServerState{}, fmt.Errorf("read server state: %w", err)
	}
	var state ServerState
	if err := json.Unmarshal(data, &state); err != nil {
		return ServerState{}, fmt.Errorf("parse server state: %w", err)
	}
	if state.PID <= 0 {
		Remove()
		return ServerState{}, errors.New("nyro server is not running (stale state file removed)")
	}
	if err := syscall.Kill(state.PID, 0); err != nil {
		Remove()
		return ServerState{}, errors.New("nyro server is not running (stale state file removed)")
	}
	return state, nil
}

// Remove deletes the state file. Missing files are ignored.
func Remove() {
	path, err := StatePath()
	if err != nil {
		return
	}
	_ = os.Remove(path)
}

// AdminBaseURL converts Listen into an http://host:port URL, rewriting
// 0.0.0.0 to 127.0.0.1 for local client access.
func (s ServerState) AdminBaseURL() string {
	listen := strings.TrimSpace(s.Listen)
	if listen == "" {
		return defaults.ControlPlaneBaseURL
	}
	if strings.HasPrefix(listen, "http://") || strings.HasPrefix(listen, "https://") {
		return listen
	}
	host := listen
	if strings.HasPrefix(host, "0.0.0.0:") {
		host = "127.0.0.1" + strings.TrimPrefix(host, "0.0.0.0")
	} else if host == "0.0.0.0" {
		host = "127.0.0.1"
	} else if strings.HasPrefix(host, "[::]:") {
		host = "127.0.0.1" + strings.TrimPrefix(host, "[::]")
	} else if host == "[::]" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + host
}
