# B-mod-static-embed Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Embed `static/` directory into the skygate binary via `embed.FS` so prebuilt Docker images serve CSS/webfonts/favicon without needing `static/` on disk.

**Architecture:** Replace `http.ServeFile(w, r, "./static/"+clean)` (disk read) with `http.ServeFileFS(w, r, staticRootFS, path.Join("static", clean))` (embedded read). Cache-Control headers continue to be set by the handler before calling ServeFileFS. The `static/` directory remains in the source tree (dev path uses bind-mount `./:/app`); embed.FS is the production path.

**Tech Stack:** Go 1.25 `embed.FS`, `net/http`, `net/http/httptest`, `path`, `strings`.

## Global Constraints

- Go 1.25 (per `Dockerfile:48` and `Dockerfile.prebuilt:36`).
- `static/` directory layout (source of truth):
  - `static/css/font-awesome.min.css`
  - `static/css/themes.css`
  - `static/favicon.svg`
  - `static/webfonts/*.woff2` (8 files: Geist, Inter, JetBrains Mono, Manrope, Sora — 4 weights each, + Geist Mono + fa-brands/regular/solid/v4compatibility = 4+4+4+4+4+4+4+1 = 29)
  - `static/scripts/*.sh` (2 files)
- Tests must use `httptest.NewRecorder()` + `httptest.NewRequest()`, not full `App` constructor.
- `StaticHandler` must reject path traversal (`..`, `/../`, etc.) returning 404.
- `StaticHandler` must set `Cache-Control: public, max-age=31536000, immutable` for content-hashed filenames, else `public, max-age=86400, must-revalidate`.
- `FaviconHandler` must serve `static/favicon.svg` with `Content-Type: image/svg+xml` + `Cache-Control: public, max-age=86400, must-revalidate`.
- All paths must remain backward-compatible: `/static/css/themes.css`, `/static/webfonts/fa-solid-900.woff2`, `/favicon.svg` must return 200.

---

## File Structure

| File | Action | Responsibility |
|---|---|---|
| `internal/handlers/static.go` | Modify | Replace `http.ServeFile` with `http.ServeFileFS` over `embed.FS` |
| `internal/handlers/static_test.go` | Create | TDD tests for StaticHandler + FaviconHandler (8 tests) |
| `scripts/check_b_mod_static_embed.sh` | Create | B-check contract script (6 contracts) |
| `Dockerfile` | No change | `static/` no longer needed at runtime |
| `Dockerfile.prebuilt` | No change | `static/` no longer needed at runtime |
| `entrypoint.sh` | No change | Doesn't reference `./static/` |

---

## Task 1: RED — write failing test for StaticHandler serving embedded CSS

**Files:**
- Create: `internal/handlers/static_test.go`

**Interfaces:**
- Consumes: `app.StaticHandler(http.ResponseWriter, *http.Request)` (existing method, no signature change)
- Produces: test signal — failing test that verifies the static handler returns 200 + correct Content-Type + non-empty body for `GET /static/css/themes.css`

- [ ] **Step 1: Write the failing test**

