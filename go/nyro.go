// Command nyro is the unified gateway CLI: `nyro gateway` (data plane),
// `nyro admin` (control plane), `nyro tool` (utilities).
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nyroway/nyro/go/cmd/admin"
	"github.com/nyroway/nyro/go/cmd/gateway"
	"github.com/nyroway/nyro/go/cmd/tool"
)

func main() {
	root := &cobra.Command{
		Use:   "nyro",
		Short: "Nyro gateway",
	}
	root.PersistentFlags().String("storage", "memory", "storage backend: memory|sqlite|postgres|mysql")
	root.PersistentFlags().String("db-dsn", "", "database path/DSN for persistent backends")
	root.AddCommand(gateway.NewCmd())
	root.AddCommand(admin.NewCmd())
	root.AddCommand(tool.NewCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
