// Package envflag adds an environment-variable layer to the nyro cobra CLI,
// giving every subcommand flag the industry-standard precedence
//
//	explicit flag  >  environment variable  >  built-in default
//
// It is wired once on the root command (see cmd wiring in nyro.go): Bind is
// installed as the root's PersistentPreRunE so it runs for whichever leaf
// command is executed, and Decorate rewrites each flag's help text to advertise
// its env var. No subcommand code changes — a flag added to any current or
// future subcommand gets env support automatically.
//
// The env var name for a flag is derived from the command path and flag name:
//
//	nyro admin       --listen               -> NYRO_ADMIN_LISTEN
//	nyro gateway     --config-file          -> NYRO_GATEWAY_CONFIG_FILE
//	nyro ca init     --dir                  -> NYRO_CA_INIT_DIR
//	nyro ca sign-admin --out                -> NYRO_CA_SIGN_ADMIN_OUT
package envflag

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// envPrefix is prepended to every generated env var name; it matches the root
// command name ("nyro"). Kept separate from the per-command prefix so EnvKey
// produces NYRO_<PREFIX>_<FLAG> rather than a doubled root segment.
const envPrefix = "NYRO"

// EnvKey builds the environment variable name for a flag under the given
// per-command prefix (e.g. prefix "ADMIN", flag "config-poll-interval" ->
// "NYRO_ADMIN_CONFIG_POLL_INTERVAL"). The flag name's dashes become
// underscores and everything is upper-cased.
func EnvKey(prefix, flagName string) string {
	return envPrefix + "_" + prefix + "_" + normalize(flagName)
}

// normalize upper-cases a path/flag segment and turns dashes and spaces into
// underscores, matching the shell convention for env var names.
func normalize(s string) string {
	s = strings.ReplaceAll(s, "-", "_")
	s = strings.ReplaceAll(s, " ", "_")
	return strings.ToUpper(s)
}

// prefixFromCommand derives the per-command env prefix from a command's full
// path by dropping the leading root segment ("nyro") and joining the rest with
// underscores, upper-cased. "nyro ca sign-admin" -> "CA_SIGN_ADMIN". Returns ""
// for the bare root command (which carries no flags of its own).
func prefixFromCommand(cmd *cobra.Command) string {
	path := strings.Fields(cmd.CommandPath())
	if len(path) <= 1 {
		return ""
	}
	return normalize(strings.Join(path[1:], "_"))
}

// Bind is the root PersistentPreRunE. For the executed command it applies, for
// each flag the user did NOT set explicitly on the command line, the value of
// the corresponding env var when that var is present. This yields the
// precedence explicit flag > env > default: an explicitly-set flag is skipped
// (its Changed is already true), an env-supplied value is applied via Set
// (which marks the flag Changed too, so downstream Changed()-based validation
// treats an env value like an explicit one), and a flag left untouched keeps
// its pflag default.
func Bind(cmd *cobra.Command, _ []string) error {
	prefix := prefixFromCommand(cmd)
	if prefix == "" {
		return nil
	}
	var bindErr error
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if bindErr != nil || f.Changed {
			return
		}
		envName := EnvKey(prefix, f.Name)
		val, ok := os.LookupEnv(envName)
		if !ok {
			return
		}
		if err := cmd.Flags().Set(f.Name, val); err != nil {
			bindErr = fmt.Errorf("invalid value for %s: %w", envName, err)
		}
	})
	return bindErr
}

// Decorate walks the command tree rooted at root and appends an "(env NYRO_...)"
// hint to every flag's usage string so `--help` advertises the env var. It must
// be called after the full command tree is assembled (all AddCommand calls
// done), since it relies on CommandPath being complete.
func Decorate(root *cobra.Command) {
	for _, cmd := range root.Commands() {
		Decorate(cmd)
	}
	prefix := prefixFromCommand(root)
	if prefix == "" {
		return
	}
	root.Flags().VisitAll(func(f *pflag.Flag) {
		f.Usage = fmt.Sprintf("%s (env %s)", f.Usage, EnvKey(prefix, f.Name))
	})
}