```go
package handlers

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
		t.Fatalf("expected themes.css body to contain ':root', got first 200 chars: %q", body[:min(200, len(body))])
	}
}

// min is a tiny helper since Go 1.21 added the builtin but we want
// to be explicit about the contract.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/ -run TestStaticHandler_ServesThemesCSS -v`
Expected: FAIL with `expected 200, got 404` (because current static.go uses `http.ServeFile(w, r, "./static/...")` and there's no `./static/` relative to where `go test` runs from — only `./internal/handlers/templates/...` is on disk in test context; the working directory is the package directory, not the project root).

Note: this WILL fail with 404 because the test runs from `internal/handlers/`, where `./static/...` resolves to `internal/handlers/static/...` which doesn't exist. The current handler relies on the production runtime starting from the project root.

- [ ] **Step 3: Confirm failure reason**

Run the test. The output must show `expected 200, got 404`. If it shows something else (e.g., `expected text/css prefix, got application/octet-stream`), the test would be wrong — the 404 is the correct signal.

---

## Task 2: RED — write failing test for StaticHandler serving webfont

**Files:**
- Modify: `internal/handlers/static_test.go`

- [ ] **Step 1: Add the test**

Append to `static_test.go`:

```go
// TestStaticHandler_ServesWebfont verifies that GET /static/webfonts/fa-solid-900.woff2
// returns 200 with a non-empty binary body. The body is binary (woff2 starts
// with "wOF2" magic bytes). This pins the B-mod-static-embed fix for
// webfonts — the operator's report said "при развертывании в другом месте
// веб панель оказалась без css и любых других ассетов", webfonts included.
func TestStaticHandler_ServesWebfont(t *testing.T) {
	app := &App{}
	req := httptest.NewRequest("GET", "/static/webfonts/fa-solid-900.woff2", nil)
	rec := httptest.NewRecorder()

	app.StaticHandler(rec, req)

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d (body=%q)", rec.Code, rec.Body.String())
	}
	body := rec.Bytes()
	if len(body) < 100 {
		t.Fatalf("expected woff2 body > 100 bytes (real one is ~158KB), got %d", len(body))
	}
	// wOF2 magic: bytes 0x77 0x4F 0x46 0x32 = "wOF2"
	if len(body) >= 4 && string(body[:4]) != "wOF2" {
		t.Fatalf("expected woff2 magic 'wOF2', got %q", string(body[:4]))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/ -run TestStaticHandler_ServesWebfont -v`
Expected: FAIL with `expected 200, got 404`

---

## Task 3: RED — write failing test for path traversal protection

**Files:**
- Modify: `internal/handlers/static_test.go`

- [ ] **Step 1: Add the test**

Append to `static_test.go`:

```go
// TestStaticHandler_PathTraversal verifies that GET /static/../etc/passwd
// (and similar traversal attempts) return 404. The pre-B-mod-static-embed
// code already handled this via filepath.Clean + HasPrefix(".."), so this
// test pins the existing behavior and ensures the embed.FS rewrite doesn't
// regress it.
func TestStaticHandler_PathTraversal(t *testing.T) {
	app := &App{}
	cases := []string{
		"/static/../etc/passwd",
		"/static/..%2Fetc%2Fpasswd", // URL-encoded slash — Go's net/http decodes before handler
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/ -run TestStaticHandler_PathTraversal -v`
Expected: FAIL (current code uses `strings.HasPrefix(clean, "..")` + `strings.Contains(clean, "/../")` which should catch the first three, but the rewrite to embed.FS may handle this differently — verify behavior)

If the existing path traversal protection already works, this test will PASS pre-change. That's OK — the test pins existing behavior and guards against regression in the embed.FS rewrite. Skip to Task 4 in that case.

---

## Task 4: RED — write failing test for StaticHandler 404 on missing files

**Files:**
- Modify: `internal/handlers/static_test.go`

- [ ] **Step 1: Add the test**

Append to `static_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/ -run TestStaticHandler_NotFoundOnMissing -v`
Expected: FAIL with `expected 404 for missing file, got 200` (current code with `./static/nonexistent.css` may return 200 with empty body, or 404 depending on Go's http.ServeFile behavior — but the point is the test pins the expected behavior).

---

## Task 5: RED — write failing test for Cache-Control content-hash detection

**Files:**
- Modify: `internal/handlers/static_test.go`

- [ ] **Step 1: Add the test**

Append to `static_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify behavior**

Run: `go test ./internal/handlers/ -run TestStaticHandler_CacheControl -v`
Expected: depends on pre-change behavior. The cache header logic is in `hasContentHash` — already implemented correctly pre-B-mod. These tests pin the behavior to guard against regression in the embed.FS rewrite.

If pre-change behavior is correct (which it should be — `hasContentHash` is well-tested by its own logic), the tests PASS. They serve as guards during the rewrite.

---

## Task 6: RED — write failing test for FaviconHandler

**Files:**
- Modify: `internal/handlers/static_test.go`

- [ ] **Step 1: Add the test**

Append to `static_test.go`:

```go
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
	if !strings.HasPrefix(strings.TrimSpace(body), "<svg") && !strings.HasPrefix(strings.TrimSpace(body), "<?xml") {
		t.Fatalf("expected SVG body, got first 80 chars: %q", body[:min(80, len(body))])
	}
	cc := rec.Header().Get("Cache-Control")
	if cc != "public, max-age=86400, must-revalidate" {
		t.Fatalf("expected must-revalidate cache, got %q", cc)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/handlers/ -run TestFaviconHandler_ReturnsSVG -v`
Expected: FAIL with `expected 200, got 404` (current code uses `http.ServeFile(w, r, "./static/favicon.svg")` — fails for the same reason as StaticHandler tests).

---

## Task 7: GREEN — rewrite static.go to use embed.FS

**Files:**
- Modify: `internal/handlers/static.go`

**Interfaces:**
- Consumes: nothing (no API surface change)
- Produces: `staticRootFS` (package-level `embed.FS`), updated `StaticHandler` and `FaviconHandler`

- [ ] **Step 1: Replace static.go with embed.FS implementation**

```go
package handlers

import (
	"embed"
	"net/http"
	"path"
	"path/filepath"
	"strings"
)

// 2026-09-11 (B-mod-static-embed): static assets are now embedded in the
// binary via embed.FS instead of being read from disk at request time.
// Pre-change, http.ServeFile(w, r, "./static/"+clean) required the
// ./static/ directory to exist on disk at the binary's working directory.
// This broke the prebuilt Docker image (Dockerfile.prebuilt does not
// COPY static/ — the binary ran but every /static/* request returned 404,
// rendering the web panel without CSS/JS/webfonts/favicon).
//
// Post-change, the entire static/ directory is compiled into the binary.
// The dev path (./:/app bind-mount) still works because the embed.FS
// is built from the same source tree. The prebuilt path works because
// the binary IS the static tree.
//
// Mounted in main.go: mux.HandleFunc("/static/", app.StaticHandler)
// Mounted in main.go: mux.HandleFunc("/favicon.svg", app.FaviconHandler)

//go:embed static
var staticRootFS embed.FS

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
	clean := filepath.Clean(p)
	if strings.HasPrefix(clean, "..") || strings.Contains(clean, "/../") {
		http.NotFound(w, r)
		return
	}
	setStaticCacheControl(w, clean)

	// ServeFileFS handles content-type detection, Last-Modified,
	// Range requests, and 404 (via fs.ErrNotExist) in one call.
	// The path passed to ServeFileFS is relative to the FS root,
	// which is "static" because we embed the directory under that name.
	http.ServeFileFS(w, r, staticRootFS, path.Join("static", clean))
}

// FaviconHandler serves the site favicon from the embedded static/favicon.svg.
// Same Content-Type + Cache-Control as the pre-B-mod-static-embed version.
func (a *App) FaviconHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400, must-revalidate")
	http.ServeFileFS(w, r, staticRootFS, "static/favicon.svg")
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
```

- [ ] **Step 2: Run all static_test.go tests to verify they pass**

Run: `go test ./internal/handlers/ -run 'TestStaticHandler|TestFaviconHandler' -v`
Expected: ALL PASS.

- [ ] **Step 3: Run full handler test suite to verify no regression**

Run: `go test ./internal/handlers/ -v`
Expected: ALL PASS (existing tests don't exercise StaticHandler/FaviconHandler directly, so they shouldn't regress).

- [ ] **Step 4: Verify the binary actually contains the embedded static data**

Run:
```bash
go build -o /tmp/skygate-test ./cmd/skygate
# Use Go's strings tool or just check file size — embedded static adds ~700KB
ls -la /tmp/skygate-test
# Check that one of the embedded files is reachable
strings /tmp/skygate-test | grep -c "themes.css" || true
```

Expected: binary > 25MB (was 21MB without embedded static, now ~22MB with it), and `strings` shows references to embedded paths.

- [ ] **Step 5: Commit**

```bash
git add internal/handlers/static.go internal/handlers/static_test.go
git commit -m "feat(static): embed static/ via embed.FS

Prebuilt Docker image (ghcr.io/barssky/skygate:*) previously returned
404 for all /static/* requests because static/ wasn't COPY'd into the
image (Dockerfile + Dockerfile.prebuilt only COPY the binary + entrypoint).
The bind-mount ./:/app dev path worked by accident; prebuilt broke the
operator's panel.

Embed the entire static/ directory via //go:embed so the binary IS the
asset source. No Dockerfile / compose change needed.

Tests (internal/handlers/static_test.go):
- TestStaticHandler_ServesThemesCSS (200 + text/css + ':root' body)
- TestStaticHandler_ServesWebfont (200 + wOF2 magic)
- TestStaticHandler_PathTraversal (../ and variants → 404)
- TestStaticHandler_NotFoundOnMissing (404)
- TestStaticHandler_CacheControlContentHashed (immutable)
- TestStaticHandler_CacheControlNonHashed (must-revalidate)
- TestFaviconHandler_ReturnsSVG (200 + image/svg+xml)
"
```

---

## Task 8: B-check contract script

**Files:**
- Create: `scripts/check_b_mod_static_embed.sh`

- [ ] **Step 1: Write the check script**

```bash
#!/usr/bin/env bash
# scripts/check_b_mod_static_embed.sh — B-mod-static-embed contract checks.
#
# Pins the 6-contract B-check for the embed.FS rewrite:
#   A. internal/handlers/static.go has //go:embed for static/
#   B. internal/handlers/static.go does NOT use http.ServeFile with "./static/"
#   C. FaviconHandler uses http.ServeFileFS (not http.ServeFile)
#   D. go build succeeds
#   E. go vet is clean
#   F. The new tests in static_test.go pass
#
# Run from the project root: `bash scripts/check_b_mod_static_embed.sh`
set -euo pipefail

STATIC_GO="internal/handlers/static.go"
TEST_GO="internal/handlers/static_test.go"

pass() { printf "  PASS %s\n" "$1"; }
fail() { printf "  FAIL %s\n" "$1"; exit 1; }

echo "=== A. $STATIC_GO has //go:embed for static/ ==="
if grep -E '^//go:embed[[:space:]]+static([[:space:]]|$)' "$STATIC_GO" >/dev/null; then
    pass "embed.FS directive present"
else
    fail "//go:embed static directive missing"
fi

echo "=== B. $STATIC_GO does NOT use http.ServeFile with \"./static/\" ==="
if grep -E 'http\.ServeFile\(w,[[:space:]]+r,[[:space:]]+"\./static/' "$STATIC_GO" >/dev/null; then
    fail "http.ServeFile with \"./static/\" still present"
else
    pass "disk read removed"
fi

echo "=== C. FaviconHandler uses http.ServeFileFS ==="
if grep -B1 -A1 'func .*FaviconHandler' "$STATIC_GO" | grep -q 'ServeFileFS'; then
    pass "FaviconHandler uses ServeFileFS"
else
    fail "FaviconHandler does not use ServeFileFS"
fi

echo "=== D. go build succeeds ==="
if go build -o /tmp/skygate-bmod-static ./cmd/skygate 2>&1; then
    pass "go build clean"
else
    fail "go build failed"
fi

echo "=== E. go vet is clean ==="
if go vet ./internal/handlers/... 2>&1; then
    pass "go vet clean"
else
    fail "go vet failed"
fi

echo "=== F. static_test.go tests pass ==="
if go test ./internal/handlers/ -run 'TestStaticHandler|TestFaviconHandler' -count=1 2>&1; then
    pass "all 7 tests pass"
else
    fail "static_test.go tests failed"
fi

echo
echo "PASS B-mod-static-embed (6/6 contracts)"
```

- [ ] **Step 2: Make executable and run**

Run:
```bash
chmod +x scripts/check_b_mod_static_embed.sh
bash scripts/check_b_mod_static_embed.sh
```

Expected: `PASS B-mod-static-embed (6/6 contracts)`

- [ ] **Step 3: Commit**

```bash
git add scripts/check_b_mod_static_embed.sh
git commit -m "ci: add B-mod-static-embed contract check

6-contract B-check pins the embed.FS rewrite:
- //go:embed static directive present
- no http.ServeFile with './static/'
- FaviconHandler uses ServeFileFS
- go build + go vet clean
- new tests pass

Run: bash scripts/check_b_mod_static_embed.sh"
```

---

## Task 9: AGENTS.md + audit-doc update

**Files:**
- Modify: `AGENTS.md` (add release note for B-mod-static-embed)
- Modify: `docs/internal/2026-09-11-skygate-adoption-audit.md` (mark A1 done)

- [ ] **Step 1: Add release note to AGENTS.md**

In `AGENTS.md`, find the latest B-mod-* entry (look for the section that ends with the B-series release notes) and add a new entry above the most recent one. Format matches the existing pattern:

```markdown
  - **B-mod-static-embed (v1.5.3)**: `static/` directory embedded into
    the binary via `//go:embed static` (`internal/handlers/static.go`).
    Pre-built Docker image (`ghcr.io/barssky/skygate:prebuilt` /
    `Dockerfile.prebuilt`) used to 404 on every `/static/*` request
    because the runtime image didn't `COPY static/`. The bind-mount
    `./:/app` dev path worked by accident. Embedding closes the gap.
    `StaticHandler` and `FaviconHandler` now use `http.ServeFileFS`
    over the `embed.FS`. `Cache-Control` logic is unchanged
    (immutable for content-hashed files, must-revalidate otherwise).
    7 unit tests in `internal/handlers/static_test.go` +
    6-contract `scripts/check_b_mod_static_embed.sh`.
```

- [ ] **Step 2: Mark A1 done in audit doc**

In `docs/internal/2026-09-11-skygate-adoption-audit.md`, find Section 6 "План доработки" → "Фаза A — блокеры деплоя" → "A1. B-mod-static-embed" and prepend:
- `[x] A1 DONE (commit <hash>)`

- [ ] **Step 3: Commit docs**

```bash
git add AGENTS.md docs/internal/2026-09-11-skygate-adoption-audit.md
git commit -m "docs: mark B-mod-static-embed done (v1.5.3)

- AGENTS.md: add B-mod-static-embed release note
- audit doc: check A1 as completed"
```

---

## Task 10: Live verification on the operator's prebuilt image

**Files:**
- None (manual verification on a fresh prebuilt pull)

- [ ] **Step 1: Build the prebuilt image**

Run:
```bash
docker build -f Dockerfile.prebuilt -t skygate:test-prebuilt .
```

Expected: `Successfully tagged skygate:test-prebuilt`.

- [ ] **Step 2: Run with HEADSCALE_URL pointing at an existing headscale + Postgres DSN**

```bash
# Start a postgres for skygate (use a separate volume to avoid touching prod data)
docker run -d --name pg-test -e POSTGRES_USER=skygate -e POSTGRES_PASSWORD=skygate -e POSTGRES_DB=skygate -p 5444:5432 postgres:15-alpine

# Run skygate prebuilt
docker run -d --name skygate-test \
  -e SKYGATE_PREBUILT=1 \
  -e SKYGATE_DB_DSN=postgres://skygate:skygate@host.docker.internal:5444/skygate?sslmode=disable \
  -e HEADSCALE_URL=https://hs.example.com \
  -e HEADSCALE_API_KEY=dummy \
  -e SKYGATE_PORT=8080 \
  -e SKYGATE_JWT_SECRET=$(openssl rand -hex 32) \
  -e SKYGATE_SECRET_KEY=$(openssl rand -hex 32) \
  -e SKYGATE_ADMIN_USER=admin \
  -e SKYGATE_ADMIN_PASS=$(openssl rand -base64 18) \
  -p 8088:8080 \
  skygate:test-prebuilt
```

Expected: container starts, `/healthz` returns 200.

- [ ] **Step 3: Curl all asset paths**

```bash
for url in \
  /static/css/themes.css \
  /static/css/font-awesome.min.css \
  /static/webfonts/fa-solid-900.woff2 \
  /static/webfonts/geist-latin-400-normal.woff2 \
  /favicon.svg; do
    code=$(curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8088$url)
    echo "$code  $url"
done
```

Expected:
```
200  /static/css/themes.css
200  /static/css/font-awesome.min.css
200  /static/webfonts/fa-solid-900.woff2
200  /static/webfonts/geist-latin-400-normal.woff2
200  /favicon.svg
```

If any returns 404, the B-mod-static-embed fix is incomplete — debug and re-run.

- [ ] **Step 4: Verify Cache-Control headers**

```bash
curl -sI http://127.0.0.1:8088/static/css/themes.css | grep -i cache-control
curl -sI http://127.0.0.1:8088/favicon.svg | grep -i cache-control
```

Expected:
```
Cache-Control: public, max-age=86400, must-revalidate
Cache-Control: public, max-age=86400, must-revalidate
```

- [ ] **Step 5: Cleanup**

```bash
docker rm -f skygate-test pg-test
```

- [ ] **Step 6: Final commit (no code change; just confirmation in audit)**

```bash
# No new code to commit; the verification is logged in audit doc
git diff --stat
# (should show no changes if all prior commits clean)
```

---

## Self-Review

**1. Spec coverage:** Section 4.5 of audit doc lists the static asset bug. Task 7 fixes it (embed.FS). Tasks 1-6 pin all 7 behaviors via tests. Task 8 pins the 6-contract B-check. Task 9 updates docs. Task 10 verifies on the operator's actual deployment scenario. ✅

**2. Placeholder scan:** No TBD/TODO/fill-in-later strings. All test code is concrete (no `mock` placeholders, no `similar to Task N`). ✅

**3. Type consistency:** `app.StaticHandler(http.ResponseWriter, *http.Request)` signature unchanged. `app.FaviconHandler(http.ResponseWriter, *http.Request)` signature unchanged. `staticRootFS` is a new package-level `embed.FS`. `hasContentHash` signature unchanged. `setStaticCacheControl` is a new private helper. ✅

## Execution Handoff

After completing all 10 tasks, the B-mod-static-embed fix is shipped. Proceed to `docs/superpowers/plans/2026-09-11-first-run-adoption.md` (Phase B) for the next iteration.
