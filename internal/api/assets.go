package api

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed web
var webFS embed.FS

var indexHTML http.Handler

func init() {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	indexHTML = http.FileServer(http.FS(sub))
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/")
	if p == "" || p == "index.html" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		indexHTML.ServeHTTP(w, r)
		return
	}
	switch {
	case strings.HasPrefix(p, "static/"):
		// Serve hashed-free assets from the embedded tree.
		w.Header().Set("Cache-Control", "no-cache")
		http.StripPrefix("/static/", indexHTML).ServeHTTP(w, r)
	case p == "app.css" || p == "app.js":
		w.Header().Set("Cache-Control", "no-cache")
		indexHTML.ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}
