// Package web embeds the built dashboard so the server is a single self-contained
// binary. Run `npm run build` in this directory to regenerate dist/.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the dashboard's built assets rooted at dist/.
func FS() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
