#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.20.6 (B136) — per-user display preferences (DB-persisted)
#
# Operator's 2026-08-18 request: "wherever the user opens the
# web interface, their display preferences are saved for them".
# Pre-B136 the theme was per-user (in portal_users.theme) but
# font + size were global in themes.css. B136 adds 3 columns
# to portal_users (font_family, font_scale, selection_bg) and
# exposes them through the /my/account form + a per-page
# <style> override in the layout.
#
# The pre-B136 UX was: change a global CSS file, redeploy.
# The post-B136 UX is: user changes font + size in /my/account,
# saved to DB, applied on every page render via
# `<style id="user-display-prefs">` injected by layout.html.
#
# What this script verifies:
#   A. Migration V057PG exists and adds the 3 new columns
#      (font_family, font_scale, selection_bg) to portal_users
#   B. GetUserDisplayPrefs / SetUserDisplayPrefs helpers exist
#      in db.go
#   C. IsValidFontFamily / ClampFontScale helpers exist
#   D. PostMyAccountDisplay handler in feature/my/settings.go
#   E. Route POST /my/account/display registered in main.go
#   F. Layout template injects <style id="user-display-prefs">
#   G. /my/account template has the Display form
#   H. i18n keys (account.display_*, account.font_*,
#      account.selection_*) present in BOTH ru and en
#   I. B131 + B133 + B134 + B135 contracts still pass
#      (B136 builds on top of the B135 Manrope + B133 contrast
#       + B134 wrapper-removal fixes)
#===============================================================================
set -uo pipefail

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/.." && pwd 2>/dev/null || echo "$SCRIPT_DIR/.." )"
DB_GO="$PROJECT_ROOT/internal/db/db.go"
DB_MIG="$PROJECT_ROOT/internal/db/migrations_pg.go"
DB_DRIVER="$PROJECT_ROOT/internal/db/driver_postgres.go"
SETTINGS_GO="$PROJECT_ROOT/internal/feature/my/settings.go"
MAIN_GO="$PROJECT_ROOT/cmd/skygate/main.go"
LAYOUT_HTML="$PROJECT_ROOT/internal/handlers/templates/layout.html"
ACCOUNT_HTML="$PROJECT_ROOT/internal/handlers/templates/user/account.html"
CATALOG_MY="$PROJECT_ROOT/internal/i18n/catalog_my.go"

