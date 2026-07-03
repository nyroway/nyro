//go:build webui_embed

package webui

import (
	"embed"
	"io/fs"
)

//go:embed dist/*
var embeddedDist embed.FS

func embeddedFS() (fs.FS, bool) {
	sub, err := fs.Sub(embeddedDist, "dist")
	if err != nil {
		return nil, false
	}
	return sub, true
}
