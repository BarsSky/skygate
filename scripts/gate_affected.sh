#!/usr/bin/env bash
# scripts/gate_affected.sh — run the checks a DIFF can actually break.
#
# B413 (2026-10-23). Operator: «очень долго осуществляется прогон гейт есть ли способ
# распараллелить выполнение его и тем самым ускорить?»
#
# MEASURED FIRST, BUILT SECOND. The parallel pool was implemented and then removed:
# on the reference 8-core Windows workstation six workers made the catalog ~2.4x
# SLOWER (~119 min vs ~50) and turned one go-test check red on a healthy tree,
# because 88 % of the CPU sits in 182 heavyweight `check_b*.sh` contracts that
# themselves run `go test` (see the B413 banner in verify_pre_deploy.sh). The wall
# clock is work, not waiting: the fix is to run LESS, not everything at once.
#
# This script answers "less of WHAT": it reads the diff, maps every changed path to
# the catalog entries whose registration text names that path (or its basename), adds
# the meta checks that guard the harness itself, and runs the catalog with
# SKYGATE_GATE_ONLY=<selection>. Unselected checks print SKIP — never a silent pass —
# so the run is short without ever claiming more than it verified.
#
# WHAT IT IS NOT: a replacement for the full gate. Push/tag/CI still run
# `scripts/verify_pre_deploy.sh` with no filter. Use this while working.
#
# Usage:
#   bash scripts/gate_affected.sh                 # diff against origin/main
#   bash scripts/gate_affected.sh --base HEAD~3
#   bash scripts/gate_affected.sh --dry-run       # print the selection, run nothing
#   bash scripts/gate_affected.sh --full          # ignore the diff, run everything
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

GATE="scripts/verify_pre_deploy.sh"
BASE="origin/main"
DRY=0
PATHS=""
while [ $# -gt 0 ]; do
  case "$1" in
    --base)   BASE="${2:-}"; shift 2 ;;
    --paths)  PATHS="${2:-}"; shift 2 ;;
    --dry-run) DRY=1; shift ;;
    --include-untracked) INCLUDE_UNTRACKED=1; shift ;;
    --full)   exec bash "$GATE" ;;
    -h|--help) sed -n '2,24p' "$0"; exit 0 ;;
    *) echo "gate-affected: unknown argument: $1" >&2; exit 2 ;;
  esac
done

# --- the diff ----------------------------------------------------------------
# `--paths a,b,c` answers "what WOULD this change run" with no git history at all:
# the B413 contract uses it (a shallow CI checkout has no HEAD~1) and a reviewer can
# ask the same question about a hypothetical change.
if [ -n "$PATHS" ]; then
  CHANGED="$(printf '%s\n' "$PATHS" | tr ',' '\n' | sed '/^[[:space:]]*$/d')"
  if [ -z "$CHANGED" ]; then
    echo "gate-affected: --paths contained no path — nothing to select." >&2
    exit 2
  fi
else
  # A base that does not resolve is a refusal, not a silently empty selection: an
  # empty selection would make "nothing to check" indistinguishable from "the base
  # was mistyped" (the B413 lesson: a fast path must never be able to lie).
  if ! git rev-parse --verify --quiet "$BASE" >/dev/null 2>&1; then
    echo "gate-affected: base '$BASE' does not resolve (try --base HEAD~1 or fetch origin)" >&2
    exit 2
  fi
  CHANGED="$( { git diff --name-only "$BASE" --; if [ "${INCLUDE_UNTRACKED:-0}" = "1" ]; then git ls-files --others --exclude-standard; fi; } | sort -u | sed '/^$/d')"
fi
if [ -z "$CHANGED" ]; then
  echo "gate-affected: no changes against $BASE — nothing to select."
  echo "              (use --full to run the whole catalog anyway)"
  exit 0
fi