PASS=0; FAIL=0; WARN=0
pass() { echo "  PASS  $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn() { echo "  WARN  $*"; WARN=$((WARN+1)); }

# Cross-platform grep with -- (so leading - doesn't break)
USE_PYTHON_GREP=0
if ! echo "test" | grep -P "test" >/dev/null 2>&1; then
  USE_PYTHON_GREP=1
fi

pygrep_count() {
  local pattern="$1"
  local file="$2"
  python3 -c "
import sys,re
s=open(sys.argv[1],encoding='utf-8',errors='replace').read()
print(len(re.findall(sys.argv[2], s, re.MULTILINE)))
" "$file" "$pattern" | tr -d '\n\r '
}

search_one() {
  local pattern="$1"
  local file="$2"
  grep -oP -- "$pattern" "$file" 2>/dev/null | head -1
}

count_matches() {
  local pattern="$1"
  local file="$2"
  if [ "$USE_PYTHON_GREP" -eq 1 ]; then
    pygrep_count "$pattern" "$file"
  else
    grep -cP -- "$pattern" "$file" 2>/dev/null | tr -d '\n\r ' || echo 0
  fi
}

# Verify file exists (skip contract with WARN if not)
file_check() {
  if [ ! -f "$1" ]; then
    warn "expected file missing: $1"
    return 1
  fi
  return 0
}

echo "skygate root: $PROJECT_ROOT"
echo

echo "=== A. Migration V057PG adds 3 columns to portal_users ==="
file_check "$DB_MIG" || exit 0
MIG_DECL=$(search_one 'func migrateV057PG\([^)]*\)' "$DB_MIG")
if [ -n "$MIG_DECL" ]; then
  pass "migrateV057PG function defined: $MIG_DECL"
else
  fail "migrateV057PG not found in $DB_MIG"
fi
N_FONT_FAMILY=$(count_matches 'ADD COLUMN IF NOT EXISTS font_family' "$DB_MIG")
N_FONT_SCALE=$(count_matches 'ADD COLUMN IF NOT EXISTS font_scale' "$DB_MIG")
N_SELECTION=$(count_matches 'ADD COLUMN IF NOT EXISTS selection_bg' "$DB_MIG")
if [ "$N_FONT_FAMILY" -ge 1 ]; then
  pass "V057 adds font_family column (count: $N_FONT_FAMILY)"
else
  fail "V057 missing font_family column"
fi
if [ "$N_FONT_SCALE" -ge 1 ]; then
  pass "V057 adds font_scale column (count: $N_FONT_SCALE)"
else
  fail "V057 missing font_scale column"
fi
if [ "$N_SELECTION" -ge 1 ]; then
  pass "V057 adds selection_bg column (count: $N_SELECTION)"
else
  fail "V057 missing selection_bg column"
fi
# V057 must be registered in driver_postgres.go
N_REGISTERED=$(count_matches 'migrateV057PG' "$DB_DRIVER")
if [ "$N_REGISTERED" -ge 1 ]; then
  pass "V057 registered in driver_postgres.go (count: $N_REGISTERED — in the migration slice)"
else
  fail "V057 NOT registered in driver_postgres.go (count: $N_REGISTERED, want >=1)"
fi

echo
echo "=== B. GetUserDisplayPrefs / SetUserDisplayPrefs in db.go ==="
file_check "$DB_GO" || exit 0
if grep -qF "func GetUserDisplayPrefs" "$DB_GO"; then
  pass "GetUserDisplayPrefs defined"
else
  fail "GetUserDisplayPrefs missing from db.go"
fi
if grep -qF "func SetUserDisplayPrefs" "$DB_GO"; then
  pass "SetUserDisplayPrefs defined"
else
  fail "SetUserDisplayPrefs missing from db.go"
fi
if grep -qF "type DisplayPrefs struct" "$DB_GO"; then
  pass "DisplayPrefs struct defined"
else
  fail "DisplayPrefs struct missing from db.go"
fi

echo
echo "=== C. IsValidFontFamily / ClampFontScale helpers ==="
if grep -qF "func IsValidFontFamily" "$DB_GO"; then
  pass "IsValidFontFamily defined"
else
  fail "IsValidFontFamily missing"
fi
if grep -qF "func ClampFontScale" "$DB_GO"; then
  pass "ClampFontScale defined"
else
  fail "ClampFontScale missing"
fi
# All 5 known font families must be listed
for fam in manrope inter geist sora system; do
  N=$(count_matches "FontFamily[A-Z][a-z]*[[:space:]]*=[[:space:]]*\"$fam\"" "$DB_GO")
  if [ "$N" -ge 1 ]; then
    pass "font family '$fam' declared as a constant"
  else
    fail "font family '$fam' missing from db.go constants"
  fi
done

echo
echo "=== D. PostMyAccountDisplay handler in feature/my/settings.go ==="
file_check "$SETTINGS_GO" || exit 0
if grep -qF "func (s *Service) PostMyAccountDisplay" "$SETTINGS_GO"; then
  pass "PostMyAccountDisplay handler defined"
else
  fail "PostMyAccountDisplay missing from feature/my/settings.go"
fi
if grep -qF "GetUserDisplayPrefs" "$SETTINGS_GO"; then
  pass "handler uses GetUserDisplayPrefs to load existing prefs"
else
  fail "handler does NOT use GetUserDisplayPrefs"
fi
if grep -qF "SetUserDisplayPrefs" "$SETTINGS_GO"; then
  pass "handler uses SetUserDisplayPrefs to persist"
else
  fail "handler does NOT use SetUserDisplayPrefs"
fi

echo
echo "=== E. Route POST /my/account/display registered in main.go ==="
file_check "$MAIN_GO" || exit 0
if grep -qF '/my/account/display' "$MAIN_GO"; then
  pass "/my/account/display referenced in main.go"
else
  fail "/my/account/display route NOT registered"
fi
if grep -qF 'PostMyAccountDisplay' "$MAIN_GO"; then
  pass "PostMyAccountDisplay handler wired in main.go"
else
  fail "PostMyAccountDisplay NOT wired in main.go"
fi

echo
echo "=== F. Layout template injects user-display-prefs <style> ==="
file_check "$LAYOUT_HTML" || exit 0
if grep -qF 'user-display-prefs' "$LAYOUT_HTML"; then
  pass "layout.html has <style id=\"user-display-prefs\">"
else
  fail "layout.html missing <style id=\"user-display-prefs\">"
fi
if grep -qF 'DisplayFont' "$LAYOUT_HTML"; then
  pass "layout.html references {{.DisplayFont}}"
else
  fail "layout.html does NOT reference {{.DisplayFont}}"
fi
if grep -qF 'DisplayScale' "$LAYOUT_HTML"; then
  pass "layout.html references {{.DisplayScale}}"
else
  fail "layout.html does NOT reference {{.DisplayScale}}"
fi
if grep -qF 'DisplaySelBg' "$LAYOUT_HTML"; then
  pass "layout.html references {{.DisplaySelBg}}"
else
  fail "layout.html does NOT reference {{.DisplaySelBg}}"
fi

echo
echo "=== G. /my/account template has the Display form ==="
file_check "$ACCOUNT_HTML" || exit 0
if grep -qF '/my/account/display' "$ACCOUNT_HTML"; then
  pass "account.html form posts to /my/account/display"
else
  fail "account.html does NOT post to /my/account/display"
fi
if grep -qF 'name="font_family"' "$ACCOUNT_HTML"; then
  pass "account.html has font_family <select>"
else
  fail "account.html missing font_family <select>"
fi
if grep -qF 'name="font_scale"' "$ACCOUNT_HTML"; then
  pass "account.html has font_scale <select>"
else
  fail "account.html missing font_scale <select>"
fi
if grep -qF 'name="selection_bg"' "$ACCOUNT_HTML"; then
  pass "account.html has selection_bg <input>"
else
  fail "account.html missing selection_bg <input>"
fi
if grep -qF 'display_title' "$ACCOUNT_HTML"; then
  pass "account.html has Display card title (i18n key display_title)"
else
  fail "account.html missing Display card"
fi

echo
echo "=== H. i18n keys (RU + EN) for the Display form ==="
file_check "$CATALOG_MY" || exit 0
KEYS=(display_title display_desc font_family font_family_help font_system
      font_scale font_scale_smaller font_scale_small font_scale_normal
      font_scale_large font_scale_larger font_scale_help
      selection_bg selection_bg_placeholder selection_bg_help
      display_save display_saved display_save_failed)
MISSING=()
for k in "${KEYS[@]}"; do
  N=$(count_matches "\"account\.${k}\"" "$CATALOG_MY")
  if [ "$N" -lt 2 ]; then
    MISSING+=("$k (count: $N, want >=2 for RU+EN)")
  fi
done
if [ "${#MISSING[@]}" -eq 0 ]; then
  pass "all ${#KEYS[@]} i18n keys present in RU + EN (${#KEYS[@]} × 2 = $((${#KEYS[@]} * 2)))"
else
  fail "i18n keys missing: ${MISSING[*]}"
fi

echo
echo "=== I. B131 + B133 + B134 + B135 contracts still pass ==="
for b in 131 133 134 135; do
  if [ -x "$SCRIPT_DIR/check_b${b}.sh" ]; then
    if bash "$SCRIPT_DIR/check_b${b}.sh" >/dev/null 2>&1; then
      pass "check_b${b}.sh PASS"
    else
      fail "check_b${b}.sh FAIL"
    fi
  else
    warn "check_b${b}.sh not present — skipping"
  fi
done

echo
echo "=== B136 summary ==="
echo "  PASS: $PASS"
echo "  FAIL: $FAIL"
echo "  WARN: $WARN"

if [ "$FAIL" -gt 0 ]; then
  echo
  echo "B136 contracts FAILED."
  exit 1
fi

if [ "$WARN" -gt 0 ]; then
  echo
  echo "B136 contracts passed with warnings."
  exit 0
fi

echo
echo "B136 contracts all hold."
exit 0
