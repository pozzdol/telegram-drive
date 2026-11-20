//go:build embedui

// Package webui holds the built web UI (web/dist, copied to webui/dist) inside the binary, so one
// process serves both the UI and the API. Release builds use -tags embedui; see .github/workflows.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var files embed.FS

// FS is the built UI, or nil in builds without it.
func FS() fs.FS {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		return nil
	}
	return sub
}
