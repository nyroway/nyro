// Command nyro is the unified gateway CLI: `nyro gateway` (data plane),
// `nyro admin` (control plane).
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nyroway/nyro/go/cmd/admin"
	"github.com/nyroway/nyro/go/cmd/ca"
	"github.com/nyroway/nyro/go/cmd/gateway"
	"github.com/nyroway/nyro/go/internal/version"
)

// newRootCmd builds the root cobra command. Extracted from main so tests can
// inspect its subcommand/flag shape without calling Execute or os.Exit.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "nyro",
		Short: "Nyro gateway",
	}
	// nyro is not meant to be introspected via shell-completion scripts today;
	// disable cobra's auto-added `completion` subcommand rather than ship an
	// unmaintained surface.
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(gateway.NewCmd())
	root.AddCommand(admin.NewCmd())
	root.AddCommand(ca.NewCmd())
	root.AddCommand(newVersionCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the nyro version",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "nyro version %s\n", version.Version)
			return err
		},
	}
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
