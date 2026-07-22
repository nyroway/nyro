package envflag

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// newTestCmd builds a leaf command "nyro server" carrying string/bool/duration
// flags, so Bind sees a realistic CommandPath ("nyro server") and prefix
// ("SERVER"). The returned command is not executed; tests call Bind directly.
func newTestCmd() *cobra.Command {
	root := &cobra.Command{Use: "nyro"}
	server := &cobra.Command{Use: "server", RunE: func(*cobra.Command, []string) error { return nil }}
	server.Flags().String("listen", "127.0.0.1:19531", "listen addr")
	server.Flags().Bool("auto-migrate", false, "auto migrate")
	server.Flags().Duration("config-poll-interval", 0, "poll interval")
	root.AddCommand(server)
	return server
}

func TestBindAppliesEnvWhenFlagUnset(t *testing.T) {
	cmd := newTestCmd()
	t.Setenv("NYRO_SERVER_LISTEN", "0.0.0.0:29530")

	if err := Bind(cmd, nil); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	got, _ := cmd.Flags().GetString("listen")
	if got != "0.0.0.0:29530" {
		t.Errorf("listen = %q, want env value 0.0.0.0:29530", got)
	}
	if !cmd.Flags().Changed("listen") {
		t.Error("env-applied flag should report Changed()==true")
	}
}

func TestExplicitFlagBeatsEnv(t *testing.T) {
	cmd := newTestCmd()
	// Simulate the user passing --listen explicitly on the command line.
	if err := cmd.Flags().Set("listen", "1.2.3.4:1111"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NYRO_SERVER_LISTEN", "0.0.0.0:29530")

	if err := Bind(cmd, nil); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	got, _ := cmd.Flags().GetString("listen")
	if got != "1.2.3.4:1111" {
		t.Errorf("listen = %q, want explicit flag value 1.2.3.4:1111 (flag must beat env)", got)
	}
}

func TestDefaultWhenNoEnvNoFlag(t *testing.T) {
	cmd := newTestCmd()
	if err := Bind(cmd, nil); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	got, _ := cmd.Flags().GetString("listen")
	if got != "127.0.0.1:19531" {
		t.Errorf("listen = %q, want default 127.0.0.1:19531", got)
	}
	if cmd.Flags().Changed("listen") {
		t.Error("untouched flag should not be Changed()")
	}
}

func TestBindTypedFlagsFromEnv(t *testing.T) {
	cmd := newTestCmd()
	t.Setenv("NYRO_SERVER_AUTO_MIGRATE", "true")
	t.Setenv("NYRO_SERVER_CONFIG_POLL_INTERVAL", "5s")

	if err := Bind(cmd, nil); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if b, _ := cmd.Flags().GetBool("auto-migrate"); !b {
		t.Error("auto-migrate should be true from env")
	}
	if d, _ := cmd.Flags().GetDuration("config-poll-interval"); d != 5*time.Second {
		t.Errorf("config-poll-interval = %v, want 5s from env", d)
	}
}

func TestBindInvalidEnvValueErrors(t *testing.T) {
	cmd := newTestCmd()
	t.Setenv("NYRO_SERVER_CONFIG_POLL_INTERVAL", "not-a-duration")

	err := Bind(cmd, nil)
	if err == nil {
		t.Fatal("expected error for invalid duration env value, got nil")
	}
}

func TestEnvKey(t *testing.T) {
	cases := []struct{ prefix, flag, want string }{
		{"SERVER", "listen", "NYRO_SERVER_LISTEN"},
		{"SERVER", "obs-data-dir", "NYRO_SERVER_OBS_DATA_DIR"},
		{"PROXY", "sync-tls-ca", "NYRO_PROXY_SYNC_TLS_CA"},
	}
	for _, c := range cases {
		if got := EnvKey(c.prefix, c.flag); got != c.want {
			t.Errorf("EnvKey(%q,%q) = %q, want %q", c.prefix, c.flag, got, c.want)
		}
	}
}

func TestPrefixFromCommand(t *testing.T) {
	root := &cobra.Command{Use: "nyro"}
	ca := &cobra.Command{Use: "ca"}
	signServer := &cobra.Command{Use: "sign-server"}
	ca.AddCommand(signServer)
	root.AddCommand(ca)

	if got := prefixFromCommand(signServer); got != "CA_SIGN_SERVER" {
		t.Errorf("prefixFromCommand(nyro ca sign-server) = %q, want CA_SIGN_SERVER", got)
	}
	if got := prefixFromCommand(root); got != "" {
		t.Errorf("prefixFromCommand(root) = %q, want empty", got)
	}
}

func TestDecorateAppendsEnvHint(t *testing.T) {
	cmd := newTestCmd()
	root := cmd.Parent()
	Decorate(root)

	usage := cmd.Flags().Lookup("listen").Usage
	if want := "(env NYRO_SERVER_LISTEN)"; !strings.Contains(usage, want) {
		t.Errorf("listen usage = %q, want it to contain %q", usage, want)
	}
}
