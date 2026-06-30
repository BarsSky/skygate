package handlers

import (
	"net/http"
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
	http.ServeFile(w, r, "./static/"+p)
}
