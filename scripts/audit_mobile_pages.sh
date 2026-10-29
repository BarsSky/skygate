#!/usr/bin/env bash
# scripts/audit_mobile_pages.sh — B435.1 audit (read-only).
#
# Reports the three patterns that break the panel on a 360–390 px screen, per template:
#   T  a <table> that is NOT inside .table-wrap AND NOT inside .card
#      (body{overflow-x:hidden} clips it, so the right-hand columns are simply lost);
#   W  an inline fixed width >= 400px (BEFORE `min-width`/`max-width` are excluded,
#      because those are the responsive forms);
#   N  white-space:nowrap on a <td>/<th> — long keys, URLs and UUIDs refuse to wrap.
#
# Exit status: 0 when the numbers are within the passed budgets, 1 otherwise.
# Budgets are arguments with the frozen defaults, so the contract can call this with 0/0/0
# after the fixes and prove the detector still fires on a planted violation.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

# TPL_ROOT exists so the contract can point the audit at a planted violation and prove that
# the detector still fires (a budget of 0 that nothing can trip is not a contract).
TPL_ROOT="${TPL_ROOT:-internal/handlers/templates}"

BUDGET_T="${1:-0}"
BUDGET_W="${2:-0}"
BUDGET_N="${3:-0}"

T=0; W=0; N=0
T_LIST=""; W_LIST=""; N_LIST=""

for f in $(find "$TPL_ROOT" -name '*.html' | sort); do
  # --- T: an unprotected table -------------------------------------------------------
  # Track, per file, whether the most recent <table> sits inside .table-wrap or .card by
  # looking back 6 lines for either marker (the markup opens the wrapper immediately above).
  while IFS= read -r line; do
    ln="${line%%:*}"
    start=$((ln - 6)); [ "$start" -lt 1 ] && start=1
    ctx="$(sed -n "${start},${ln}p" "$f")"
    case "$ctx" in
      *table-wrap*) : ;;
      *class=\"card*|*card\"*) : ;;
      *) T=$((T+1)); T_LIST="${T_LIST}${f}:${ln}"$'\n' ;;
    esac
  done < <(grep -n '<table' "$f" 2>/dev/null || true)

  # --- W: an inline fixed width ------------------------------------------------------
  w="$(grep -nE 'style="[^"]*(^|;|[[:space:]])width:[[:space:]]*[4-9][0-9]{2,}px' "$f" 2>/dev/null || true)"
  if [ -n "$w" ]; then
    n_w="$(printf '%s\n' "$w" | grep -c .)"
    W=$((W + n_w))
    while IFS= read -r l; do [ -n "$l" ] && W_LIST="${W_LIST}${f}:${l%%:*}"$'\n'; done <<< "$w"
  fi

  # --- N: nowrap on a cell -----------------------------------------------------------
  nn="$(grep -nE '<(td|th)[^>]*nowrap|white-space:[[:space:]]*nowrap' "$f" 2>/dev/null || true)"
  if [ -n "$nn" ]; then
    n_n="$(printf '%s\n' "$nn" | grep -c .)"
    N=$((N + n_n))
    while IFS= read -r l; do [ -n "$l" ] && N_LIST="${N_LIST}${f}:${l%%:*}"$'\n'; done <<< "$nn"
  fi
done

report() {
  local label="$1" got="$2" budget="$3" list="$4"
  printf '%-34s %4d (budget %s)\n' "$label" "$got" "$budget"
  if [ "$got" -gt 0 ]; then
    printf '%s' "$list" | grep . | head -12 | sed 's/^/    /'
    local shown; shown="$(printf '%s' "$list" | grep -c .)"
    [ "$shown" -gt 12 ] && printf '    … %d more\n' "$((shown - 12))"
  fi
}

report "tables outside a wrapper (T)" "$T" "$BUDGET_T" "$T_LIST"
report "inline fixed widths (W)"    "$W" "$BUDGET_W" "$W_LIST"
report "nowrap cells (N)"           "$N" "$BUDGET_N" "$N_LIST"

fail=0
[ "$T" -gt "$BUDGET_T" ] && fail=1
[ "$W" -gt "$BUDGET_W" ] && fail=1
[ "$N" -gt "$BUDGET_N" ] && fail=1
[ "$fail" -eq 0 ] && printf 'mobile-audit: within budget\n' || printf 'mobile-audit: OVER budget\n'
exit "$fail"
