#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.20.5 (B135) — Manrope font + +1px size bump
#
# Operator picked Manrope over Inter/Geist/Sora after looking
# at the font examples (2026-08-18). The pre-B135 UI was on
# Inter 14px body — "tech control panel" feel. Manrope is a
# warmer, more rounded humanist sans that reads more pleasant
# during long admin sessions.
#
# Two changes:
#   1. --font swapped from 'Inter' to 'Manrope' (Google Fonts
#      CDN, loaded in layout.html with preconnect + font-display:swap)
#   2. +1px size bump across the UI:
#      body 14→15, table 13→14, h2 22→24, button 13→14, h1
#      15→16, alert 13→14, tag 11→12, kbd 12→13, doc-card h3
#      16→17, line-height 1.55→1.6, --header-h 54→56
#
# What this script verifies (file-content contract, runs anywhere):
#   A. --font declares Manrope (was 'Inter', 'system-ui', ...)
#   B. body font-size is 15px (was 14px — pre-B135)
#   C. body line-height is 1.6 (was 1.55)
#   D. table font-size is 14px (was 13px)
#   E. title-row h2 is 24px (was 22px)
#   F. button font-size is 14px (was 13px)
#   G. layout.html references Manrope as a font option
#      (the B136 per-user display-prefs block lives here)
#   H. post-B158 self-hosted Manrope: themes.css has 4
#      @font-face rules for Manrope (400/500/600/700) AND
#      all 4 woff2 files exist in static/webfonts/. Replaces
#      the pre-B158 "fonts.gstatic.com preconnect" contract
#      that B158 deliberately removed.
#   I. B131 / B133 / B134 contracts still pass (we didn't break
#      the contrast bump or the wrapper-removal fix while
#      reflowing the typography)
#===============================================================================
set -uo pipefail

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/.." && pwd 2>/dev/null || echo "$SCRIPT_DIR/.." )"
THEMES_CSS="$PROJECT_ROOT/static/css/themes.css"
LAYOUT_HTML="$PROJECT_ROOT/internal/handlers/templates/layout.html"

