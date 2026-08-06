// Package migrate implements the `nyro tool migrate` subcommand group: render and
// diff the canonical GORM schema (internal/storage/model) into plain SQL, for
// operators who apply schema changes by hand (no runtime DDL rights). It only
// depends on GORM — see internal/schemadump.
//
//   - `nyro tool migrate dump`: full CREATE DDL for a fresh database.
//   - `nyro tool migrate diff`: incremental DDL to bring an existing schema up to
//     the models.
//
// Automatic migration (GORM AutoMigrate) is the other path — `nyro serve
// --auto-migrate`; these subcommands are for the manual path.
package migrate

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"gorm.io/gorm"

	"github.com/nyroway/nyro/go/internal/bootstrap"
	"github.com/nyroway/nyro/go/internal/schemadump"
	"github.com/nyroway/nyro/go/internal/storage/database"
)

// NewCmd builds the `nyro tool migrate` command group.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Manage database schemas",
	}
	cmd.AddCommand(newDumpCmd())
	cmd.AddCommand(newDiffCmd())
	return cmd
}

func newDumpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dump",
		Short: "Print the complete database schema",
	}
	dsn := cmd.Flags().String("dsn", "", "Database DSN used to select SQL dialect")
	output := cmd.Flags().String("output", "", "Write SQL to a file")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		db, err := openGorm(orSQLiteMem(*dsn))
		if err != nil {
			return err
		}
		sql, err := schemadump.Dump(db)
		if err != nil {
			return err
		}
		return writeOut(cmd.OutOrStdout(), *output, sql)
	}
	return cmd
}

func newDiffCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Print database schema changes",
	}
	shadowDSN := cmd.Flags().String("shadow-dsn", "", "Writable scratch database DSN")
	targetDSN := cmd.Flags().String("target-dsn", "", "Database DSN to inspect")
	targetFile := cmd.Flags().String("target-file", "", "Schema SQL file to compare")
	output := cmd.Flags().String("output", "", "Write SQL to a file")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if (*targetDSN == "") == (*targetFile == "") {
			return fmt.Errorf("exactly one of --target-file or --target-dsn is required")
		}
		if *targetDSN != "" && *targetDSN == *shadowDSN {
			return fmt.Errorf("--shadow-dsn must differ from --target-dsn (the shadow is written to)")
		}
		shadow, err := openGorm(orSQLiteMem(*shadowDSN))
		if err != nil {
			return fmt.Errorf("open shadow: %w", err)
		}
		current, err := currentSchema(*targetFile, *targetDSN)
		if err != nil {
			return err
		}
		sql, err := schemadump.Diff(shadow, current)
		if err != nil {
			return err
		}
		return writeOut(cmd.OutOrStdout(), *output, sql)
	}
	return cmd
}

// currentSchema resolves the diff's "current state" from exactly one of a
// schema file or a live target DB (introspected).
func currentSchema(targetFile, targetDSN string) (string, error) {
	if targetFile != "" {
		b, err := os.ReadFile(targetFile)
		if err != nil {
			return "", fmt.Errorf("read --target-file: %w", err)
		}
		return string(b), nil
	}
	target, err := openGorm(targetDSN)
	if err != nil {
		return "", fmt.Errorf("open target: %w", err)
	}
	sql, err := schemadump.IntrospectSchema(target)
	if err != nil {
		return "", fmt.Errorf("introspect --target-dsn: %w", err)
	}
	return sql, nil
}

// openGorm opens a gorm.DB for dsn, reusing the storage backend constructors.
func openGorm(dsn string) (*gorm.DB, error) {
	backend, driverDSN, err := bootstrap.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	var b *database.Backend
	switch backend {
	case "sqlite":
		b, err = database.NewSQLite(driverDSN)
	case "postgres":
		b, err = database.NewPostgres(driverDSN)
	default:
		return nil, fmt.Errorf("unknown backend %q", backend)
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", backend, err)
	}
	return b.DB(), nil
}

func orSQLiteMem(dsn string) string {
	if dsn == "" {
		return "sqlite://:memory:"
	}
	return dsn
}

func writeOut(stdout io.Writer, outputFile, sql string) error {
	if outputFile != "" {
		return os.WriteFile(outputFile, []byte(sql+"\n"), 0o644)
	}
	_, err := fmt.Fprintln(stdout, sql)
	return err
}
