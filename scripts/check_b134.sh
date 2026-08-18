#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.20.4 (B134) — themes.css must NOT have <style> / </style> wrappers
#
# The operator reported on 2026-08-18: "тема все еще не отображает
# корректно таблицы отсутствуют видимые границы тоже самое по формам
# окнам и также выделениям ничего нет, выглядит так словно сам css е
# применился". Root cause: the file `static/css/themes.css` was
# created from an HTML template (v1.0.0 squashed release) and
# retained the surrounding `<style>...</style>` tags. The browser
# would silently drop the CSS rules that appeared AFTER the
# `</style>` close tag (lines 761-end, ~80 lines of CSS for the
# user form grid, password reset form, and other post-v1.0
# additions). The remaining CSS that the browser DID parse was
# also at risk of being misinterpreted depending on the engine.
#
# B134 fix: remove the two wrapper tags. Pure file-content fix.
# Backed by 5 contracts that pin the no-wrappers invariant.
#
# What this script verifies:
#   A. No `<style>` tag in themes.css (line 1 was the offender)
#   B. No `</style>` tag in themes.css (line 760 was the offender)
#   C. The file starts with a CSS comment `/* ====...` (the
#      original `<style>` was followed by this exact comment, so
#      removing `<style>` leaves the comment as the new line 1)
#   D. B131 contracts still pass (we didn't break the contrast
#      bump by removing the wrappers)
#   E. B133 contracts still pass (we didn't break the dramatic
#      overhaul by removing the wrappers)
#
# This is a file-content contract — runs anywhere (Windows, Linux,
# WSL), no VM required.
#===============================================================================
set -uo pipefail

# Resolve project root (B134 runs from anywhere; resolve relative to script)
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
# scripts/ is one level below project root in skygate
PROJECT_ROOT="$( cd "$SCRIPT_DIR/.." && pwd 2>/dev/null || echo "$SCRIPT_DIR/.." )"
THEMES_CSS="$PROJECT_ROOT/static/css/themes.css"

# Cross-platform grep (avoid `grep -P` which is missing on some Windows greps)
USE_PYTHON_GREP=0
if ! echo "test" | grep -P "test" >/dev/null 2>&1; then
  USE_PYTHON_GREP=1
fi

match_count() {
  local pattern="$1"
  local file="$2"
  if [ "$USE_PYTHON_GREP" -eq 1 ]; then
    python3 -c "import sys,re; s=open(sys.argv[1],encoding='utf-8',errors='replace').read(); print(len(re.findall(sys.argv[2], s, re.MULTILINE)))" "$file" "$pattern" | tr -d '\n\r '
  else
    grep -cP "$pattern" "$file" 2>/dev/null | tr -d '\n\r ' || echo 0
  fi
}

match_exists() {
  local pattern="$1"
  local file="$2"
  local n
  n=$(match_count "$pattern" "$file")
  [ "$n" -gt 0 ]
}

PASS=0
FAIL=0
WARN=0
fail() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
pass() { echo "  PASS  $*"; PASS=$((PASS+1)); }
warn() { echo "  WARN  $*"; WARN=$((WARN+1)); }

echo "skygate root: $PROJECT_ROOT"
echo
echo "=== A. No '<style>' tag in themes.css ==="
COUNT_OPEN=$(match_count '<style>' "$THEMES_CSS")
if [ "$COUNT_OPEN" -eq 0 ]; then
  pass "No <style> opening tag in themes.css (was on line 1 in v1.0.0 - v1.3.20.3)"
else
  fail "Found $COUNT_OPEN '<style>' tag(s) in themes.css — CSS files must not be HTML-wrapped"
fi

echo
echo "=== B. No '</style>' tag in themes.css ==="
COUNT_CLOSE=$(match_count '</style>' "$THEMES_CSS")
if [ "$COUNT_CLOSE" -eq 0 ]; then
  pass "No </style> closing tag in themes.css (was on line 760 in v1.0.0 - v1.3.20.3, which was orphaning the post-tag CSS)"
else
  fail "Found $COUNT_CLOSE '</style>' tag(s) in themes.css — CSS files must not be HTML-wrapped"
fi

echo
echo "=== C. themes.css starts with a CSS comment (preserves the original preamble) ==="
FIRST_LINE=$(head -1 "$THEMES_CSS" 2>/dev/null || echo "")
if [[ "$FIRST_LINE" == /* ]]; then
  pass "themes.css line 1 is a CSS comment (got: ${FIRST_LINE:0:50}...)"
else
  fail "themes.css line 1 is not a CSS comment — got: '$FIRST_LINE'"
fi

echo
echo "=== D. B131 contract still passes (contrast bump intact after wrapper removal) ==="
if [ -x "$SCRIPT_DIR/check_b131.sh" ]; then
  if bash "$SCRIPT_DIR/check_b131.sh" >/dev/null 2>&1; then
    pass "check_b131.sh PASS — contrast bump not broken by wrapper removal"
  else
    fail "check_b131.sh FAIL — wrapper removal may have broken the contrast bump"
  fi
else
  warn "check_b131.sh not present — skipping contract D"
fi

echo
echo "=== E. B133 contract still passes (dramatic overhaul intact after wrapper removal) ==="
if [ -x "$SCRIPT_DIR/check_b133.sh" ]; then
  if bash "$SCRIPT_DIR/check_b133.sh" >/dev/null 2>&1; then
    pass "check_b133.sh PASS — dramatic contrast overhaul not broken by wrapper removal"
  else
    fail "check_b133.sh FAIL — wrapper removal may have broken the B133 colors"
  fi
else
  warn "check_b133.sh not present — skipping contract E"
fi

echo
echo "=== B134 summary ==="
echo "  PASS: $PASS"
echo "  FAIL: $FAIL"
echo "  WARN: $WARN"

if [ "$FAIL" -gt 0 ]; then
  echo
  echo "B134 contracts FAILED — themes.css has wrapper tags or contracts regressed."
  exit 1
fi

if [ "$WARN" -gt 0 ]; then
  echo
  echo "B134 contracts passed with warnings."
  exit 0
fi

echo
echo "B134 contracts all hold."
exit 0
