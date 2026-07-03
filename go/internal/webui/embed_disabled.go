//go:build !webui_embed

package webui

import "io/fs"

func embeddedFS() (fs.FS, bool) {
	return nil, false
}
