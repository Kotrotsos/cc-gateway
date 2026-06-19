// Package web embeds the built React UI (web/dist) into the binary. During
// development the dist directory holds a placeholder; `pnpm --dir web build`
// replaces it with the real Vite output, which `go build` then embeds.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the embedded UI rooted at the dist directory.
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return sub
}
