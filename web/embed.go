// Package web holds the React frontend and embeds its build output into the
// server binary.
//
// dist/ is produced by `npm run build` in this directory and is not checked
// in. Without a build the directory holds only .gitkeep (the all: prefix
// embeds dotfiles so the embed still compiles), and the server answers
// every page with 404.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the frontend files rooted at dist/.
func Dist() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
