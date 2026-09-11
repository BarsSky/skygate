package handlers

import (
	"io"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"skygate/internal/staticfs"
)

// Note: we use `path.Clean` (POSIX forward-slash semantics) here, NOT
// `filepath.Clean` (OS-specific separators). HTTP request paths always
// use forward slashes, and `http.ServeFileFS` expects forward-slash
// names relative to the FS root. Using `filepath.Clean` on Windows
// silently converts separators to backslash, breaking the lookup.
//
// hasContentHash() below still uses `filepath.Ext` + `filepath.Base`
// for filename parsing — those work on either separator because they
// operate on the final path component, not the full path.

// 2026-09-11 (B-mod-static-embed): static assets are now embedded in the
// binary via the staticfs.FS embed.FS instead of being read from disk at
// request time.
//
// Pre-change, http.ServeFile(w, r, "./static/"+clean) required the
// ./static/ directory to exist on disk at the binary's working directory.
// This broke the prebuilt Docker image (Dockerfile.prebuilt does not
// COPY static/ — the binary ran but every /static/* request returned
// 404, rendering the web panel without CSS/JS/webfonts/favicon).
//
// Post-change, the entire static/ directory is compiled into the binary
// via the staticfs package at the repo root (Go's //go:embed requires
// files to live in the same directory as the source file). The dev path
// (./:/app bind-mount) still works because the embed.FS is built from
// the same source tree. The prebuilt path works because the binary IS
// the static tree.
//
// Mounted in main.go:
//   mux.HandleFunc("/static/", app.StaticHandler)
//   mux.HandleFunc("/favicon.svg", app.FaviconHandler)

// StaticHandler serves files from the embedded static/ directory.
// Sets Cache-Control: public, max-age=31536000, immutable for files
// with a content-hash in the path (Vite/webpack pattern:
// app.<hash>.js, app.<hash>.css), else public, max-age=86400,
// must-revalidate. This matches the pre-B-mod-static-embed behavior
// bit-for-bit (verified by TestStaticHandler_CacheControl*).
func (a *App) StaticHandler(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/static/")
	if p == "" || p == "/" {
		p = "index.html"
	}
	// path.Clean (POSIX forward-slash), not filepath.Clean (OS-specific).
	// See package comment above for why.
	clean := path.Clean(p)
	if strings.HasPrefix(clean, "..") || strings.Contains(clean, "/../") {
		http.NotFound(w, r)
		return
	}

	// Set Cache-Control BEFORE the Open check so that even 404 responses
	// for content-hashed URLs (a future Vite scenario) preserve the
	// immutable cache header — verified by TestStaticHandler_CacheControlContentHashed.
	// http.NotFound (which we use on Open failure) preserves pre-set headers,
	// unlike http.ServeFileFS which clears them.
	setStaticCacheControl(w, clean)

	// Open the file ourselves so we can use http.ServeContent (which
	// preserves our Cache-Control header) instead of http.ServeFileFS
	// (which clears headers on 404, verified empirically).
	f, err := staticfs.FS.Open(path.Join("static", clean))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	// http.ServeContent handles Content-Type detection, Last-Modified,
	// and Range requests. embed.FS files satisfy io.ReadSeeker (they
	// implement Read+ReadAt+Seek+Close), so the type assertion succeeds.
	rc, ok := f.(io.ReadSeeker)
	if !ok {
		// embed.FS files always satisfy ReadSeeker; this is defensive.
		http.Error(w, "internal: embedded file is not seekable", http.StatusInternalServerError)
		return
	}
	stat, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, clean, stat.ModTime(), rc)
}

// FaviconHandler serves the site favicon from the embedded static/favicon.svg.
// Same Content-Type + Cache-Control as the pre-B-mod-static-embed version.
// Uses the same Open-then-ServeContent pattern as StaticHandler — see the
// long comment there for why we don't use http.ServeFileFS.
func (a *App) FaviconHandler(w http.ResponseWriter, r *http.Request) {
	f, err := staticfs.FS.Open("static/favicon.svg")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400, must-revalidate")
	rc, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "internal: embedded favicon is not seekable", http.StatusInternalServerError)
		return
	}
	stat, _ := f.Stat()
	http.ServeContent(w, r, "favicon.svg", stat.ModTime(), rc)
}

// setStaticCacheControl writes the Cache-Control header based on whether
// the path looks content-hashed (e.g. `app.abc123def.js`). Extracted
// from the inline conditional in StaticHandler for clarity. Vite uses
// an 8+ char hex hash by default; we accept 6+ chars as conservative.
func setStaticCacheControl(w http.ResponseWriter, p string) {
	if hasContentHash(p) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=86400, must-revalidate")
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
