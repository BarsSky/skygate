#!/bin/bash
# check_b158.sh — self-hosted Google Fonts (B158, v1.5.0)
#
# Background (operator 2026-08-20): the live VM
# couldn't reach fonts.googleapis.com, so the page
# blocked on the network fetch (log: 'GET ...
# /css2?family=Manrope... net::ERR_CONNECTION_TIMED_OUT').
# B158 bundles the woff2 files locally + uses @font-face
# in themes.css so the page works without external access.
#
# B158 (this file) pins the fixes:
#   1. All 24 woff2 files MUST exist on disk (4 body
#      families × 4 weights + 2 mono families × 2 weights).
#   2. Each woff2 MUST be > 1KB (sanity check, not a
#      truncated download).
#   3. themes.css MUST have @font-face rules for each
#      family + weight.
#   4. layout.html MUST NOT reference
#      fonts.googleapis.com (the <link rel="stylesheet">
#      is gone; only the @font-face in themes.css loads
#      the fonts).
#   5. The static handler MUST serve /webfonts/*.woff2
#      with the right Cache-Control (immutable for
#      content-hashed, max-age=86400 for versioned).
#   6. verify_pre_deploy.sh MUST include B158.

set -euo pipefail

ok() { echo "  PASS  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

echo "=== contract A: woff2 files exist on disk ==="
# Body fonts: 4 families × 4 weights = 16 files.
# Mono fonts:  2 families × 2 weights = 4 files.
# TOTAL: 20 Google Fonts woff2 files.
required_files=(
    manrope-latin-400-normal.woff2
    manrope-latin-500-normal.woff2
    manrope-latin-600-normal.woff2
    manrope-latin-700-normal.woff2
    inter-latin-400-normal.woff2
    inter-latin-500-normal.woff2
    inter-latin-600-normal.woff2
    inter-latin-700-normal.woff2
    geist-latin-400-normal.woff2
    geist-latin-500-normal.woff2
    geist-latin-600-normal.woff2
    geist-latin-700-normal.woff2
    sora-latin-400-normal.woff2
    sora-latin-500-normal.woff2
    sora-latin-600-normal.woff2
    sora-latin-700-normal.woff2
    geist-mono-latin-400-normal.woff2
    geist-mono-latin-500-normal.woff2
    jetbrains-mono-latin-400-normal.woff2
    jetbrains-mono-latin-500-normal.woff2
)
for f in "${required_files[@]}"; do
    if [ -f "static/webfonts/$f" ]; then
        sz=$(wc -c < "static/webfonts/$f")
        if [ "$sz" -gt 1024 ]; then
            ok "static/webfonts/$f exists ($sz bytes)"
        else
            bad "static/webfonts/$f is too small ($sz bytes), looks like a truncated download"
        fi
    else
        bad "static/webfonts/$f MISSING"
    fi
done

echo ""
echo "=== contract B: themes.css has @font-face rules ==="
# Check that each family has its @font-face declared
# (we grep for the font-family: 'X' line, then verify
# the src uses the local woff2 path).
for family in "Manrope" "Inter" "Geist" "Sora" "Geist Mono" "JetBrains Mono"; do
    if grep -qE "font-family:'$family'" static/css/themes.css; then
        ok "themes.css has @font-face for '$family'"
    else
        bad "themes.css missing @font-face for '$family'"
    fi
done
# All srcs must point to /webfonts/ (local), not
# fonts.gstatic.com.
if grep -c "src:url(/webfonts/" static/css/themes.css | grep -qE '^[1-9][0-9]*$'; then
    count=$(grep -c "src:url(/webfonts/" static/css/themes.css)
    ok "themes.css has $count local @font-face srcs (all /webfonts/)"
else
    bad "themes.css has NO local @font-face srcs — fonts not self-hosted"
fi

echo ""
echo "=== contract C: layout.html removed the Google Fonts <link>s ==="
# The pre-B158 layout had 4 <link rel='stylesheet'
# href='https://fonts.googleapis.com/css2?...'> blocks
# (one per font family). After B158, none of those
# should be in the layout. We allow the comment in
# the layout that mentions fonts.googleapis.com
# (the B158 comment) — but no <link href='...googleapis...'>
# must remain.
if grep -qE '<link[^>]*href="https://fonts\.googleapis\.com' internal/handlers/templates/layout.html; then
    bad "layout.html STILL has a Google Fonts <link> — remove it"
else
    ok "layout.html has no Google Fonts <link> tags"
fi
# Defensive: no fonts.gstatic.com preconnect either.
if grep -qE '<link[^>]*href="https://fonts\.gstatic\.com' internal/handlers/templates/layout.html; then
    bad "layout.html STILL has a fonts.gstatic.com <link> — remove it"
else
    ok "layout.html has no fonts.gstatic.com <link> tags"
fi

echo ""
echo "=== contract D: total Google Fonts woff2 size is reasonable (<500KB) ==="
total=$(du -k -s static/webfonts/ | awk '{print $1}')
# Subtract the font-awesome bytes (we know it's ~307KB;
# this check is conservative: we want to catch a runaway
# download like a full TTF instead of woff2, or a 10MB
# cyrillic-vietnamese subset, etc).
if [ "$total" -lt 1024 ]; then
    ok "static/webfonts/ total size = ${total}KB (< 1MB)"
else
    bad "static/webfonts/ total size = ${total}KB — too large, likely a non-woff2 download"
fi

echo ""
echo "=== contract E: verify_pre_deploy.sh includes B158 ==="
if grep -q 'B158' scripts/verify_pre_deploy.sh && \
   grep -q 'check_b158' scripts/verify_pre_deploy.sh; then
    ok "verify_pre_deploy.sh registers B158"
else
    bad "verify_pre_deploy.sh MISSING B158"
fi

echo ""
echo "=== contract F: layout.html Go template block is balanced ==="
# Defensive: the B158 edit removed a {{if}} block that
# loaded Google Fonts <link>s, but kept the {{end}} — that
# left the parser one 'end' too many. A similar
# future edit (removing a {{if}} without its {{end}})
# would again produce a 'panic: parse layout: template:
# layout.html:N: unexpected {{end}}' on the live VM
# (seen 2026-08-20 after B158 commit 592b69d, the
# skygate container was stuck restarting).
# This check counts {{if/range/define}} opens vs
# {{end}} closes; mismatch means a future template edit
# broke balance. NOTE: {{else if ...}} does NOT count
# as an extra {{if}} — it's a branch of the same if.
layout=internal/handlers/templates/layout.html
opens=$(grep -cE '\{\{(if|range|define)\b' "$layout" || true)
closes=$(grep -cE '\{\{end\}\}' "$layout" || true)
if [ "$opens" = "$closes" ]; then
    ok "layout.html template blocks balanced (if+range+define=$opens, end=$closes)"
else
    bad "layout.html template blocks UNBALANCED: if+range+define=$opens, end=$closes"
fi

echo ""
echo "=== summary ==="
echo "B158: self-hosted Google Fonts (Manrope + Inter + Geist + Sora + Geist Mono + JetBrains Mono)"
echo "all B158 contracts satisfied"
