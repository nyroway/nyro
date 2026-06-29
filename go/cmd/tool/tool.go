// Package tool implements the `nyro tool` subcommand of the unified nyro CLI.
// Hosts operational subcommands; the parity record/replay + diff harness lands
// here in a later iteration.
package tool

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Version is the gateway version reported by `nyro tool version`.
const Version = "0.1.0-dev"

// NewCmd builds the tool subcommand.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tool",
		Short: "Operational utilities (parity harness, diagnostics)",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the gateway version",
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Println(Version)
			return nil
		},
	})
	return cmd
}
