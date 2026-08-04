package tool

import "testing"

func TestNewCmdSubcommands(t *testing.T) {
	cmd := NewCmd()
	if cmd.Use != "tool" {
		t.Errorf("Use = %q, want tool", cmd.Use)
	}
	if cmd.Short != "Operational tools" {
		t.Errorf("Short = %q, want Operational tools", cmd.Short)
	}

	names := map[string]bool{}
	for _, child := range cmd.Commands() {
		names[child.Name()] = true
	}
	if !names["ca"] {
		t.Error("ca subcommand missing")
	}
	if !names["migrate"] {
		t.Error("migrate subcommand missing")
	}
}