# --- the catalog, split into per-check blocks --------------------------------
# Each block is one `run_check "ID" …` entry: the registration line plus every
# following line up to the next entry. That text is the catalogue's own claim about
# WHAT the check owns, so it is the map we match paths against. One awk pass selects
# and reports the reason (a per-id rescan of a 6 400-line catalog would be quadratic).
TMP_CHANGED="$(mktemp "${TMPDIR:-/tmp}/gate-affected-XXXXXX")" || exit 2
TMP_SELECT="$(mktemp "${TMPDIR:-/tmp}/gate-affected-sel-XXXXXX")" || exit 2
trap 'rm -f "$TMP_CHANGED" "$TMP_SELECT"' EXIT
printf '%s\n' "$CHANGED" >"$TMP_CHANGED"

catalog_select() {
  awk -v list="$TMP_CHANGED" '
    BEGIN {
      while ((getline line < list) > 0) {
        if (line == "") continue
        n++
        paths[n] = line
        base[n] = line
        sub(/.*\//, "", base[n])
      }
      close(list)
    }
    function emit(   j, hit) {
      if (id == "") return
      hit = ""
      for (j = 1; j <= n; j++) {
        if (index(txt, paths[j]) > 0) { hit = paths[j]; break }
        if (base[j] != paths[j] && index(txt, base[j]) > 0) { hit = base[j]; break }
      }
      if (hit != "") printf "%s\t%s\n", id, hit
      id = ""
    }
    /^run_check "/ {
      emit()
      id = $0
      sub(/^run_check "/, "", id)
      sub(/".*/, "", id)
      txt = $0
      next
    }
    { if (id != "") txt = txt " " $0 }
    END { emit() }
  ' "$GATE"
}

catalog_select >"$TMP_SELECT"

# --- the meta checks that guard the harness itself --------------------------
# Meta checks that guard the harness/docs/i18n on ANY change: if the gate itself is
# wrong, the diff-scoped verdict is worthless.
ALWAYS=" B1 B2 B3 B4 B7 B10 B325 B330 B337 B340 B413 "
# The catalog-harness contracts only move when the catalog itself moves.
case "$CHANGED" in *scripts/verify_pre_deploy.sh*) ALWAYS="$ALWAYS B281 B299 B322 B335 " ;; esac

SELECTED=""
REASONS=""
pick() { # $1 = id, $2 = why
  case " $SELECTED " in
    *" $1 "*) return 0 ;;
  esac
  SELECTED="$SELECTED $1"
  REASONS="$REASONS
  $1 <- $2"
}

for id in $ALWAYS; do pick "$id" "always (harness/meta)"; done
while IFS="$(printf '\t')" read -r id why; do
  [ -n "${id:-}" ] || continue
  pick "$id" "mentions $why"
done <"$TMP_SELECT"

# Report which changed paths no catalog entry even mentions — the honest "nothing
# verifies this file" signal, and exactly what a developer needs to see.
UNMATCHED=""
for p in $CHANGED; do
  grep -qF -- "$p" "$GATE" || UNMATCHED="$UNMATCHED $p"
done

N="$(printf '%s' "$SELECTED" | wc -w | tr -d ' ')"
echo "gate-affected: base=$BASE, changed files: $(printf '%s\n' "$CHANGED" | wc -l | tr -d ' ')"
echo "gate-affected: selected $N check(s) of the full catalog:$(printf '\n%s' "$REASONS" | sed 's/^$//')"
if [ -n "$UNMATCHED" ]; then
  echo "gate-affected: NOT covered by any single check (the full gate still runs them):"
  printf '  %s\n' $UNMATCHED
fi

if [ "$DRY" = 1 ]; then
  echo "gate-affected: --dry-run, nothing executed."
  exit 0
fi

# --- run ---------------------------------------------------------------------
# The catalog keeps its own counters, summary and verdict; this script only decides
# WHICH entries are allowed to run.
SKYGATE_GATE_ONLY="$SELECTED" bash "$GATE"
