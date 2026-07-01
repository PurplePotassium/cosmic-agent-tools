// Package web embeds the built React SPA (web/dist) so the whole app ships as one
// binary — the user installs nothing to get the UI. `npm run build` writes web/dist;
// `go build` bakes it in. The reference silhouette is Syncthing: Go backend + embedded
// web UI + single binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// Dist returns the SPA build rooted at its top (index.html at "/"), or nil if the
// build directory is effectively empty (only the placeholder), so the server can fall
// back to its built-in placeholder page.
func Dist() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil
	}
	if _, err := fs.Stat(sub, "index.html"); err != nil {
		return nil
	}
	return sub
}
