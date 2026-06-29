package tool

import (
	"testing"
)

func TestNewCmdHasVersion(t *testing.T) {
	cmd := NewCmd()
	if cmd.Use != "tool" {
		t.Errorf("Use = %q, want tool", cmd.Use)
	}
	found := false
	for _, c := range cmd.Commands() {
		if c.Use == "version" {
			found = true
		}
	}
	if !found {
		t.Error("expected a 'version' subcommand")
	}
}
