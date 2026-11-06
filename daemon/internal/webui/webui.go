// Package webui serves the web application embedded into the daemon binary.
// The Makefile copies the built webapp into dist/ before compiling; a plain
// `go build` without that step still compiles and responds with a hint.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler serves the embedded UI with an index.html fallback for
// client-side routes.
func Handler() http.Handler {
	assets, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return handlerFor(assets)
}

// handlerFor implements Handler against any filesystem, so tests can supply
// their own instead of the embedded one.
func handlerFor(assets fs.FS) http.Handler {
	fileServer := http.FileServerFS(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := fs.Stat(assets, "index.html"); err != nil {
			http.Error(w, "web UI not embedded in this build — run `make build`", http.StatusNotFound)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" {
			if _, err := fs.Stat(assets, name); err != nil {
				http.ServeFileFS(w, r, assets, "index.html")
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}
