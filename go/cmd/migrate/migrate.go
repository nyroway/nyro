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
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"gorm.io/gorm"

	infradatabase "github.com/nyroway/nyro/go/infra/database"
	dbsqlite "github.com/nyroway/nyro/go/infra/database/sqlite"
	"github.com/nyroway/nyro/go/internal/schemadump"
	"github.com/nyroway/nyro/go/internal/storage/database"
)

type openedGorm struct {
	DB         *gorm.DB
	connection *infradatabase.Connection
}

func (o *openedGorm) Close() error {
	if o == nil {
		return nil
	}
	return o.connection.Close()
}

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
		opened, err := openGorm(cmd.Context(), orSQLiteMem(*dsn))
		if err != nil {
			return err
		}
		defer func() { _ = opened.Close() }()
		sql, err := schemadump.Dump(opened.DB)
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
		shadow, err := openGorm(cmd.Context(), orSQLiteMem(*shadowDSN))
		if err != nil {
			return fmt.Errorf("open shadow: %w", err)
		}
		defer func() { _ = shadow.Close() }()
		current, err := currentSchema(cmd.Context(), *targetFile, *targetDSN)
		if err != nil {
			return err
		}
		sql, err := schemadump.Diff(shadow.DB, current)
		if err != nil {
			return err
		}
		return writeOut(cmd.OutOrStdout(), *output, sql)
	}
	return cmd
}

// currentSchema resolves the diff's "current state" from exactly one of a
// schema file or a live target DB (introspected).
func currentSchema(ctx context.Context, targetFile, targetDSN string) (string, error) {
	if targetFile != "" {
		b, err := os.ReadFile(targetFile)
		if err != nil {
			return "", fmt.Errorf("read --target-file: %w", err)
		}
		return string(b), nil
	}
	target, err := openGorm(ctx, targetDSN)
	if err != nil {
		return "", fmt.Errorf("open target: %w", err)
	}
	defer func() { _ = target.Close() }()
	sql, err := schemadump.IntrospectSchema(target.DB)
	if err != nil {
		return "", fmt.Errorf("introspect --target-dsn: %w", err)
	}
	return sql, nil
}

// openGorm opens a caller-owned SQL connection and wraps it with the Config
// Engine's GORM backend.
func openGorm(ctx context.Context, dsn string) (*openedGorm, error) {
	connection, err := infradatabase.Open(ctx, dsn, infradatabase.Options{
		SQLite: dbsqlite.Options{
			BusyTimeout:  5 * time.Second,
			MaxOpenConns: 5,
			MaxIdleConns: 2,
		},
	})
	if err != nil {
		return nil, err
	}
	b, err := database.New(connection.Kind, connection.DB)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	return &openedGorm{DB: b.DB(), connection: connection}, nil
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
