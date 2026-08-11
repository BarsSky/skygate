package handlers

import (
	"net/http"
	"path/filepath"
	"strings"
)

// StaticHandler serves files from ./static directory.
// Mounted in main.go: mux.HandleFunc("/static/", app.StaticHandler)
func (a *App) StaticHandler(w http.ResponseWriter, r *http.Request) {
	// Strip "/static/" prefix
	p := strings.TrimPrefix(r.URL.Path, "/static/")
	if p == "" || p == "/" {
		p = "index.html"
	}
	// Prevent path traversal: clean and ensure p stays inside ./static
	clean := filepath.Clean(p)
	if strings.HasPrefix(clean, "..") || strings.Contains(clean, "/../") {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, "./static/"+clean)
}

// FaviconHandler serves the site favicon. We ship a single SVG and let the
// browser decide what to do with it. Also acts as /favicon.ico so legacy
// browsers don't 404.
func (a *App) FaviconHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeFile(w, r, "./static/favicon.svg")
}

