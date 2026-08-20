#!/bin/bash
# check_b152.sh — static asset sanity (BL-2 follow-up: "долго переключение между страницами")
#
# Background (2026-08-20): operator reported page-switching in the web
# UI is slow. Root cause: 46 templates reference /static/css/font-awesome.min.css
# which didn't exist on disk (404 every navigation) AND the StaticHandler
# had no Cache-Control headers (every navigation re-fetched themes.css).
#
# B152 (this file) pins the fixes:
#   1. font-awesome.min.css MUST exist (no 404 on every page)
#   2. The 3 WOFF2 webfonts MUST exist (CSS references them at ../webfonts/...)
#   3. The StaticHandler MUST send Cache-Control on every response
#   4. The favicon handler MUST also send Cache-Control
#
# Without these checks, the silent 404 + uncached CSS would regress
# (the 404 doesn't fail go build / go test, only the live browser
# shows the perf issue).

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: font-awesome.min.css exists on disk ==="
if [ -f "static/css/font-awesome.min.css" ]; then
    sz=$(wc -c < "static/css/font-awesome.min.css")
    if [ "$sz" -gt 10000 ]; then
        ok "static/css/font-awesome.min.css exists ($sz bytes)"
    else
        bad "static/css/font-awesome.min.css is too small ($sz bytes), looks empty"
    fi
else
    bad "static/css/font-awesome.min.css MISSING — every page would 404 on /static/css/font-awesome.min.css"
fi

echo ""
echo "=== contract B: 3 WOFF2 webfonts exist ==="
for f in fa-solid-900 fa-regular-400 fa-brands-400; do
    if [ -f "static/webfonts/$f.woff2" ]; then
        sz=$(wc -c < "static/webfonts/$f.woff2")
        if [ "$sz" -gt 1000 ]; then
            ok "static/webfonts/$f.woff2 exists ($sz bytes)"
        else
            bad "static/webfonts/$f.woff2 too small ($sz bytes)"
        fi
    else
        bad "static/webfonts/$f.woff2 MISSING — font-awesome CSS references this"
    fi
done

echo ""
echo "=== contract C: StaticHandler sets Cache-Control ==="
if grep -q 'Cache-Control' internal/handlers/static.go; then
    ok "internal/handlers/static.go has Cache-Control"
else
    bad "internal/handlers/static.go MISSING Cache-Control — every page re-fetches CSS"
fi

if grep -q 'max-age=31536000' internal/handlers/static.go; then
    ok "Cache-Control: immutable for content-hashed assets (1 year)"
else
    bad "StaticHandler missing immutable Cache-Control for content-hashed assets"
fi

if grep -q 'max-age=86400' internal/handlers/static.go; then
    ok "Cache-Control: 1-day must-revalidate for versioned assets"
else
    bad "StaticHandler missing 1-day must-revalidate Cache-Control for versioned assets"
fi

echo ""
echo "=== contract D: FaviconHandler also has Cache-Control ==="
if grep -q 'Cache-Control' internal/handlers/static.go | grep -A1 'Favicon' | head -1 || \
   awk '/FaviconHandler/,/^}$/' internal/handlers/static.go | grep -q 'Cache-Control'; then
    ok "FaviconHandler has Cache-Control"
else
    bad "FaviconHandler missing Cache-Control"
fi

echo ""
echo "=== contract E: layout.html references local font-awesome ==="
# We should reference the LOCAL font-awesome, not a CDN. The previous
# /static/css/font-awesome.min.css reference was correct, just missing
# the file. After B152 the file exists, so this is a regression check.
if grep -q '/static/css/font-awesome.min.css' internal/handlers/templates/layout.html; then
    ok "layout.html references /static/css/font-awesome.min.css (local)"
else
    bad "layout.html no longer references /static/css/font-awesome.min.css — icons broken?"
fi

# We should NOT reference external CDNs for font-awesome (operator's
# privacy + offline-first policy: no external dependencies).
if grep -qiE 'cdnjs|jsdelivr|unpkg|fontawesome\.com.*css' internal/handlers/templates/layout.html; then
    bad "layout.html references external font-awesome CDN — should be local"
else
    ok "layout.html uses local font-awesome (no external CDN dependency)"
fi

echo ""
echo "=== contract F: pre-push hook has B152 wired ==="
# The pre-push hook (scripts/verify_pre_deploy.sh) must include B152 so
# the check runs in the standard pre-push gate.
if grep -q "B152\|check_b152" scripts/verify_pre_deploy.sh; then
    ok "verify_pre_deploy.sh includes B152"
else
    bad "verify_pre_deploy.sh missing B152 registration"
fi

echo ""
echo "=== summary ==="
echo "B152: static asset sanity (font-awesome bundled + Cache-Control on /static/)"
echo "all B152 contracts satisfied"
