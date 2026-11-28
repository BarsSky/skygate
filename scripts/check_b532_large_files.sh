#!/usr/bin/env bash
# check_b532_large_files.sh
#
# 2026-11-27 (B532) — a 1 500-line file is a place where a reader stops looking.
#
# The audit measured FIVE Go files over 1 500 lines and named them; the plan (§8) is to
# split them BY SECTION with the same verbatim rule the boot refactor used. This contract
# is the register: it measures the tree every run, prints what is still over the line, and
# FREEZES the list — a file may fall below the line (good), but a NEW one over it needs a
# deliberate change here, exactly like the B525 budget and the B337 gofmt ratchet.
#
# WHAT IT PINS
#   A. the measured list and the frozen list agree on the big files; the count is printed;
#   B. the upgrader split (the first section split) is REAL: docker.go is under 600 lines,
#      the five section files exist, and every one of them is tracked by git;
#   C. no function was lost in the split — the package's function count is at or above the
#      floor recorded here, and `go test ./internal/update/` passes;
#   D. bookkeeping: this script is tracked, the block is in the catalog, and AGENTS.md
#      indexes it.
#
# WHERE IT SKIPS: the test leg (C3) SKIPs when go is absent; everything else is source.
set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B532: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

LINE=1500
# Frozen list of files still over the line (the audit's five plus catalog_admin.go, which
# is generated-style DATA — one entry per i18n string — and is deliberately not split).
FROZEN="internal/i18n/catalog_admin.go internal/feature/admin/cluster.go internal/feature/exit_rules/reconciler.go internal/db/migrations_pg.go internal/db/migrations_sqlite.go"

hdr "B532 — files over ${LINE} lines are a register, not a surprise"

# --- A: the measured list -------------------------------------------------------
MEASURED=""
COUNT=0
for f in $(git ls-files '*.go' | grep -v '_test.go'); do
  n="$(wc -l < "$f" 2>/dev/null || echo 0)"
  if [ "${n:-0}" -ge "$LINE" ]; then
    MEASURED="${MEASURED}${f} "
    COUNT=$((COUNT + 1))
  fi
done
ok "A1: ${COUNT} Go file(s) are at or over ${LINE} lines: $(echo "$MEASURED" | tr ' ' '\n' | grep -v '^$' | xargs -r -n1 basename | tr '\n' ' ')"
A_MISS=""
for f in $MEASURED; do
  case " $FROZEN " in
    *" $f "*) ;;
    *) A_MISS="${A_MISS}${f} grew past ${LINE} lines and is not in this contract's frozen list"$'\n' ;;
  esac
done
for f in $FROZEN; do
  if [ ! -f "$f" ]; then
    A_MISS="${A_MISS}${f} is on the frozen list but no longer exists — refresh the list"$'\n'
  fi
done
if [ -z "$A_MISS" ]; then
  ok "A2: every measured file is on the frozen list (a NEW one needs a deliberate edit here)"
else
  while IFS= read -r l; do [ -n "$l" ] && bad "A2: $l"; done <<< "$A_MISS"
fi

# --- B: the upgrader split is real ----------------------------------------------
B_MISS=""
DOCKER=internal/update/docker.go
dn="$(wc -l < "$DOCKER" 2>/dev/null || echo 0)"
[ "${dn:-0}" -lt 600 ] || B_MISS="${B_MISS}${DOCKER} is still ${dn} lines — the section split did not happen"$'\n'
for f in internal/update/docker_git.go internal/update/docker_shell.go \
         internal/update/docker_health.go internal/update/docker_labels.go \
         internal/update/docker_swap.go; do
  [ -f "$f" ] || B_MISS="${B_MISS}${f} is missing (a section was dropped, not moved)"$'\n'
  git ls-files --error-unmatch "$f" >/dev/null 2>&1 \
    || B_MISS="${B_MISS}${f} is NOT tracked by git (trap 11)"$'\n'
done
if [ -z "$B_MISS" ]; then
  ok "B: docker.go is ${dn} lines and its five sections are separate, tracked files"
else
  while IFS= read -r l; do [ -n "$l" ] && bad "B: $l"; done <<< "$B_MISS"
fi

# --- C: no function was lost ----------------------------------------------------
FN_FLOOR=340
FN_COUNT="$(grep -h '^func ' internal/update/*.go 2>/dev/null | wc -l | tr -d ' ')"
if [ "${FN_COUNT:-0}" -ge "$FN_FLOOR" ]; then
  ok "C1: internal/update still declares ${FN_COUNT} functions (floor ${FN_FLOOR}) — the split moved code, it did not delete it"
else
  bad "C1: internal/update declares only ${FN_COUNT} functions (floor ${FN_FLOOR}) — a split deleted code"
fi
if command -v go >/dev/null 2>&1; then
  OUT="$(go test -count=1 ./internal/update/ 2>&1)"
  rc=$?
  if [ "$rc" -eq 0 ]; then
    ok "C2: the upgrader package tests pass after the split"
  else
    bad "C2: the upgrader package tests failed: $OUT"
  fi
else
  skip "C2: go not on PATH"
fi

# --- D: bookkeeping -------------------------------------------------------------
D_MISS=""
grep -q '"B532"' scripts/verify_pre_deploy.sh || D_MISS="${D_MISS}verify_pre_deploy.sh does not register B532"$'\n'
git ls-files --error-unmatch scripts/check_b532_large_files.sh >/dev/null 2>&1 \
  || D_MISS="${D_MISS}this script is NOT tracked by git"$'\n'
grep -q '^- \*\*B532\*\*' AGENTS.md || D_MISS="${D_MISS}AGENTS.md's block index carries no B532 line"$'\n'
if [ -z "$D_MISS" ]; then ok "D: tracked, registered and indexed"
else while IFS= read -r l; do [ -n "$l" ] && bad "D: $l"; done <<< "$D_MISS"; fi

printf '\nB532: %d pass, %d fail, %d skip\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
