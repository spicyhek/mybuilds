// Package web embeds the built frontend into the server binary.
//
// dist/ currently holds a placeholder page that lists releases from the API.
// The React pipeline UI replaces it; demo/index.html at the repository root
// is the standalone prototype it is built from.
package web

import (
	"embed"
	"io/fs"
)

//go:embed dist
var dist embed.FS

// Dist returns the frontend files rooted at dist/.
func Dist() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
