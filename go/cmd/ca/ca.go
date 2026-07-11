// Package ca implements the `nyro ca` subcommand group: an offline
// certificate authority for the config-sync mTLS channel between admin
// (control plane) and gateway (data plane). It never runs online — it's a
// one-shot CLI for generating a CA and signing leaf certificates that get
// distributed to admin/gateway hosts and loaded via their
// --config-tls-ca/-cert/-key flags.
package ca

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/nyroway/nyro/go/internal/configsync/pki"
)

// defaultDir is ~/.nyro/pki, matching the admin control plane's ~/.nyro home
// convention (see cmd/admin's nyroHomeDir). Falls back to ./.nyro/pki if the
// OS user home directory can't be resolved.
func defaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".nyro", "pki")
	}
	return filepath.Join(home, ".nyro", "pki")
}

const (
	defaultCAValid   = 87600 * time.Hour // 10y
	defaultLeafValid = 8760 * time.Hour  // 1y
)

// NewCmd builds the `nyro ca` command group.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ca",
		Short: "Offline certificate authority for config-sync mTLS",
	}
	cmd.AddCommand(newInitCmd())
	cmd.AddCommand(newSignAdminCmd())
	cmd.AddCommand(newSignGatewayCmd())
	return cmd
}

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create (or reuse) the config-sync CA",
	}
	dir := cmd.Flags().String("dir", defaultDir(), "directory to write ca.pem/ca-key.pem into")
	valid := cmd.Flags().Duration("valid", defaultCAValid, "CA certificate validity period")
	force := cmd.Flags().Bool("force", false, "regenerate the CA even if one already exists (invalidates all certs it previously signed)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if *force {
			for _, f := range []string{"ca.pem", "ca-key.pem"} {
				p := filepath.Join(*dir, f)
				if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("--force: remove existing %s: %w", p, err)
				}
			}
		}
		existed := caExists(*dir)
		if _, err := pki.EnsureCA(*dir, *valid); err != nil {
			return err
		}
		if existed {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "reused existing CA in %s\n", *dir)
		} else {
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "generated new CA in %s (valid %s)\n", *dir, *valid)
		}
		return nil
	}
	return cmd
}

func newSignAdminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sign-admin",
		Short: "Sign admin's config-sync server certificate",
	}
	dir := cmd.Flags().String("dir", defaultDir(), "directory containing ca.pem/ca-key.pem (from `nyro ca init`)")
	valid := cmd.Flags().Duration("valid", defaultLeafValid, "leaf certificate validity period")
	out := cmd.Flags().String("out", "admin", "output file basename (writes <dir>/<out>.pem and <dir>/<out>-key.pem)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		certPath, keyPath, err := signWithCA(*dir, func(c *pki.CA) (string, string, error) {
			return c.SignServer(*dir, *out, *valid)
		})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s and %s (identity: spiffe://nyro/%s)\n", certPath, keyPath, pki.AdminSPIFFEID)
		return nil
	}
	return cmd
}

func newSignGatewayCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sign-gateway",
		Short: "Sign a gateway node's config-sync client certificate",
	}
	dir := cmd.Flags().String("dir", defaultDir(), "directory containing ca.pem/ca-key.pem (from `nyro ca init`)")
	nodeID := cmd.Flags().String("node-id", "", "node identity for the SPIFFE SAN (spiffe://nyro/gateway/<node-id>); random if unset")
	valid := cmd.Flags().Duration("valid", defaultLeafValid, "leaf certificate validity period")
	out := cmd.Flags().String("out", "gateway", "output file basename (writes <dir>/<out>.pem and <dir>/<out>-key.pem)")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		id := *nodeID
		if id == "" {
			var err error
			id, err = randomNodeID()
			if err != nil {
				return err
			}
		}
		certPath, keyPath, err := signWithCA(*dir, func(c *pki.CA) (string, string, error) {
			return c.SignClient(*dir, *out, id, *valid)
		})
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s and %s (node-id: %s)\n", certPath, keyPath, id)
		return nil
	}
	return cmd
}

// signWithCA loads the CA at dir (must already exist — created via `nyro ca
// init`) and runs sign against it.
func signWithCA(dir string, sign func(*pki.CA) (certPath, keyPath string, err error)) (certPath, keyPath string, err error) {
	if !caExists(dir) {
		return "", "", fmt.Errorf("no CA found in %s — run `nyro ca init --dir %s` first", dir, dir)
	}
	c, err := pki.LoadCA(dir)
	if err != nil {
		return "", "", err
	}
	return sign(c)
}

func caExists(dir string) bool {
	_, certErr := os.Stat(filepath.Join(dir, "ca.pem"))
	_, keyErr := os.Stat(filepath.Join(dir, "ca-key.pem"))
	return certErr == nil && keyErr == nil
}

func randomNodeID() (string, error) {
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generate random node-id: %w", err)
	}
	return hex.EncodeToString(suffix), nil
}
