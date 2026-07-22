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
	if !names["proxy"] {
		t.Error("proxy subcommand missing")
	}
	if !names["server"] {
		t.Error("server subcommand missing")
	}
	// The pre-rename names must be fully gone: the rename ships without a
	// compatibility period, so a lingering alias would be a bug, not a courtesy.
	if names["gateway"] {
		t.Error("gateway subcommand should have been renamed to proxy")
	}
	if names["admin"] {
		t.Error("admin subcommand should have been renamed to server")
	}
}

func TestRootCmdNoGlobalStorageFlags(t *testing.T) {
	root := newRootCmd()
	if f := root.PersistentFlags().Lookup("dsn"); f != nil {
		t.Error("--dsn must not be a global/root flag (it belongs to admin only)")
	}
}
