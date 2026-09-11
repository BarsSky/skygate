#!/usr/bin/env bash
# scripts/check_b_mod_static_embed.sh — B-mod-static-embed contract checks.
#
# Pins the 6-contract B-check for the embed.FS rewrite:
#   A. internal/staticfs/staticfs.go exists with //go:embed for static/
#   B. internal/handlers/static.go does NOT use http.ServeFile with "./static/"
#   C. internal/handlers/static.go uses embed.FS via staticfs.FS
#   D. go build succeeds
#   E. go vet is clean on handlers package
#   F. The new tests in static_test.go pass (all 7)
#
# Run from the project root: `bash scripts/check_b_mod_static_embed.sh`
set -euo pipefail

# Resolve `go` binary: prefer a cached env var (CI passes GO_BIN), then
# `command -v`, then a few well-known locations. We DON'T rely on PATH
# alone because Git Bash / WSL bash on Windows often doesn't get the
# Go bin dir into PATH even when the parent PowerShell has it.
GO="${GO_BIN:-}"
if [ -z "$GO" ]; then
    GO=$(command -v go 2>/dev/null || true)
fi
if [ -z "$GO" ]; then
    # Search well-known locations. Quoted to handle "Program Files" etc.
    for CAND in \
        "/c/Program Files/Go/bin/go.exe" \
        "/c/Program Files (x86)/Go/bin/go.exe" \
        "/mnt/c/Program Files/Go/bin/go.exe" \
        "/mnt/c/Program Files (x86)/Go/bin/go.exe" \
        "/usr/local/go/bin/go" \
        "/usr/bin/go" \
        "/opt/homebrew/bin/go"; do
        if [ -x "$CAND" ]; then
            GO="$CAND"
            break
        fi
    done
fi
if [ -z "$GO" ] && command -v where.exe >/dev/null 2>&1; then
    WIN_GO=$(where.exe go 2>/dev/null | head -1 | tr -d '\r\n')
    if [ -n "$WIN_GO" ]; then
        GO="$WIN_GO"
    fi
fi
if [ -z "$GO" ]; then
    echo "FATAL: go binary not found. Tried: command -v go, well-known locations, where.exe go" >&2
    echo "Hint: install Go 1.25+ or run this script with go on PATH" >&2
    echo "      CI override: GO_BIN=/path/to/go.exe bash scripts/check_b_mod_static_embed.sh" >&2
    exit 1
fi
# Normalize Windows backslashes to forward slashes for bash-friendly paths.
GO_NORM=$(echo "$GO" | tr '\\' '/')

STATICFS_GO="internal/staticfs/staticfs.go"
STATIC_GO="internal/handlers/static.go"
TEST_GO="internal/handlers/static_test.go"

pass() { printf "  PASS %s\n" "$1"; }
fail() { printf "  FAIL %s\n" "$1"; exit 1; }

echo "=== A. $STATICFS_GO has //go:embed for static/ ==="
if [ ! -f "$STATICFS_GO" ]; then
    fail "$STATICFS_GO missing — B-mod-static-embed requires a dedicated package at internal/staticfs/"
fi
if grep -E '^//go:embed[[:space:]]+static([[:space:]]|$)' "$STATICFS_GO" >/dev/null; then
    pass "embed.FS directive present"
else
    fail "//go:embed static directive missing"
fi

echo "=== B. $STATIC_GO does NOT use http.ServeFile with \"./static/\" ==="
# Match lines that start (after whitespace) with http.ServeFile (not in a comment).
# The pattern requires the call to NOT be preceded by // (comment marker).
if grep -vE '^[[:space:]]*//' "$STATIC_GO" | grep -E 'http\.ServeFile\(w,[[:space:]]+r,[[:space:]]+"\./static/' >/dev/null; then
    fail "http.ServeFile with \"./static/\" still present — prebuilt image will still 404 on /static/*"
else
    pass "disk read removed"
fi

echo "=== C. $STATIC_GO imports and uses staticfs.FS ==="
if grep -q '"skygate/internal/staticfs"' "$STATIC_GO"; then
    if grep -q 'staticfs\.FS' "$STATIC_GO"; then
        pass "staticfs.FS is imported and used"
    else
        fail "staticfs imported but not used"
    fi
else
    fail "staticfs not imported — embed.FS not wired"
fi

echo "=== D. go build succeeds ==="
TMPBIN=$(mktemp -u -p /tmp skygate-bmod-XXXXXX)
if "$GO" build -o "$TMPBIN" ./cmd/skygate 2>&1; then
    BYTES=$(stat -c '%s' "$TMPBIN" 2>/dev/null || stat -f '%z' "$TMPBIN")
    if [ "$BYTES" -lt 25000000 ]; then
        fail "binary suspiciously small ($BYTES bytes) — static/ may not be embedded"
    fi
    pass "go build clean (binary: ${BYTES} bytes, ~$((BYTES/1024/1024)) MB; >= 25 MB expected with embedded static/)"
    rm -f "$TMPBIN"
else
    rm -f "$TMPBIN"
    fail "go build failed"
fi

echo "=== E. go vet is clean on internal/handlers ==="
if "$GO" vet ./internal/handlers/... 2>&1; then
    pass "go vet clean"
else
    fail "go vet failed"
fi

echo "=== F. static_test.go tests pass (7 tests) ==="
if "$GO" test ./internal/handlers/ -run 'TestStaticHandler|TestFaviconHandler' -count=1 -v 2>&1; then
    # Count PASS lines as a sanity check
    N=$("$GO" test ./internal/handlers/ -run 'TestStaticHandler|TestFaviconHandler' -count=1 -v 2>&1 | grep -c '^--- PASS')
    if [ "$N" -ge 7 ]; then
        pass "all 7 tests pass"
    else
        fail "expected >= 7 PASS lines, got $N"
    fi
else
    fail "static_test.go tests failed"
fi

echo
echo "PASS B-mod-static-embed (6/6 contracts)"
