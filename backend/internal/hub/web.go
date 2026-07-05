package hub

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web
var webFS embed.FS

// WebHandler serves the traego.ai landing pages embedded in the binary.
// Clean paths map to their .html files (/install -> install.html); unknown
// paths fall back to the landing page.
func WebHandler() http.Handler {
	sub, _ := fs.Sub(webFS, "web")
	files := http.FileServerFS(sub)
	serveHTML := func(w http.ResponseWriter, r *http.Request, name string) {
		// ServeFileFS (not a path rewrite) — FileServer would 301 index.html
		// back to "/" and loop.
		http.ServeFileFS(w, r, sub, name)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimSuffix(r.URL.Path, "/")
		switch {
		case p == "" || p == "/index.html":
			serveHTML(w, r, "index.html")
		case p == "/install" || p == "/buy":
			serveHTML(w, r, strings.TrimPrefix(p, "/")+".html")
		default:
			if _, err := fs.Stat(sub, strings.TrimPrefix(r.URL.Path, "/")); err != nil {
				serveHTML(w, r, "index.html")
				return
			}
			if strings.HasSuffix(r.URL.Path, ".woff2") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
		}
	})
}
