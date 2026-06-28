// Package web embeds the controller's single-page app and serves it with the
// standard SPA-in-Go pattern: serve a real file if it exists, otherwise fall
// back to index.html so client-side routing works. The built assets are written
// to dist/ by `npm run build:embed` and compiled into the binary.
package web

import (
	"embed"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// DistFS returns the embedded dist/ subtree (the built SPA).
func DistFS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return distFS
	}
	return sub
}

// SPAHandler serves static assets from fsys and falls back to index.html for any
// path that isn't a real file, so a deep link like /fleet loads the app shell
// and the client router takes over.
func SPAHandler(fsys fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		if f, err := fsys.Open(p); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}
		serveIndex(w, fsys)
	})
}

func serveIndex(w http.ResponseWriter, fsys fs.FS) {
	f, err := fsys.Open("index.html")
	if err != nil {
		http.Error(w, "frontend not built — run `npm run build:embed`", http.StatusNotFound)
		return
	}
	defer f.Close()
	data, _ := io.ReadAll(f)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}
