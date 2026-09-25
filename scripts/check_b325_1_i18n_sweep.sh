#!/usr/bin/env bash
# check_b325_1_i18n_sweep.sh
#
# 2026-09-25 (B325.1, v1.5.92) — the localization sweep, and the two holes the sweep
# itself opened.
#
# B325 (v1.5.90) measured the panel's localization debt (docs/i18n-audit.md) and froze a
# RATCHET: "RU values without Cyrillic" and "hardcoded English text nodes" may only go
# down. B325.1 paid the ratchet down to ZERO — 196 RU values translated, 21 hardcoded
# strings moved into the catalogues, 31 previously unlabelled <select> controls given
# labels from existing keys — and then closed the two ways a sweep like this can pass its
# own contract while making the panel worse:
#
#   1. AN EMPTY VALUE IS NOT AN ASCII VALUE. The B325 metric only counts a RU value that
#      has no Cyrillic AND at least two ASCII words, so blanking a translation (`"k": ""`)
#      or replacing it with a bare token ("OK", "N/A") is invisible to it: the count goes
#      DOWN and the gate goes GREEN while the page shows nothing. Contract B below pins
#      the empty case; contract C pins the whole sweep with a floor on translated values.
#
#   2. A RESTORED TEST THAT NEVER RUNS IS NOT COVERAGE. B325.1 also restored the B76/B77/
#      B78 test bodies that the v1.3.0 purge had left as `t.Skip` stubs (21 test functions
#      across five files, all passing vacuously since). A `-run` filter that matches nothing
#      exits 0, so contract D runs the restored tests and REJECTS a vacuous filter — the
#      same mechanical class the B322 audit found in four other places.
#
# CONTRACTS
#   A. the B325 ratchet is at 0 and has not been raised back up
#   B. no RU catalogue value is empty / whitespace-only
#   C. the Cyrillic floor: the sweep is not undone (a reverted translation fails here)
#   D. the restored B76/B77/B78 tests exist, carry no `t.Skip` stub, and actually RUN
#   E. the two findings B325.1 pinned in code (auto.go's nil-DB guard, update.go's comment)
#   F. this script is tracked by git

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B325.1: cannot locate cmd/skygate/main.go from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

# Frozen floor (measured 2026-09-25 after the B325.1 sweep): 2872 of 3068 RU catalogue
# values carry Cyrillic. The remaining 196 are the deliberately byte-identical command /
# code / protocol values the B325 metric excludes. 90% leaves room for a key being
# retired; a reverted sweep lands far below it.
CYRILLIC_RATIO_FLOOR="${B325_1_CYRILLIC_FLOOR:-90}"

hdr "B325.1 — the localization sweep must hold, and must not be undone"

# --- A: the B325 ratchet is at 0 --------------------------------------------------------
B325_SCRIPT="scripts/check_b325_i18n_regressions.sh"
if [ -f "$B325_SCRIPT" ]; then
  # Read the budgets the way the check itself does, so a raise is caught here too even if
  # someone edits them in a way the check's own PASS line would still print.
  RU_BUDGET=$(grep -E '^RU_ASCII_BUDGET=' "$B325_SCRIPT" | head -1 | sed -E 's/.*:-([0-9]+)\}.*/\1/')
  HC_BUDGET=$(grep -E '^HARDCODED_BUDGET=' "$B325_SCRIPT" | head -1 | sed -E 's/.*:-([0-9]+)\}.*/\1/')
  if [ "${RU_BUDGET:-x}" = "0" ] && [ "${HC_BUDGET:-x}" = "0" ]; then
    ok "A1: the B325 ratchet budgets are both 0 (RU-ASCII=$RU_BUDGET, hardcoded=$HC_BUDGET)"
  else
    bad "A1: the B325 budgets are RU-ASCII=${RU_BUDGET:-?} hardcoded=${HC_BUDGET:-?} — B325.1 drove them to 0; they may only go down, never back up"
  fi
  B325_RC=0
  # Strip the child's ANSI colour codes before parsing: the reset sequence contains a
  # DIGIT (`\033[0m`), so a naive "skip non-digits" sed stops inside it.
  OUT="$(bash "$B325_SCRIPT" 2>&1 | sed -E $'s/\033\\[[0-9;]*m//g')" || B325_RC=$?
  if [ "$B325_RC" -eq 0 ]; then
    ok "A2: check_b325 exits 0 — $(printf '%s\n' "$OUT" | grep -E '^B325 summary:' | head -1 | sed 's/^[^:]*: *//')"
    ok "A3: $(printf '%s\n' "$OUT" | grep -E 'measured:' | head -1 | sed 's/^ *//')"
  else
    bad "A2: check_b325_i18n_regressions.sh exits $B325_RC — the localization ratchet itself is failing:"
    printf '%s\n' "$OUT" | grep -E 'FAIL|summary|measured' | head -8 | sed 's/^/       /' >&2
  fi
