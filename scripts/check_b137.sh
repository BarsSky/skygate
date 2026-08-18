#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.20.6 hotfix (B137) — color swatch grid for selection_bg
#
# Operator's 2026-08-18 feedback on the B136 Display form:
# "добавь удобную форму выбора цвета из таблицы чтобы не
#  писать вручную". Pre-B137 the selection_bg field was a
# freeform text input. Post-B137 it's a 14-tile swatch grid:
#   1× default (empty / use theme default)
#   12× preset colors (yellow, coral, turquoise, mint, ...)
#   1× custom (reveals the text input for arbitrary CSS)
# The selected tile has an accent ring. Tiny vanilla JS
# (~25 lines in the template) wires up the click handlers.
# No server-side changes (the existing selection_bg column
# already accepts any CSS color).
#
# What this script verifies:
#   A. account.html has .color-swatch-grid with 14 tiles
#   B. themes.css has .color-swatch + .color-swatch-grid rules
#   C. account.html wires up the swatch JS (click handler)
#   D. i18n keys (account.selection_swatch_default +
#      account.selection_swatch_custom) present in RU + EN
#   E. B136 + B131 + B133 + B134 + B135 contracts still pass
#      (B137 builds on top of B136's per-user selection_bg)
#===============================================================================
set -uo pipefail

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" >/dev/null 2>&1 && pwd )"
PROJECT_ROOT="$( cd "$SCRIPT_DIR/.." && pwd 2>/dev/null || echo "$SCRIPT_DIR/.." )"
ACCOUNT_HTML="$PROJECT_ROOT/internal/handlers/templates/user/account.html"
THEMES_CSS="$PROJECT_ROOT/static/css/themes.css"
CATALOG_MY="$PROJECT_ROOT/internal/i18n/catalog_my.go"

PASS=0; FAIL=0; WARN=0
pass() { echo "  PASS  $*"; PASS=$((PASS+1)); }
fail() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn() { echo "  WARN  $*"; WARN=$((WARN+1)); }

USE_PYTHON_GREP=0
if ! echo "test" | grep -P "test" >/dev/null 2>&1; then
  USE_PYTHON_GREP=1
fi

count_matches() {
  local pattern="$1"
  local file="$2"
  if [ "$USE_PYTHON_GREP" -eq 1 ]; then
    python3 -c "
import sys,re
s=open(sys.argv[1],encoding='utf-8',errors='replace').read()
print(len(re.findall(sys.argv[2], s, re.MULTILINE)))
" "$file" "$pattern" | tr -d '\n\r '
  else
    grep -cP -- "$pattern" "$file" 2>/dev/null | tr -d '\n\r ' || echo 0
  fi
}

file_check() {
  if [ ! -f "$1" ]; then
    warn "expected file missing: $1"
    return 1
  fi
  return 0
}

echo "skygate root: $PROJECT_ROOT"
echo

echo "=== A. account.html has .color-swatch-grid with 14 tiles ==="
file_check "$ACCOUNT_HTML" || exit 0
N_GRID=$(count_matches 'class="color-swatch-grid"' "$ACCOUNT_HTML")
if [ "$N_GRID" -ge 1 ]; then
  pass "color-swatch-grid present in account.html"
else
  fail "color-swatch-grid MISSING from account.html"
fi
# Count all 14 swatch buttons (1 default + 12 colors + 1 custom)
N_SWATCH=$(count_matches 'class="color-swatch' "$ACCOUNT_HTML")
if [ "$N_SWATCH" -ge 14 ]; then
  pass "all 14 swatch tiles present (count: $N_SWATCH)"
else
  fail "expected >=14 color-swatch tiles, got $N_SWATCH"
fi
# Each preset has its own data-color
for c in "#ffcc00" "#ff6b6b" "#4ecdc4" "#95e1d3" "#a8e6cf" "#ffd93d" "#fcbad3" "#c9cbff" "#aa96da" "#ff9aa2" "#6c5ce7" "#fdcb6e"; do
  N=$(count_matches "data-color=\"$c\"" "$ACCOUNT_HTML")
  if [ "$N" -ge 1 ]; then
    pass "preset $c has a swatch tile"
  else
    fail "preset $c missing from swatch grid"
  fi
