package handlers

import (
	"net/http"
	"path/filepath"
	"strings"
)

// StaticHandler serves files from ./static directory.
// Mounted in main.go: mux.HandleFunc("/static/", app.StaticHandler)
//
// Sends `Cache-Control: public, max-age=31536000, immutable` for files
// with a content-hash in the path (the typical Vite/webpack build
// pattern: `app.<hash>.js`, `app.<hash>.css`). For files WITHOUT a
// content-hash (themes.css, font-awesome.min.css, the .woff2 webfonts
// that we ship as `static/webfonts/fa-solid-900.woff2` etc.), the
// cache is set to 1 day with a `must-revalidate` directive so the
// browser re-checks but doesn't block rendering on the cached copy.
// This is the standard pattern for static assets that may ship in
// future versions: long cache when the URL is content-addressed,
// short cache + revalidate when it's versioned by directory.
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
	// Set Cache-Control based on whether the path looks content-hashed
	// (e.g. `app.abc123def.js`). Vite uses a 8+ char hex hash by default.
	if hasContentHash(clean) {
		// Immutable for 1 year — the file content for this URL is permanent.
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		// 1 day cache, must revalidate. Long enough to avoid re-fetching
		// during a normal admin session, short enough that a release
		// (which renames the file or bumps a version param) takes effect
		// within 24h.
		w.Header().Set("Cache-Control", "public, max-age=86400, must-revalidate")
	}
	http.ServeFile(w, r, "./static/"+clean)
}

// FaviconHandler serves the site favicon. We ship a single SVG and let the
// browser decide what to do with it. Also acts as /favicon.ico so legacy
// browsers don't http.StatusNotFound.
//
// Cached for 1 day — favicon rarely changes; browsers refresh on their own.
func (a *App) FaviconHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400, must-revalidate")
	http.ServeFile(w, r, "./static/favicon.svg")
}

// hasContentHash returns true if the file name appears to contain a
// content hash (Vite/webpack build pattern: `app.abc123def.js`).
// Returns true for any path component matching `.+\.[a-f0-9]{6,}\.(js|css|mjs)$`.
// Conservative — false negatives just get 1 day cache, no correctness issue.
func hasContentHash(p string) bool {
	base := filepath.Base(p)
	ext := filepath.Ext(base) // ".js", ".css", ".mjs"
	if ext == "" {
		return false
	}
	name := strings.TrimSuffix(base, ext)
	// Look for a dot-separated hash: "app.abc123def" → split on "." → ["app", "abc123def"]
	// We require the LAST segment to be a long hex string.
	parts := strings.Split(name, ".")
	if len(parts) < 2 {
		return false
	}
	last := parts[len(parts)-1]
	if len(last) < 6 {
		return false
	}
	for _, c := range last {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
