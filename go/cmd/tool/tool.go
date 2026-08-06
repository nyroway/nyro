// Package tool implements the `nyro tool` command group for operational tools.
package tool

import (
	"github.com/spf13/cobra"

	"github.com/nyroway/nyro/go/cmd/ca"
	"github.com/nyroway/nyro/go/cmd/migrate"
)

// NewCmd builds the `nyro tool` command group.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tool",
		Short: "Run operational tools",
	}
	cmd.AddCommand(ca.NewCmd())
	cmd.AddCommand(migrate.NewCmd())
	return cmd
}
