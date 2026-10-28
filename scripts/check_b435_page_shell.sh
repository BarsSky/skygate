#!/usr/bin/env bash
# check_b435_page_shell.sh — B435 (2026-10-28)
#
# Two operator reports, measured before the fix:
#
#   1. «в строке где у браузера табы страниц висит title.* вместо описания и в мобильных
#      версиях не отображается там иконка страницы» — the browser tab showed the RAW KEY
#      `title.admin_cluster` (it was returned by pageTitle and defined in NEITHER catalogue),
#      and 34 of 59 templates had no pageTitle case at all, so their tab said the generic
#      "Skygate"; separately the panel shipped ONE icon (SVG) and pointed `/favicon.ico` at
#      it with `image/svg+xml`, so a browser asking for an ICO got a document it will not
#      render — which is why a phone showed no icon, and `apple-touch-icon` pointed at an SVG
#      iOS does not accept.
#   2. «на маленьком экране … меню и футтер наплывают друг на друга» — `<footer>` is a
#      SIBLING of `<main>`, so the desktop `main{margin-left:220px}` never applied to it and
#      its left half was painted under the fixed sidebar.
#
# CONTRACTS (12)

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1
PASS=0; FAIL=0; SKIP=0
ok()  { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad() { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

HANDLERS=internal/handlers/handlers.go
CAT=internal/i18n/catalog_common.go
LAYOUT=internal/handlers/templates/layout.html
CSS=internal/staticfs/static/css/themes.css
STATIC=internal/handlers/static.go
GATE=scripts/verify_pre_deploy.sh
for f in "$HANDLERS" "$CAT" "$LAYOUT" "$CSS" "$STATIC" "$GATE"; do
  [ -f "$f" ] || { bad "missing source file: $f"; exit 1; }
done
# B339: the cmd/skygate route is read as a SURFACE (the whole package) so a refactor that
# moves the wiring cannot turn this contract into a false failure.
SKY_SRC="$(cat cmd/skygate/*.go 2>/dev/null)"

hdr "A. the tab title names the page, in both languages"
if OUT="$(go test ./internal/handlers/ -run B435 -count=1 2>&1)"; then
  ok "A1: every embedded template maps to a non-generic title key that exists in RU and EN"
else
  bad "A1: go test -run B435 failed: $(printf '%s' "$OUT" | tail -4 | tr '\n' ' ')"
fi
if grep -qF 'func TestB435_EveryPageHasATranslatedTabTitle' internal/handlers/page_title_b435_test.go 2>/dev/null; then
  ok "A2: the regression test exists (it asserts the RESULT, not the switch)"
else
  bad "A2: the B435 title test is missing"
fi
# The specific defect that produced a RAW key: a key pageTitle returns but nobody defines.
missing=""
keys="$(sed -n '/^func pageTitle/,/^}/p' "$HANDLERS" | grep -oE 'return "[a-z0-9_.]+"' | sed 's/return "//;s/"//' | sort -u)"
for k in $keys; do
  n=$(grep -rho "\"$k\":" internal/i18n/catalog_*.go | wc -l | tr -d ' ')
  [ "$n" = "2" ] || missing="$missing $k($n)"
done
if [ -z "$missing" ]; then
  ok "A3: every key pageTitle can return is defined in BOTH catalogues (no raw key in any tab)"
else
  bad "A3: key(s) returned by pageTitle but missing from a catalogue:$missing"
fi
if grep -qF '"title.admin_cluster":' "$CAT"; then
  ok "A4: title.admin_cluster exists (it was the key the operator saw raw)"
else
  bad "A4: title.admin_cluster is still undefined"
fi

hdr "B. the mobile icon is a real icon"
for f in internal/staticfs/static/favicon.ico internal/staticfs/static/favicon-180.png internal/staticfs/static/favicon-192.png internal/staticfs/static/favicon-32.png; do
  [ -s "$f" ] || { bad "B1: $f is missing or empty"; continue; }
  ok "B1: $(basename "$f") present ($(wc -c < "$f" | tr -d ' ') bytes)"
done
# A PNG must start with the 8-byte signature; an ICO with 00 00 01 00.
if [ "$(head -c 8 internal/staticfs/static/favicon-180.png | od -An -tx1 | tr -d ' \n')" = "89504e470d0a1a0a" ]; then
  ok "B2: favicon-180.png carries the PNG signature (not a renamed SVG)"
else
  bad "B2: favicon-180.png is not a PNG"
fi
if [ "$(head -c 4 internal/staticfs/static/favicon.ico | od -An -tx1 | tr -d ' \n')" = "00000100" ]; then
  ok "B3: favicon.ico is a real ICO container (type=1, count=1)"
else
  bad "B3: favicon.ico is not an ICO"
fi
case "$SKY_SRC" in
  *'mux.HandleFunc("/favicon.ico", app.FaviconICOHandler)'*)
    ok "B4: /favicon.ico is served as an ICO, not as the SVG with an svg content type"
    ;;
  *)
    bad "B4: /favicon.ico still points at FaviconHandler (the SVG) — a browser asking for an ICO gets a document it will not render"
    ;;
esac
if grep -qF 'rel="apple-touch-icon" sizes="180x180" href="/static/favicon-180.png"' "$LAYOUT"; then
  ok "B5: apple-touch-icon points at a PNG (iOS ignores an SVG there)"
else
  bad "B5: apple-touch-icon is not a PNG"
fi

hdr "C. the footer is not painted under the sidebar"
if grep -qE '^footer\{margin-left:220px' "$CSS"; then
  ok "C1: the footer carries the same 220px offset as <main> on desktop"
else
  bad "C1: the footer has no desktop offset — its left half sits under the fixed sidebar («меню и футтер наплывают»)"
fi
if awk '/@media \(max-width:768px\)/{m=1} m && /footer\{margin-left:0 !important/{found=1} END{exit(found?0:1)}' "$CSS"; then
  ok "C2: the mobile block resets the footer offset (the sidebar is an overlay drawer there)"
else
  bad "C2: the mobile block does not reset the footer offset"
fi
if grep -qE '^main\{margin-left:220px' "$CSS"; then
  ok "C3: <main> keeps its desktop offset (the footer now follows the same column)"
else
  bad "C3: <main> lost its desktop offset"
fi

printf '\n\033[1mB435 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
