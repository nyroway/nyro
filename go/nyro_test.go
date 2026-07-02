package main

import "testing"

func TestRootCmdSubcommands(t *testing.T) {
	root := newRootCmd()
	names := map[string]bool{}
	for _, c := range root.Commands() {
		names[c.Name()] = true
	}
	if names["completion"] {
		t.Error("completion subcommand should be disabled")
	}
	if names["tool"] {
		t.Error("tool subcommand should be removed")
	}
	if !names["gateway"] {
		t.Error("gateway subcommand missing")
	}
	if !names["admin"] {
		t.Error("admin subcommand missing")
	}
}

func TestRootCmdNoGlobalStorageFlags(t *testing.T) {
	root := newRootCmd()
	if f := root.PersistentFlags().Lookup("storage"); f != nil {
		t.Error("--storage must not be a global/root flag (it belongs to admin only)")
	}
	if f := root.PersistentFlags().Lookup("db-dsn"); f != nil {
		t.Error("--db-dsn must not be a global/root flag (it belongs to admin only)")
	}
}