PASS=0; FAIL=0; WARN=0
pass() { echo "  PASS  $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn() { echo "  WARN  $*"; WARN=$((WARN+1)); }

# Cross-platform grep: fall back to python if -P is missing (Windows)
USE_PYTHON_GREP=0
if ! echo "test" | grep -P "test" >/dev/null 2>&1; then
  USE_PYTHON_GREP=1
fi

# Pick a python that actually runs. On Windows Git Bash, 'python3'
# is a Microsoft Store stub that exits 1 with "Python was not found".
# 'python' resolves to the real install (C:/Python314/python.exe).
# On Linux, 'python3' is the real one. Probe so we don't trust
# `command -v`.
if [ "$USE_PYTHON_GREP" -eq 1 ]; then
  PY=""
  for candidate in python3 python py; do
    bin=$(command -v "${candidate}" 2>/dev/null) || continue
    out=$("${bin}" -c 'print("ok")' 2>/dev/null) || continue
    if [ "${out}" = "ok" ]; then
      PY="${bin}"
      break
    fi
  done
fi

pygrep() {
  local pattern="$1"
  local file="$2"
  if [ -z "${PY}" ]; then return 1; fi
  "${PY}" -c "
import sys,re
s=open(sys.argv[1],encoding='utf-8',errors='replace').read()
m=re.search(sys.argv[2], s, re.MULTILINE)
if m:
    print(m.group(0) if not m.groups() else m.group(1))
" "$file" "$pattern"
}

pygrep_count() {
  local pattern="$1"
  local file="$2"
  if [ -z "${PY}" ]; then echo 0; return; fi
  "${PY}" -c "
import sys,re
s=open(sys.argv[1],encoding='utf-8',errors='replace').read()
print(len(re.findall(sys.argv[2], s, re.MULTILINE)))
" "$file" "$pattern"
}

search_one() {
  local pattern="$1"
  local file="$2"
  if [ "$USE_PYTHON_GREP" -eq 1 ]; then
    pygrep "$pattern" "$file"
  else
    grep -oP -- "$pattern" "$file" 2>/dev/null | head -1
  fi
}

count_matches() {
  local pattern="$1"
  local file="$2"
  if [ "$USE_PYTHON_GREP" -eq 1 ]; then
    pygrep_count "$pattern" "$file" | tr -d '\n\r '
  else
    grep -cP -- "$pattern" "$file" 2>/dev/null | tr -d '\n\r ' || echo 0
  fi
}

echo "skygate root: $PROJECT_ROOT"
echo

echo "=== A. --font declares Manrope (was 'Inter' pre-B135) ==="
FONT_DECL=$(search_one '--font:[^;]+' "$THEMES_CSS" 2>/dev/null || true)
# Extract value (after :)
FONT_VAL=$(echo "$FONT_DECL" | sed -nE "s/^--font:(.*)$/\1/p")
if echo "$FONT_VAL" | grep -q "Manrope"; then
  pass "--font declares Manrope: $FONT_DECL"
else
  fail "--font does NOT declare Manrope: $FONT_DECL"
fi

echo
echo "=== B. body font-size is 15px (was 14px pre-B135) ==="
BODY_FS=$(search_one 'body\{[^}]*font-size:[0-9]+px' "$THEMES_CSS" | grep -oE 'font-size:[0-9]+px' | head -1)
if [ "$BODY_FS" = "font-size:15px" ]; then
  pass "body font-size is 15px (got: $BODY_FS)"
else
  fail "body font-size is NOT 15px (got: $BODY_FS)"
fi

echo
echo "=== C. body line-height is 1.6 (was 1.55 pre-B135) ==="
BODY_LH=$(search_one 'body\{[^}]*line-height:[0-9.]+' "$THEMES_CSS" | grep -oE 'line-height:[0-9.]+' | head -1)
if [ "$BODY_LH" = "line-height:1.6" ]; then
  pass "body line-height is 1.6 (got: $BODY_LH)"
else
  fail "body line-height is NOT 1.6 (got: $BODY_LH)"
fi

echo
echo "=== D. table font-size is 14px (was 13px pre-B135) ==="
TABLE_FS=$(search_one '^table\{[^}]*font-size:[0-9]+px' "$THEMES_CSS" | grep -oE 'font-size:[0-9]+px' | head -1)
if [ "$TABLE_FS" = "font-size:14px" ]; then
  pass "table font-size is 14px (got: $TABLE_FS)"
else
  fail "table font-size is NOT 14px (got: $TABLE_FS)"
fi

echo
echo "=== E. title-row h2 is 24px (was 22px pre-B135) ==="
H2_FS=$(search_one 'title-row h2\{[^}]*font-size:[0-9]+px' "$THEMES_CSS" | grep -oE 'font-size:[0-9]+px' | head -1)
if [ "$H2_FS" = "font-size:24px" ]; then
  pass "title-row h2 is 24px (got: $H2_FS)"
else
  fail "title-row h2 is NOT 24px (got: $H2_FS)"
fi

echo
echo "=== F. button font-size is 14px (was 13px pre-B135) ==="
BTN_FS=$(search_one 'btn\{display[^}]*font-size:[0-9]+px' "$THEMES_CSS" | grep -oE 'font-size:[0-9]+px' | head -1)
if [ "$BTN_FS" = "font-size:14px" ]; then
  pass "button font-size is 14px (got: $BTN_FS)"
else
  fail "button font-size is NOT 14px (got: $BTN_FS)"
fi

echo
echo "=== G. layout.html has the B136 per-user font override block ==="
# Pre-B158 this contract was "layout.html includes the
# Manrope Google Fonts <link>" — obsolete after B158. Post-
# B158 the layout still wires up the font choice via the
# B136 <style id='user-display-prefs'> block, where the
# DisplayFont value (one of system/inter/geist/sora) is
# resolved to the matching --font declaration. Manrope
# itself is the DEFAULT in themes.css (no per-user block
# needed) — the B136 block only overrides to Inter/Geist/
# Sora/System when the user picks one.
N=$(count_matches 'DisplayFont' "$LAYOUT_HTML")
if [ "$N" -ge 1 ]; then
  pass "layout.html has the B136 DisplayFont override block (count: $N)"
else
  fail "layout.html does NOT have the B136 DisplayFont block"
fi

echo
echo "=== H. post-B158 self-hosted Manrope (themes.css @font-face + static/webfonts/ woff2) ==="
# B158 (v1.5.0) moved all 6 fonts off fonts.googleapis.com
# into /static/webfonts/. The B135 contract that pinned the
# Google Fonts preconnect is obsolete — B158 removed it on
# purpose (operator reported 'net::ERR_CONNECTION_TIMED_OUT'
# on 2026-08-20). The post-B158 contract that pins "Manrope
# still renders correctly" is: themes.css has 4 @font-face
# rules for Manrope (400/500/600/700) AND all 4 woff2 files
# exist in static/webfonts/. We check both halves here; B158
# check_b158.sh pins the broader contract across all 6 fonts.
N=$(count_matches "font-family:'Manrope'" "$THEMES_CSS")
if [ "$N" -ge 4 ]; then
  pass "themes.css has $N @font-face rules for Manrope (need at least 4 for 400/500/600/700)"
else
  fail "themes.css has only $N Manrope @font-face rules (need ≥4)"
fi
N_WOFF2=0
for w in 400 500 600 700; do
  [ -f "$PROJECT_ROOT/static/webfonts/manrope-latin-$w-normal.woff2" ] && N_WOFF2=$((N_WOFF2+1))
done
if [ "$N_WOFF2" -eq 4 ]; then
  pass "static/webfonts/ has all 4 Manrope woff2 files (400/500/600/700)"
else
  fail "static/webfonts/ is missing Manrope woff2 files (have $N_WOFF2/4)"
fi

echo
echo "=== I. B131 + B133 + B134 contracts still pass ==="
OVERALL=0
if [ -x "$SCRIPT_DIR/check_b131.sh" ]; then
  bash "$SCRIPT_DIR/check_b131.sh" >/dev/null 2>&1 && pass "check_b131.sh PASS — contrast bump intact" || { fail "check_b131.sh FAIL"; OVERALL=1; }
else
  warn "check_b131.sh missing — skipping"
fi
if [ -x "$SCRIPT_DIR/check_b133.sh" ]; then
  bash "$SCRIPT_DIR/check_b133.sh" >/dev/null 2>&1 && pass "check_b133.sh PASS — dramatic overhaul intact" || { fail "check_b133.sh FAIL"; OVERALL=1; }
else
  warn "check_b133.sh missing — skipping"
fi
if [ -x "$SCRIPT_DIR/check_b134.sh" ]; then
  bash "$SCRIPT_DIR/check_b134.sh" >/dev/null 2>&1 && pass "check_b134.sh PASS — wrapper removal intact" || { fail "check_b134.sh FAIL"; OVERALL=1; }
else
  warn "check_b134.sh missing — skipping"
fi

echo
echo "=== B135 summary ==="
echo "  PASS: $PASS"
echo "  FAIL: $FAIL"
echo "  WARN: $WARN"

if [ "$FAIL" -gt 0 ]; then
  echo
  echo "B135 contracts FAILED."
  exit 1
fi

if [ "$WARN" -gt 0 ]; then
  echo
  echo "B135 contracts passed with warnings."
  exit 0
fi

echo
echo "B135 contracts all hold."
exit 0
