// Package web embeds the admin web, so the server is a single binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed admin
var files embed.FS

// Admin returns the admin web's files, with index.html at the root.
func Admin() fs.FS {
	sub, err := fs.Sub(files, "admin")
	if err != nil {
		panic(err) // the directory is embedded at build time
	}
	return sub
}
