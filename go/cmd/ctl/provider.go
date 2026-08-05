package ctl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"
)

type providerRow struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Provider  string `json:"provider"`
	Enabled   bool   `json:"enabled"`
	UpdatedAt string `json:"updated_at"`
}

// ProviderCmd returns the nyro provider subcommand group.
// prov is registered as a short alias; nyro prov ls is equivalent to nyro provider ls.
func ProviderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "provider",
		Aliases: []string{"prov"},
		Short:   "manage provider resources",
	}
	cmd.AddCommand(providerLsCmd())
	// Future: providerShowCmd(), providerCreateCmd(), providerRmCmd()
	return cmd
}

// providerLsCmd implements nyro provider ls.
func providerLsCmd() *cobra.Command {
	var flagServer, flagToken string
	cmd := &cobra.Command{
		Use:           "ls",
		Short:         "list providers",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := ResolveClientConfig(flagServer, flagToken)
			if err != nil {
				return err
			}
			body, err := DoRequest(context.Background(), "GET", cfg, "/api/v1/upstreams")
			if err != nil {
				return err
			}
			var providers []providerRow
			if err := json.Unmarshal(body, &providers); err != nil {
				return fmt.Errorf("decode providers response: %w", err)
			}
			type row struct{ name, id, provider, enabled, updated string }
			rows := make([]row, 0, len(providers)+1)
			rows = append(rows, row{"NAME", "ID", "PROVIDER", "ENABLED", "UPDATED"})
			for _, p := range providers {
				rows = append(rows, row{
					p.Name,
					truncate(p.ID, 12),
					p.Provider,
					fmt.Sprintf("%v", p.Enabled),
					humanizeTime(p.UpdatedAt),
				})
			}
			widths := [5]int{}
			for _, r := range rows {
				cols := [5]string{r.name, r.id, r.provider, r.enabled, r.updated}
				for i, c := range cols {
					if w := runewidth.StringWidth(c); w > widths[i] {
						widths[i] = w
					}
				}
			}
			const pad = 3
			for _, r := range rows {
				cols := [5]string{r.name, r.id, r.provider, r.enabled, r.updated}
				for j, c := range cols {
					if j == len(cols)-1 {
						_, _ = fmt.Fprint(os.Stdout, c)
					} else {
						w := runewidth.StringWidth(c)
						_, _ = fmt.Fprint(os.Stdout, c+strings.Repeat(" ", widths[j]-w+pad))
					}
				}
				_, _ = fmt.Fprintln(os.Stdout)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&flagServer, "server", "", "admin API address (overrides auto-discovery)")
	cmd.Flags().StringVar(&flagToken, "token", "", "bearer token")
	return cmd
}

// truncate shortens s to at most n characters.
func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n]
}

// humanizeTime converts an RFC3339 timestamp into a relative English phrase.
func humanizeTime(s string) string {
	if s == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// Tolerate common variants without timezone designator.
		if t2, err2 := time.Parse("2006-01-02T15:04:05", s); err2 == nil {
			t = t2
		} else if t2, err2 := time.Parse("2006-01-02 15:04:05", s); err2 == nil {
			t = t2
		} else {
			return s
		}
	}
	d := time.Since(t)
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		n := int(d.Minutes())
		if n == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", n)
	case d < 24*time.Hour:
		n := int(d.Hours())
		if n == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", n)
	case d < 30*24*time.Hour:
		n := int(d.Hours() / 24)
		if n == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", n)
	default:
		n := int(d.Hours() / 24 / 30)
		if n < 1 {
			n = 1
		}
		if n == 1 {
			return "1 month ago"
		}
		return fmt.Sprintf("%d months ago", n)
	}
}