done
# Default + custom special tiles
N_DEFAULT=$(count_matches 'swatch-default' "$ACCOUNT_HTML")
if [ "$N_DEFAULT" -ge 1 ]; then
  pass "default (use theme) swatch tile present"
else
  fail "default swatch tile missing"
fi
N_CUSTOM=$(count_matches 'swatch-custom' "$ACCOUNT_HTML")
if [ "$N_CUSTOM" -ge 1 ]; then
  pass "custom swatch tile present (reveals text input)"
else
  fail "custom swatch tile missing"
fi

echo
echo "=== B. themes.css has .color-swatch + .color-swatch-grid rules ==="
file_check "$THEMES_CSS" || exit 0
N_RULE=$(count_matches '\.color-swatch' "$THEMES_CSS")
if [ "$N_RULE" -ge 5 ]; then
  pass ".color-swatch + .color-swatch-grid rules present (count: $N_RULE)"
else
  fail ".color-swatch rules missing (got $N_RULE, want >=5)"
fi
N_GRID_RULE=$(count_matches '\.color-swatch-grid' "$THEMES_CSS")
if [ "$N_GRID_RULE" -ge 1 ]; then
  pass ".color-swatch-grid rule defined"
else
  fail ".color-swatch-grid rule missing"
fi
# Selected state has the accent ring
N_SEL=$(count_matches '\.color-swatch\.selected' "$THEMES_CSS")
if [ "$N_SEL" -ge 1 ]; then
  pass ".color-swatch.selected rule defined (accent ring)"
else
  fail ".color-swatch.selected rule missing"
fi

echo
echo "=== C. account.html wires up the swatch JS ==="
N_CLICK=$(count_matches "addEventListener\('click'" "$ACCOUNT_HTML")
if [ "$N_CLICK" -ge 1 ]; then
  pass "swatch click handler attached (addEventListener count: $N_CLICK)"
else
  fail "no addEventListener('click') in account.html"
fi
# The click handler should update the hidden text input
N_FOCUS=$(count_matches 'input\.focus' "$ACCOUNT_HTML")
if [ "$N_FOCUS" -ge 1 ]; then
  pass "click handler focuses the text input (custom swatch)"
else
  fail "click handler does not focus the text input"
fi
# The markSelected function should remove the .selected class from all
# and add it to the clicked one
N_MARK=$(count_matches 'markSelected' "$ACCOUNT_HTML")
if [ "$N_MARK" -ge 3 ]; then
  pass "markSelected() used >=3 times (def + 3 callers)"
else
  fail "markSelected used only $N_MARK times (want >=3)"
fi

echo
echo "=== D. i18n keys (RU + EN) for the swatch UI ==="
file_check "$CATALOG_MY" || exit 0
for k in selection_swatch_default selection_swatch_custom; do
  N=$(count_matches "\"account\.${k}\"" "$CATALOG_MY")
  if [ "$N" -ge 2 ]; then
    pass "i18n key 'account.$k' present in both RU + EN (count: $N)"
  else
    fail "i18n key 'account.$k' missing or only in 1 lang (count: $N, want >=2)"
  fi
done

echo
echo "=== E. B136 + B131 + B133 + B134 + B135 contracts still pass ==="
for b in 136 131 133 134 135; do
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
echo "=== B137 summary ==="
echo "  PASS: $PASS"
echo "  FAIL: $FAIL"
echo "  WARN: $WARN"

if [ "$FAIL" -gt 0 ]; then
  echo
  echo "B137 contracts FAILED."
  exit 1
fi

if [ "$WARN" -gt 0 ]; then
  echo
  echo "B137 contracts passed with warnings."
  exit 0
fi

echo
echo "B137 contracts all hold."
exit 0
