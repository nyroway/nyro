// Command gen generates typed GORM query code for the SQL storage backend.
package main

import (
	"gorm.io/gen"

	"github.com/nyroway/nyro/go/internal/storage/model"
)

func main() {
	g := gen.NewGenerator(gen.Config{
		OutPath:      "./internal/storage/query",
		ModelPkgPath: "github.com/nyroway/nyro/go/internal/storage/model",
		Mode:         gen.WithDefaultQuery | gen.WithQueryInterface,
	})
	g.ApplyBasic(model.All()...)
	g.Execute()
}
