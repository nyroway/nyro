package main

import "testing"

func TestRootCmdSubcommands(t *testing.T) {
	root := newRootCmd()
	names := map[string]bool{}
	aliases := map[string]bool{}
	for _, c := range root.Commands() {
		names[c.Name()] = true
		for _, alias := range c.Aliases {
			aliases[alias] = true
		}
	}
	if names["completion"] {
		t.Error("completion subcommand should be disabled")
	}
	if !names["tool"] {
		t.Error("tool subcommand missing")
	}
	if !names["proxy"] {
		t.Error("proxy subcommand missing")
	}
	if !names["serve"] {
		t.Error("serve subcommand missing")
	}
	if names["server"] || aliases["server"] {
		t.Error("server subcommand should have been renamed to serve without an alias")
	}
	// The pre-rename names must be fully gone: the rename ships without a
	// compatibility period, so a lingering alias would be a bug, not a courtesy.
	if names["gateway"] {
		t.Error("gateway subcommand should have been renamed to proxy")
	}
	if names["admin"] {
		t.Error("admin subcommand should have been renamed to serve")
	}
	if names["ca"] || aliases["ca"] {
		t.Error("ca must be available only under tool")
	}
	if names["migrate"] || aliases["migrate"] {
		t.Error("migrate must be available only under tool")
	}

	tool, _, err := root.Find([]string{"tool"})
	if err != nil {
		t.Fatalf("find tool command: %v", err)
	}
	toolNames := map[string]bool{}
	for _, c := range tool.Commands() {
		toolNames[c.Name()] = true
	}
	if !toolNames["ca"] {
		t.Error("tool ca subcommand missing")
	}
	if !toolNames["migrate"] {
		t.Error("tool migrate subcommand missing")
	}
}

func TestRootCmdNoGlobalStorageFlags(t *testing.T) {
	root := newRootCmd()
	if f := root.PersistentFlags().Lookup("dsn"); f != nil {
		t.Error("--dsn must not be a global/root flag (it belongs to admin only)")
	}
}