else
  bad "A1: $B325_SCRIPT is missing — the B325 ratchet it defines cannot be read"
fi

# --- B: no RU value is empty -----------------------------------------------------------
# A blanked translation lowers B325's count instead of raising it, so it needs its own
# contract. Whitespace-only counts as empty (the panel would render a blank label).
EMPTY=$(awk '
  /^var ru[A-Za-z0-9_]* = map\[string\]string\{/ { inru = 1; next }
  /^var en[A-Za-z0-9_]* = map\[string\]string\{/ { inru = 0; next }
  /^\}/ { inru = 0 }
  inru && /"[^"]+":[[:space:]]*"/ {
    v = $0
    sub(/^[^:]*:[[:space:]]*"/, "", v)
    sub(/",?[[:space:]]*$/, "", v)
    if (v ~ /^[[:space:]]*$/) { print FILENAME ":" FNR }
  }
' internal/i18n/*.go)
if [ -z "$EMPTY" ]; then
  ok "B1: no RU catalogue value is empty or whitespace-only (an emptied translation renders a blank label)"
else
  bad "B1: RU value(s) are EMPTY — the page renders a blank label and the B325 metric cannot see it:"
  printf '%s\n' "$EMPTY" | head -10 | sed 's/^/       /' >&2
fi

# --- C: the Cyrillic floor -------------------------------------------------------------
# Total and translated counts over the same line-by-line walk the B325 metric uses, so
# "deleted the key" and "emptied the value" both land here.
read -r RU_TOTAL RU_CYR <<<"$(awk '
  /^var ru[A-Za-z0-9_]* = map\[string\]string\{/ { inru = 1; next }
  /^var en[A-Za-z0-9_]* = map\[string\]string\{/ { inru = 0; next }
  /^\}/ { inru = 0 }
  inru && /"[^"]+":[[:space:]]*"/ {
    v = $0
    sub(/^[^:]*:[[:space:]]*"/, "", v)
    sub(/",?[[:space:]]*$/, "", v)
    t++
    if (v ~ /[А-Яа-яЁё]/) n++
  }
  END { printf "%d %d\n", t + 0, n + 0 }
' internal/i18n/*.go)"
if [ "${RU_TOTAL:-0}" -gt 0 ]; then
  RATIO=$(( RU_CYR * 100 / RU_TOTAL ))
  if [ "$RATIO" -ge "$CYRILLIC_RATIO_FLOOR" ]; then
    ok "C1: $RU_CYR of $RU_TOTAL RU values carry Cyrillic (${RATIO}%, floor ${CYRILLIC_RATIO_FLOOR}%)"
  else
    bad "C1: only $RU_CYR of $RU_TOTAL RU values carry Cyrillic (${RATIO}%, floor ${CYRILLIC_RATIO_FLOOR}%) — translations were removed or reverted, which the B325 ASCII-only metric cannot see"
  fi
else
  bad "C1: no RU catalogue values found — the awk walk over internal/i18n/*.go found nothing"
fi

# --- D: the restored B76/B77/B78 tests must exist, not skip, and actually RUN ----------
RESTORED="internal/feature/admin/update_target_test.go
internal/feature/admin/system_tests_test.go
internal/nodeownership/auto_test.go
internal/nodeownership/nodeownership_test.go
internal/nodeownership/infra_test.go"
MISSING_FILE=""; SKIPS=""
while IFS= read -r f; do
  [ -n "$f" ] || continue
  if [ ! -f "$f" ]; then MISSING_FILE="$MISSING_FILE $f"; continue; fi
  if grep -qE '\bt\.Skip\(' "$f"; then SKIPS="$SKIPS $f"; fi
done <<< "$RESTORED"
if [ -z "$MISSING_FILE" ] && [ -z "$SKIPS" ]; then
  ok "D1: all five restored test files exist and carry no t.Skip stub"
else
  [ -n "$MISSING_FILE" ] && bad "D1: restored test file(s) missing:$MISSING_FILE"
  [ -n "$SKIPS" ] && bad "D1: restored test file(s) still contain a t.Skip stub (the pre-B325.1 state):$SKIPS"
fi

# The restored tests must RUN. A `-run` filter that matches nothing prints
# "[no tests to run]" and exits 0 — the B322 C1 class — so the filter is checked
# against the test binary's own list, and the run is checked for real names.
RESTORED_FILTER='TestNormalizeUpdateTarget|TestListLastRunWithResults|TestAutoBackfill|TestBackfill'
if command -v go >/dev/null 2>&1; then
  LIST="$(go test -list "$RESTORED_FILTER" ./internal/feature/admin/ ./internal/nodeownership/ 2>&1)"
  MATCHED=$(printf '%s\n' "$LIST" | grep -cE "^(TestNormalizeUpdateTarget|TestListLastRunWithResults|TestAutoBackfill|TestBackfill)")
  if [ "${MATCHED:-0}" -ge 18 ]; then
    ok "D2: the restored tests are discoverable ($MATCHED matched by -list; a vacuous filter cannot pass)"
  else
    bad "D2: -list matched only ${MATCHED:-0} restored tests, want >=18 — the filters or the test bodies were renamed/lost:"
    printf '%s\n' "$LIST" | tail -6 | sed 's/^/       /' >&2
  fi
  OUT="$(go test -count=1 -run "$RESTORED_FILTER" ./internal/feature/admin/ ./internal/nodeownership/ 2>&1)"
  if grep -q 'no tests to run' <<< "$OUT"; then
    bad "D3: go test reports '[no tests to run]' for the restored filter — the contract is vacuous:"
    printf '%s\n' "$OUT" | tail -6 | sed 's/^/       /' >&2
  elif [ "$(printf '%s\n' "$OUT" | grep -c '^ok')" -ge 2 ]; then
    ok "D3: the restored B76/B77/B78 tests actually run and pass"
  else
    bad "D3: the restored tests did not report ok for both packages:"
    printf '%s\n' "$OUT" | tail -10 | sed 's/^/       /' >&2
  fi
else
  skip "D2/D3: go not on PATH — run them on the VM"
fi

# --- E: the two findings B325.1 pinned ------------------------------------------------
# E1 — the nil-DB guard. `dbConn == nil` cannot see a non-nil DBSource whose Current() is
# nil (a typed-nil), so the loop used to start and the first tick dereferenced a missing
# pool inside a goroutine started from main. The fix asks the shared db.DBCurrent accessor.
if grep -qF 'if db.DBCurrent(dbConn) == nil {' internal/nodeownership/auto.go; then
  ok "E1a: AutoBackfill's nil-DB guard asks db.DBCurrent, so a typed-nil DBSource is caught"
else
  bad "E1a: AutoBackfill's nil-DB guard does not use db.DBCurrent(dbConn) — a non-nil DBSource with a nil Current() reaches dbConn.Current() on the first tick"
fi
if grep -qF 'func TestAutoBackfill_NilCurrentDBSourceIsSafe(' internal/nodeownership/auto_test.go; then
  ok "E1b: the typed-nil regression test is present (TestAutoBackfill_NilCurrentDBSourceIsSafe)"
else
  bad "E1b: TestAutoBackfill_NilCurrentDBSourceIsSafe is missing — the typed-nil guard has no regression test"
fi
# E2 — the comment must describe the CODE. The pre-B325.1 text promised that a target
# looking like "a SHA/branch ref" is left alone; the helper only exempts v/skygate-/main/
# HEAD, so it answers "vdevelop" and "ve2d0b9e". A comment that lies about a git ref is
# how the next reader reintroduces the B76 false-rollback class.
if grep -qF 'looks' internal/feature/admin/update.go && grep -qF 'like a SHA/branch ref' internal/feature/admin/update.go; then
  bad "E2: update.go still claims a SHA/branch ref is left alone — the helper only exempts the v/skygate-/main/HEAD prefixes"
else
  ok "E2: update.go's normalizeUpdateTarget comment matches the code (no unqualified SHA/branch exemption)"
fi
if grep -qF 've2d0b9e' internal/feature/admin/update_target_test.go &&
   grep -qF 'vdevelop' internal/feature/admin/update_target_test.go; then
  ok "E2b: the two shapes are pinned by test (raw SHA -> ve2d0b9e, other branch -> vdevelop)"
else
  bad "E2b: update_target_test.go no longer pins the raw-SHA and non-main-branch shapes"
fi

# --- F: git ---------------------------------------------------------------------------
if git ls-files --error-unmatch scripts/check_b325_1_i18n_sweep.sh >/dev/null 2>&1; then
  ok "F1: this script is TRACKED by git (AGENTS trap #11)"
else
  bad "F1: this script is NOT tracked by git"
fi

printf '\n\033[1mB325.1 summary:\033[0m %d passed, %d failed, %d skipped\n' "$PASS" "$FAIL" "$SKIP"
printf '  measured: RU values with Cyrillic = %s of %s (floor %s%%), empty RU values = %s\n' \
  "${RU_CYR:-?}" "${RU_TOTAL:-?}" "$CYRILLIC_RATIO_FLOOR" "$(printf '%s' "$EMPTY" | grep -c . )"
[ "$FAIL" -eq 0 ] || exit 1
