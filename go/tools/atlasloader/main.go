// Command atlasloader prints the SQL DDL for the canonical GORM models
// (go/internal/storage/model) in a given dialect. It exists only as the
// `program` invoked by atlas.hcl's external_schema data sources — `atlas
// migrate diff` shells out to it to compute the desired-state schema, then
// diffs that against the migration directory. Not part of any production
// binary.
package main

import (
	"fmt"
	"io"
	"os"

	"ariga.io/atlas-provider-gorm/gormschema"

	"github.com/nyroway/nyro/go/internal/storage/model"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: atlasloader <mysql|postgres>")
		os.Exit(1)
	}
	stmts, err := gormschema.New(os.Args[1]).Load(model.All()...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "atlasloader: load gorm schema: %v\n", err)
		os.Exit(1)
	}
	if _, err := io.WriteString(os.Stdout, stmts); err != nil {
		fmt.Fprintf(os.Stderr, "atlasloader: write stdout: %v\n", err)
		os.Exit(1)
	}
}
