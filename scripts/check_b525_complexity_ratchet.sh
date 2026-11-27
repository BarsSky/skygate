#!/usr/bin/env bash
# check_b525_complexity_ratchet.sh
#
# 2026-11-27 (B525 block 0) — «сложность должна быть измеримой, иначе она растёт молча».
#
# WHY. The 2026-11-27 audit answered "is the code tangled?" with numbers: 1 304 Go files,
# 321 627 lines, 72 TODO markers, 45 packages importing internal/db — and the real hotspots
# were five files and seven functions (main() 2 272 lines / cc 139, registerRoutes 1 109,
# GetMyExitRules 1 051/124, GenerateACLWithViaForPlane 1 018/114 …). The problem is LOCAL
# and therefore fixable in blocks, but nothing tracked it: a function could double in size
# between releases and only a human reading the diff would notice. This contract is the
# ratchet (the gofmt/B337 pattern): today's numbers are a frozen budget, any GROWTH fails
# the gate, and every refactor block lowers the numbers on purpose.
#
# WHAT IT DOES NOT DO. It is not a linter and not a complexity oracle: scripts/
# complexity_scan.awk counts physical lines and branch keywords after stripping comments,
# strings and runes. Its one job is to notice growth. The report half
# (complexity_scan.sh --report) is what a human reads before choosing the next block.
#
# A SCANNER THAT FINDS NOTHING MUST FAIL. The first version of the awk looked for backticks
# before comments, so one `// … `x` …` comment swallowed every file after it and the report
# looked CLEAN with 14 functions instead of 4 403. Row A2 plants a known-long function and a
# known-branchy one into a temp file and requires the scanner to report both, with numbers.
set -uo pipefail
if [ -f "$(dirname "$0")/../go.mod" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/go.mod" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f go.mod ] || { printf 'B525: cannot locate go.mod from %s\n' "$PWD" >&2; exit 2; }

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SCAN=scripts/complexity_scan.sh
AWK=scripts/complexity_scan.awk
BASE=scripts/complexity_baseline.txt
SKY_TMP="$(mktemp -d /tmp/skygate-b525.XXXXXX)" || exit 2
trap 'rm -rf "$SKY_TMP"' EXIT

hdr "B525 — the complexity budget is measured, frozen and enforced"

# --- A: the instrument works, and it is PROVEN to see something -----------------
A_MISS=""
[ -x "$SCAN" ] || [ -f "$SCAN" ] || A_MISS="${A_MISS}$SCAN is missing"$'\n'
[ -f "$AWK" ] || A_MISS="${A_MISS}$AWK is missing"$'\n'
if [ -n "$A_MISS" ]; then
  bad "A1: the instrument is incomplete:"
  printf '%s' "$A_MISS" | sed 's/^/       /' >&2
else
  if ! command -v go >/dev/null 2>&1 && ! command -v awk >/dev/null 2>&1; then
    skip "A1: neither go nor awk on PATH — the scanner cannot run here"
  else
    printf '%s' 'bash scripts/complexity_scan.sh' >/dev/null
    bash "$SCAN" > "$SKY_TMP/a1.txt" 2>"$SKY_TMP/a1.err"; RC=$?
    bash "$SCAN" > "$SKY_TMP/a2.txt" 2>/dev/null
    N=$(grep -c . "$SKY_TMP/a1.txt" 2>/dev/null || echo 0)
    if [ "$RC" -ne 0 ]; then
      bad "A1: the scanner failed (rc=$RC): $(tail -2 "$SKY_TMP/a1.err")"
    elif [ "$N" -lt 3000 ]; then
      # A scanner that silently finds almost nothing is worse than no scanner: it reports
      # a clean tree. Measured: 4 403 functions on 2026-11-27.
      bad "A1: the scanner reported only $N function(s) — a scan that finds nothing proves nothing (see A2)"
    elif ! cmp -s "$SKY_TMP/a1.txt" "$SKY_TMP/a2.txt"; then
      bad "A1: the scanner is not deterministic (two runs differ)"
    else
      ok "A1: deterministic scan of $N functions"
    fi
  fi
fi

# A2 — prove the detector sees a long and a branchy function.
if command -v awk >/dev/null 2>&1; then
  {
    printf 'package fixture\n\nfunc PlantedLong() error {\n'
    i=0; while [ $i -lt 260 ]; do printf '\t_ = %s\n' "$i"; i=$((i+1)); done
    printf '\treturn nil\n}\n\nfunc PlantedBranchy(x int) int {\n\ttotal := 0\n'
    i=0; while [ $i -lt 45 ]; do printf '\tif x == %s {\n\t\ttotal++\n\t}\n' "$i"; i=$((i+1)); done
    printf '\treturn total\n}\n'
  } > "$SKY_TMP/planted.go"
  OUT="$(bash scripts/complexity_scan.sh --files "$SKY_TMP/planted.go" 2>&1)"
  LONG_LINES=$(printf '%s\n' "$OUT" | awk -F'\t' '$4=="PlantedLong"{print $1}')
  BRANCHY_CC=$(printf '%s\n' "$OUT" | awk -F'\t' '$4=="PlantedBranchy"{print $2}')
  if [ -z "$LONG_LINES" ] || [ -z "$BRANCHY_CC" ]; then
    bad "A2: the scanner did not see the planted functions — it cannot be trusted on the real tree"
  elif [ "$LONG_LINES" -lt 250 ] || [ "$BRANCHY_CC" -lt 40 ]; then
    bad "A2: the scanner under-counts (lines=$LONG_LINES want >=250, cc=$BRANCHY_CC want >=40) — a planting must be caught"
  else
    ok "A2: the detector is proven on a planted fixture (lines=$LONG_LINES, cc=$BRANCHY_CC)"
  fi
else
  skip "A2: no awk on PATH — the detector proof cannot run here"
fi

# --- B/C/D: the frozen budget vs the live tree ----------------------------------
if [ ! -f "$BASE" ]; then
  bad "B1: $BASE is missing — the budget is not frozen"
  bad "C1: nothing to compare against"
  bad "D1: nothing to compare against"
else
  ENTRIES=$(grep -cv '^#' "$BASE" 2>/dev/null || echo 0)
  R_LINES=$(sed -n 's/^# ratchet thresholds: lines>=\([0-9]*\) or cc>=\([0-9]*\)/\1/p' "$BASE" | head -1)
  R_CC=$(sed -n 's/^# ratchet thresholds: lines>=\([0-9]*\) or cc>=\([0-9]*\) or cc>=*//p' "$BASE" | head -1)
  R_CC=$(sed -n 's/^# ratchet thresholds: lines>=\([0-9]*\) or cc>=\([0-9]*\)/\2/p' "$BASE" | head -1)
  if [ -z "$R_LINES" ] || [ -z "$R_CC" ]; then
    bad "B1: the budget header does not record its own thresholds (cannot tell what 'growth' means)"
  elif [ "$ENTRIES" -lt 10 ]; then
    bad "B1: the budget lists only $ENTRIES entries — a threshold that matches nothing is not a budget"
  else
    ok "B1: budget frozen with $ENTRIES entries (lines>=$R_LINES or cc>=$R_CC)"
  fi
  if [ -n "$R_LINES" ] && [ -n "$R_CC" ] && [ "$FAIL" -eq 0 ]; then
    bash "$SCAN" > "$SKY_TMP/live.txt" 2>/dev/null
    awk -F'\t' 'NR==FNR && $0 !~ /^#/ {k=$3"|"$4; base[k]=$1"|"$2; next} {k=$3"|"$4}' "$BASE" "$SKY_TMP/live.txt" > /dev/null 2>&1
    # C: growth of a budgeted function
    awk -F'\t' -v OFS='\t' '
      NR==FNR && $0 !~ /^#/ { k=$3"|"$4; bl[k]=$1+0; bc[k]=$2+0; next }
      { k=$3"|"$4; if (k in bl) { if ($1+0 > bl[k] || $2+0 > bc[k]) printf "%s\t%s\tlines %d->%d cc %d->%d\n", $3, $4, bl[k], $1, bc[k], $2 } }
    ' "$BASE" "$SKY_TMP/live.txt" > "$SKY_TMP/growth.txt"
    if [ -s "$SKY_TMP/growth.txt" ]; then
      bad "C1: a budgeted function grew — the budget is the contract, split or bill it deliberately:"
      sed 's/^/       /' "$SKY_TMP/growth.txt" >&2
    else
      ok "C1: no budgeted function grew beyond its frozen numbers"
    fi
    # D: a NEW offender above the ratchet thresholds must be argued in the baseline
    awk -F'\t' -v L="$R_LINES" -v C="$R_CC" '
      NR==FNR && $0 !~ /^#/ { k=$3"|"$4; seen[k]=1; next }
      { k=$3"|"$4; if (!(k in seen) && (($1+0>=L)||($2+0>=C))) printf "%s\t%s\tlines=%d cc=%d\n", $3, $4, $1, $2 }
    ' "$BASE" "$SKY_TMP/live.txt" > "$SKY_TMP/new.txt"
    if [ -s "$SKY_TMP/new.txt" ]; then
      bad "D1: new function(s) above the ratchet thresholds — add to the budget with a reason, or split:"
      sed 's/^/       /' "$SKY_TMP/new.txt" >&2
    else
      ok "D1: no new offender appeared above the ratchet thresholds"
    fi
    # Stale entries are reported, not failed: a split/rename is a legitimate outcome.
    # Comment lines in the budget are not entries: without the guards below they become a
    # phantom "gone" row (observed: D2 claimed one entry left the tree on a freshly frozen
    # budget). The key is printed so a real rename is diagnosable.
    awk -F'\t' '
      NR==FNR && $0 !~ /^#/ && NF>=4 { k=$3"|"$4; base[k]=1; next }
      NR!=FNR && NF>=4 { k=$3"|"$4; live[k]=1 }
      END { for (k in base) if (!(k in live)) print k }
    ' "$BASE" "$SKY_TMP/live.txt" > "$SKY_TMP/gone.txt"
    if [ -s "$SKY_TMP/gone.txt" ]; then
      ok "D2: $(grep -c . "$SKY_TMP/gone.txt") budget entr(ies) left the tree — refresh the budget deliberately (bash scripts/complexity_scan.sh --baseline)"
    else
      ok "D2: every budget entry still exists"
    fi
  fi
fi

# --- E: the human half still works ----------------------------------------------
OUT="$(bash scripts/complexity_scan.sh --report 2>&1)"
case "$OUT" in
  *"functions="*"TOP BY LINES"*"TOP BY BRANCHES"*)
    ok "E1: the report renders totals plus the two hotspot tables" ;;
  *)
    bad "E1: the report half is broken (no totals/tables)";;
esac

# --- F: bookkeeping -------------------------------------------------------------
F_MISS=""
for f in "$SCAN" "$AWK" "$BASE" scripts/check_b525_complexity_ratchet.sh; do
  [ -e "$f" ] || F_MISS="${F_MISS}$f does not exist"$'\n'
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 || F_MISS="${F_MISS}$f is NOT tracked by git"$'\n'
done
grep -q 'check_b525_complexity_ratchet.sh' scripts/verify_pre_deploy.sh \
  || F_MISS="${F_MISS}verify_pre_deploy.sh does not register B525"$'\n'
grep -q '\*\*B525\*\*' AGENTS.md \
  || F_MISS="${F_MISS}AGENTS.md's block index has no B525 entry"$'\n'
if [ -z "$F_MISS" ]; then
  ok "F1: tracked, registered and indexed"
else
  bad "F1: the block bookkeeping is incomplete:"
  printf '%s' "$F_MISS" | sed 's/^/       /' >&2
fi

hdr "B525 summary"
printf 'B525: %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
