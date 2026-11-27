#!/usr/bin/env bash
# complexity_scan.sh — B525 block 0: the measurable half of "is the code getting tangled?".
#
# Usage:
#   bash scripts/complexity_scan.sh                 # TSV: lines<TAB>cc<TAB>file<TAB>func
#   bash scripts/complexity_scan.sh --report        # human summary (top offenders + totals)
#   bash scripts/complexity_scan.sh --thresholds    # the thresholds this script uses
#   bash scripts/complexity_scan.sh --files f1 f2   # scan explicit files (used by tests)
#
# WHY THIS EXISTS. The 2026-11-27 audit answered "is the tree cluttered?" with numbers:
# 1 304 Go files / 321 627 lines, 72 TODO markers, 45 packages importing internal/db — and
# the actual hotspots were five files and seven functions. The problem is therefore local
# and trackable, but nothing tracked it: a function could double in size between two
# releases and only a human reading the diff would notice. This is the instrument.
#
# The scan is deliberately dumb and deterministic (see complexity_scan.awk): it measures
# PHYSICAL LINES and BRANCH KEYWORDS, not "cognitive complexity". It is a BUDGET, and the
# frozen budget lives in scripts/complexity_baseline.txt; the contract that compares them is
# scripts/check_b525_complexity_ratchet.sh.
set -uo pipefail
if [ -f "$(dirname "$0")/../go.mod" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/go.mod" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi

# Thresholds: a function above EITHER bound is an "offender" and must be listed in the
# budget. Chosen from the 2026-11-27 measurement so that the baseline is ~20 entries, not
# ~200: big enough to catch real regressions, small enough that every line is deliberate.
MIN_LINES="${SKYGATE_COMPLEXITY_MIN_LINES:-120}"
MIN_CC="${SKYGATE_COMPLEXITY_MIN_CC:-25}"

# RATCHET thresholds: the frozen budget's own line. Deliberately looser than the report's
# thresholds, so the baseline starts as the ~20 genuine hotspots instead of 110 entries;
# every refactor block lowers these numbers, and a NEW function above them fails the gate.
RATCHET_LINES="${SKYGATE_COMPLEXITY_RATCHET_LINES:-200}"
RATCHET_CC="${SKYGATE_COMPLEXITY_RATCHET_CC:-40}"

mode=scan
files=()
while [ $# -gt 0 ]; do
  case "$1" in
    --report) mode=report ;;
    --thresholds) mode=thresholds ;;
    --baseline) mode=baseline ;;
    --files) shift; while [ $# -gt 0 ]; do files+=("$1"); shift; done; break ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) printf 'complexity_scan: unknown argument %s\n' "$1" >&2; exit 2 ;;
  esac
  shift
done

if [ "$mode" = thresholds ]; then
  printf 'report: lines>=%s or cc>=%s\nratchet: lines>=%s or cc>=%s\n' "$MIN_LINES" "$MIN_CC" "$RATCHET_LINES" "$RATCHET_CC"
  exit 0
fi

if [ "${#files[@]}" -eq 0 ]; then
  # Non-test Go sources only: a test file is allowed to be long (table-driven fixtures),
  # and including it would drown the product signal.
  mapfile -t files < <(git ls-files '*.go' 2>/dev/null | grep -v '_test\.go$')
fi
[ "${#files[@]}" -gt 0 ] || { printf 'complexity_scan: no Go files to scan\n' >&2; exit 2; }

scan() {
  awk -f scripts/complexity_scan.awk "${files[@]}" 2>/dev/null | sort -t$'\t' -k3,3 -k4,4
}

if [ "$mode" = scan ]; then
  scan
  exit 0
fi

# --baseline: freeze today's offenders as the budget. Run ONLY when a refactor block
# deliberately changes the shape (a function split, or a new deliberate hotspot argued in
# the block's B-check). The check compares live numbers against this file.
if [ "$mode" = baseline ]; then
  out="${1:-scripts/complexity_baseline.txt}"
  {
    printf '# B525 complexity budget — frozen %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf '# ratchet thresholds: lines>=%s or cc>=%s\n' "$RATCHET_LINES" "$RATCHET_CC"
    printf '# format: lines<TAB>cc<TAB>file<TAB>func\n'
    scan | awk -F'\t' -v L="$RATCHET_LINES" -v C="$RATCHET_CC" '($1+0>=L)||($2+0>=C)' | sort -t$'\t' -k3,3 -k4,4
  } > "$out"
  printf 'wrote %s (%s entries)\n' "$out" "$(grep -cv '^#' "$out")"
  exit 0
fi
# --report: what a human needs to decide where to work next.
ALL="$(scan)"
total_funcs=$(printf '%s\n' "$ALL" | grep -c . || true)
max_lines=$(printf '%s\n' "$ALL" | awk -F'\t' 'BEGIN{m=0} {if ($1+0>m) m=$1+0} END{print m}')
max_cc=$(printf '%s\n' "$ALL" | awk -F'\t' 'BEGIN{m=0} {if ($2+0>m) m=$2+0} END{print m}')
offenders=$(printf '%s\n' "$ALL" | awk -F'\t' -v L="$MIN_LINES" -v C="$MIN_CC" '($1+0>=L)||($2+0>=C)' | grep -c . || true)
off_total=$(printf '%s\n' "$ALL" | awk -F'\t' -v L="$MIN_LINES" -v C="$MIN_CC" '($1+0>=L)||($2+0>=C)' | wc -l)
printf 'functions=%s  max_lines=%s  max_cc=%s  offenders=%s\n' "$total_funcs" "$max_lines" "$max_cc" "$offenders"
printf 'thresholds: lines>=%s or cc>=%s\n' "$MIN_LINES" "$MIN_CC"
printf '\nTOP BY LINES\n'
printf '%s\n' "$ALL" | sort -t$'\t' -k1,1 -rn | head -12 | awk -F'\t' '{printf "  %5d lines %4d cc  %-56s %s\n", $1, $2, $3, $4}'
printf '\nTOP BY BRANCHES\n'
printf '%s\n' "$ALL" | sort -t$'\t' -k2,2 -rn | head -12 | awk -F'\t' '{printf "  %5d lines %4d cc  %-56s %s\n", $1, $2, $3, $4}'
printf '\nPER-PACKAGE OFFENDER LINES\n'
printf '%s\n' "$ALL" | awk -F'\t' -v L="$MIN_LINES" -v C="$MIN_CC" '($1+0>=L)||($2+0>=C)' \
  | awk -F'\t' '{f=$3; sub(/\/[^\/]+$/, "", f); sum[f]+=$1} END {for (p in sum) printf "  %7d  %s\n", sum[p], p}' \
  | sort -rn | head -12
