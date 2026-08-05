package ctl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type statusResp struct {
	Status         string `json:"status"`
	Version        string `json:"version"`
	UpstreamCount  int    `json:"upstream_count"`
	RouteCount     int    `json:"route_count"`
	ConsumerCount  int    `json:"consumer_count"`
	Backend        string `json:"backend"`
	Writable       bool   `json:"writable"`
}

// StatusCmd returns the nyro status subcommand.
func StatusCmd() *cobra.Command {
	var flagServer, flagToken string
	cmd := &cobra.Command{
		Use:           "status",
		Short:         "show nyro server status",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := ResolveClientConfig(flagServer, flagToken)
			if err != nil {
				return err
			}
			body, err := DoRequest(context.Background(), "GET", cfg, "/api/v1/status")
			if err != nil {
				return err
			}
			var resp statusResp
			if err := json.Unmarshal(body, &resp); err != nil {
				return fmt.Errorf("decode status response: %w", err)
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 4, ' ', 0)
			fmt.Fprintf(w, "Host\t%s\n", cfg.ServerAddr)
			fmt.Fprintf(w, "Status\t%s\n", resp.Status)
			fmt.Fprintf(w, "Version\t%s\n", resp.Version)
			fmt.Fprintf(w, "Providers\t%d\n", resp.UpstreamCount)
			fmt.Fprintf(w, "Models\t%d\n", resp.RouteCount)
			fmt.Fprintf(w, "Consumers\t%d\n", resp.ConsumerCount)
			fmt.Fprintf(w, "Backend\t%s\n", resp.Backend)
			fmt.Fprintf(w, "Writable\t%v\n", resp.Writable)
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&flagServer, "server", "", "admin API address (overrides auto-discovery)")
	cmd.Flags().StringVar(&flagToken, "token", "", "bearer token")
	return cmd
}
