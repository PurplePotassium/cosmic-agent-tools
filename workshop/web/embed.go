// Package web embeds the dashboard. The UI is deliberately buildless:
// vendored Preact + HTM as native ES modules wired by an import map — no
// Node, no bundler, nothing to install; `go build` is the whole build. If
// the UI ever outgrows this, swapping in a Vite build only changes what
// lands in ui/.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:ui
var uiFS embed.FS

// Dist returns the UI file tree, or nil when no real UI is embedded.
func Dist() fs.ReadFileFS {
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		return nil
	}
	if _, err := sub.(fs.ReadFileFS).ReadFile("index.html"); err != nil {
		return nil
	}
	return sub.(fs.ReadFileFS)
}
