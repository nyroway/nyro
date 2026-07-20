// Package migrate implements the `nyro migrate` subcommand group: render and
// diff the canonical GORM schema (internal/storage/model) into plain SQL, for
// operators who apply schema changes by hand (no runtime DDL rights). It only
// depends on GORM — see internal/schemadump.
//
//   - `nyro migrate dump`: full CREATE DDL for a fresh database.
//   - `nyro migrate diff`: incremental DDL to bring an existing schema up to
//     the models.
//
// Automatic migration (GORM AutoMigrate) is the other path — `nyro
// admin`/`gateway --auto-migrate`; these subcommands are for the manual path.
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

// NewCmd builds the `nyro migrate` command group.
func NewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Render and diff the mysql/postgres schema DDL (GORM-only)",
	}
	cmd.AddCommand(newDumpCmd())
	cmd.AddCommand(newDiffCmd())
	return cmd
}

func newDumpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dump",
		Short: "Print the full CREATE DDL for the models (fresh-database schema)",
		Long: "Renders model.All() to CREATE DDL in --dsn's dialect via a DryRun " +
			"session — nothing is executed, so a read-only account is fine. --dsn " +
			"only selects the dialect (mysql/postgres); omit it for sqlite (in-memory).",
	}
	dsn := cmd.Flags().String("dsn", "", "a reachable DB of the target dialect (read-only ok) whose dialect selects the DDL; sqlite in-memory if omitted")
	output := cmd.Flags().String("output", "", "write SQL to this file instead of stdout")
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
		Short: "Print the incremental DDL to bring a current schema up to the models",
		Long: "Loads the current schema (from --target-file or --target-dsn) onto the " +
			"writable --shadow-dsn, runs AutoMigrate for real, and prints the DDL it " +
			"would issue. --target-file (an exact schema dump, e.g. from `dump`) is " +
			"precise; --target-dsn introspects a live DB and is lossy (may re-suggest " +
			"indexes/constraints). The target is only read.",
	}
	shadowDSN := cmd.Flags().String("shadow-dsn", "", "writable scratch DB with DDL rights (must differ from --target-dsn); sqlite in-memory if omitted")
	targetDSN := cmd.Flags().String("target-dsn", "", "current-schema source: a read-only DB to introspect (lossy)")
	targetFile := cmd.Flags().String("target-file", "", "current-schema source: a schema .sql file (precise, recommended)")
	output := cmd.Flags().String("output", "", "write SQL to this file instead of stdout")
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
	case "mysql":
		b, err = database.NewMySQL(driverDSN)
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
