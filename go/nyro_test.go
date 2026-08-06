package main

import (
	"bytes"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

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
		t.Error("--dsn must not be a global/root flag (it belongs to serve only)")
	}
}

func TestRootCmdToolCommandsExecuteOnlyUnderTool(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantErr    bool
		wantOutput string
	}{
		{
			name:    "direct ca is rejected",
			args:    []string{"ca"},
			wantErr: true,
		},
		{
			name:    "direct migrate is rejected",
			args:    []string{"migrate"},
			wantErr: true,
		},
		{
			name:       "tool ca help resolves",
			args:       []string{"tool", "ca", "--help"},
			wantOutput: "Usage:\n  nyro tool ca",
		},
		{
			name:       "tool migrate help resolves",
			args:       []string{"tool", "migrate", "--help"},
			wantOutput: "Usage:\n  nyro tool migrate",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRootCmd()
			var output bytes.Buffer
			root.SetOut(&output)
			root.SetErr(&output)
			root.SetArgs(tt.args)
			root.SilenceUsage = true
			root.SilenceErrors = true

			err := root.Execute()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Execute(%q) error = %v, wantErr %v", tt.args, err, tt.wantErr)
			}
			if tt.wantOutput != "" && !strings.Contains(output.String(), tt.wantOutput) {
				t.Errorf("Execute(%q) output = %q, want substring %q", tt.args, output.String(), tt.wantOutput)
			}
		})
	}
}

func TestPublicHelpCopyIsConcise(t *testing.T) {
	root := newRootCmd()
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		if cmd.Hidden {
			return
		}
		if strings.Contains(cmd.Short, "`") || strings.Contains(cmd.Long, "`") {
			t.Errorf("%s command help contains backticks", cmd.CommandPath())
		}
		if utf8.RuneCountInString(cmd.Short) > 48 {
			t.Errorf("%s short help is too long: %q", cmd.CommandPath(), cmd.Short)
		}
		if cmd.Long != "" {
			t.Errorf("%s has long help; keep terminal help concise", cmd.CommandPath())
		}
		cmd.LocalNonPersistentFlags().VisitAll(func(flag *pflag.Flag) {
			usage, _, _ := strings.Cut(flag.Usage, " (env ")
			if strings.Contains(usage, "`") {
				t.Errorf("%s --%s help contains backticks", cmd.CommandPath(), flag.Name)
			}
			if utf8.RuneCountInString(usage) > 64 {
				t.Errorf("%s --%s help is too long: %q", cmd.CommandPath(), flag.Name, usage)
			}
		})
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	visit(root)
}

func TestHelpOutputUsesConciseDescriptions(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{"--help"}, want: "serve       Start Nyro with embedded services"},
		{args: []string{"serve", "--help"}, want: "Create or update database tables on startup"},
		{args: []string{"proxy", "--help"}, want: "Token used to join config sync"},
		{args: []string{"tool", "ca", "--help"}, want: "Manage config sync certificates"},
		{args: []string{"tool", "migrate", "diff", "--help"}, want: "Writable scratch database DSN"},
	}
	for _, tt := range tests {
		root := newRootCmd()
		var output bytes.Buffer
		root.SetOut(&output)
		root.SetErr(&output)
		root.SetArgs(tt.args)
		if err := root.Execute(); err != nil {
			t.Fatalf("Execute(%q): %v", tt.args, err)
		}
		got := output.String()
		if !strings.Contains(got, tt.want) {
			t.Errorf("help for %q does not contain %q:\n%s", tt.args, tt.want, got)
		}
		for _, unwanted := range []string{
			"--auto-migrate nyro tool migrate dump",
			"--sync-listen nyro proxy",
			"--sync-token nyro proxy",
			"--sync-tls-ca nyro tool ca",
		} {
			if strings.Contains(got, unwanted) {
				t.Errorf("help for %q contains invalid placeholder %q", tt.args, unwanted)
			}
		}
	}
}
