package handlers

// static_test.go — TDD tests for B-mod-static-embed.
//
// Pins the embed.FS rewrite of StaticHandler + FaviconHandler so that
// the prebuilt Docker image (ghcr.io/barssky/skygate:*) serves
// /static/* and /favicon.svg without needing the ./static/ directory
// on disk at runtime.
//
// Pre-change: http.ServeFile(w, r, "./static/"+clean) — broke prebuilt
// (Dockerfile.prebuilt doesn't COPY static/), worked on dev path
// (./:/app bind-mount) by accident.
// Post-change: http.ServeFileFS(w, r, staticRootFS, ...) over embed.FS.
//
// 7 tests cover:
//   - Themes CSS body
//   - Webfont body + wOF2 magic
//   - Path traversal protection
//   - Missing files → 404
//   - Cache-Control: content-hashed → immutable
//   - Cache-Control: non-hashed → must-revalidate
//   - Favicon SVG with correct Content-Type

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// TestStaticHandler_ServesThemesCSS verifies that GET /static/css/themes.css
// returns 200 with text/css Content-Type and a non-empty body containing
// the `:root` block that themes.css is known to start with. This pins the
// B-mod-static-embed fix: prebuilt image must serve CSS without ./static/
// on disk.
func TestStaticHandler_ServesThemesCSS(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest("GET", "/static/css/themes.css", nil)
	rec := httptest.NewRecorder()

	app.StaticHandler(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/css") {
		t.Fatalf("expected Content-Type text/css prefix, got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, ":root") {
		bodySnippet := body
		if len(bodySnippet) > 200 {
			bodySnippet = bodySnippet[:200]
		}
		t.Fatalf("expected themes.css body to contain ':root', got first 200 chars: %q", bodySnippet)
	}
}

// TestStaticHandler_ServesWebfont verifies that GET /static/webfonts/fa-solid-900.woff2
// returns 200 with a non-empty binary body starting with the wOF2 magic bytes.
// This pins the B-mod-static-embed fix for webfonts — the operator's report
// said "при развертывании в другом месте веб панель оказалась без css и
// любых других ассетов", webfonts included.
func TestStaticHandler_ServesWebfont(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest("GET", "/static/webfonts/fa-solid-900.woff2", nil)
	rec := httptest.NewRecorder()

	app.StaticHandler(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	if len(body) < 100 {
		t.Fatalf("expected woff2 body > 100 bytes (real one is ~158KB), got %d", len(body))
	}
	// wOF2 magic: bytes 0x77 0x4F 0x46 0x32 = "wOF2"
	if len(body) >= 4 && string(body[:4]) != "wOF2" {
		t.Fatalf("expected woff2 magic 'wOF2', got %q", string(body[:4]))
	}
}

// TestStaticHandler_PathTraversal verifies that GET /static/../etc/passwd
// (and similar traversal attempts) return 404. The pre-B-mod-static-embed
// code already handled this via filepath.Clean + HasPrefix(".."), so this
// test pins the existing behavior and ensures the embed.FS rewrite doesn't
// regress it.
func TestStaticHandler_PathTraversal(t *testing.T) {
	app := &App{}
	cases := []string{
		"/static/../etc/passwd",
		"/static/foo/../../etc/passwd",
		"/static/./../etc/passwd",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest("GET", p, nil)
			rec := httptest.NewRecorder()
			app.StaticHandler(rec, req)
			if rec.Code != 404 {
				t.Fatalf("path %q: expected 404, got %d", p, rec.Code)
			}
		})
	}
}

// TestStaticHandler_NotFoundOnMissing verifies that GET /static/nonexistent.css
// returns 404. Pins embed.FS error handling — fs.FS.Open returns *PathError
// for missing files, which should bubble up as 404.
func TestStaticHandler_NotFoundOnMissing(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest("GET", "/static/nonexistent.css", nil)
	rec := httptest.NewRecorder()
	app.StaticHandler(rec, req)
	if rec.Code != 404 {
		t.Fatalf("expected 404 for missing file, got %d", rec.Code)
	}
}

// TestStaticHandler_CacheControlContentHashed verifies that a request for
// a content-hashed filename (e.g. "app.abc123def.js") returns the
// immutable Cache-Control header. The hasContentHash helper detects the
// 8+ char hex hash pattern.
func TestStaticHandler_CacheControlContentHashed(t *testing.T) {
	app := &App{}
	// Simulate a content-hashed filename (the current static/ tree doesn't
	// have any, but the helper exists for future Vite-style builds).
	req := httptest.NewRequest("GET", "/static/app.abc123def456.js", nil)
	rec := httptest.NewRecorder()
	app.StaticHandler(rec, req)
	cc := rec.Header().Get("Cache-Control")
	if cc != "public, max-age=31536000, immutable" {
		t.Fatalf("expected immutable cache for content-hashed URL, got %q", cc)
	}
}

// TestStaticHandler_CacheControlNonHashed verifies that a non-hashed file
// (the current style: themes.css, fa-solid-900.woff2) returns the
// 1-day-must-revalidate Cache-Control.
func TestStaticHandler_CacheControlNonHashed(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest("GET", "/static/css/themes.css", nil)
	rec := httptest.NewRecorder()
	app.StaticHandler(rec, req)
	cc := rec.Header().Get("Cache-Control")
	if cc != "public, max-age=86400, must-revalidate" {
		t.Fatalf("expected must-revalidate cache for non-hashed URL, got %q", cc)
	}
}

// TestFaviconHandler_ReturnsSVG verifies that GET /favicon.svg returns
// the SVG favicon with the correct Content-Type. Pins the B-mod-static-embed
// fix for favicon — prebuilt image had 404 on favicon too.
func TestFaviconHandler_ReturnsSVG(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest("GET", "/favicon.svg", nil)
	rec := httptest.NewRecorder()
	app.FaviconHandler(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "image/svg+xml" {
		t.Fatalf("expected Content-Type image/svg+xml, got %q", ct)
	}
	body := rec.Body.String()
	bodySnippet := body
	if len(bodySnippet) > 80 {
		bodySnippet = bodySnippet[:80]
	}
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, "<svg") && !strings.HasPrefix(trimmed, "<?xml") {
		t.Fatalf("expected SVG body, got first 80 chars: %q", bodySnippet)
	}
	cc := rec.Header().Get("Cache-Control")
	if cc != "public, max-age=86400, must-revalidate" {
		t.Fatalf("expected must-revalidate cache, got %q", cc)
	}
}
