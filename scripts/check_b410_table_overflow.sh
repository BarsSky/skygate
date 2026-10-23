#!/usr/bin/env bash
# check_b410_table_overflow.sh
#
# 2026-10-22 (B410) — A TABLE MUST SCROLL INSIDE ITS CARD, NOT LEAVE IT.
#
# WHY (operator report on the live panel)
#   «на странице /admin/system_tests?tab=history содержимое выходит за границы
#    таблицы; также в целом большинство страниц на широких экранах не оставляет
#    справа много свободного места».
#
#   Measured with the audit method of the skygate-web-ui-audit skill (§2): the
#   History tab renders a NINE-column <table> (six of them with fixed widths, plus
#   `th{white-space:nowrap}` from themes.css) and it was NOT inside .table-wrap, the
#   only element that has `overflow-x:auto` (themes.css:485). A wide row therefore
#   pushed the table past the card's padding and out of the page column — on the
#   named page and on 19 other admin templates.
#
# CONTRACTS
#   A. the named page is clean: every <table> sits inside .table-wrap
#   B. the rest is a FROZEN BUDGET that may only go down (the B337 ratchet pattern):
#      a new unwrapped table fails the gate immediately, and a file removed from the
#      list must also be removed from the budget file
#   C. the CSS prerequisite exists (overflow-x on the wrapper)
#   D. registered, tracked, documented
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/handlers/templates/admin/system_tests.html ] || {
  printf 'B410: cannot locate the tree from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b410.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

BUDGET=scripts/b410_unwrapped_tables_allowlist.txt
CSS=internal/staticfs/static/css/themes.css

hdr "B410 — a table must scroll inside its card, not leave it"

# ---------------------------------------------------------------------------
# A. the page the operator named
# ---------------------------------------------------------------------------
hdr "A — the named page"

TPL=internal/handlers/templates/admin/system_tests.html
T=$(grep -c '<table' "$TPL" 2>/dev/null || echo 0)
W=$(grep -c 'table-wrap' "$TPL" 2>/dev/null || echo 0)
if [ "${T:-0}" -gt 0 ] && [ "${W:-0}" -ge "${T:-0}" ]; then
  ok "A1: every one of the ${T} table(s) on /admin/system_tests is inside .table-wrap"
else
  bad "A1: ${T} table(s), ${W} wrapper(s) — the History tab overflowed its card"
fi
# The history rows are the wide ones: nine columns. Make sure the wrapper really
# surrounds THAT table and not only the small ones.
#
# 2026-10-23 (B410.1): the first version was `awk '/eq \.Tab "history"/,0' | grep -q
# ...` and it PASSED on Windows while FAILING on the CI runner — a range pattern
# with a bare `,0` end is not portable (mawk/busybox treat `0` as a regex `/0/`, so
# the range can stop at some unrelated line), and piping awk into `grep -q` is the
# shape B386/B388 forbid. Now a single awk pass decides it: from the History marker,
# find the FIRST <table> and answer whether a .table-wrap came before it. No range
# pattern, no pipe, no platform-dependent behaviour.
A2_VERDICT="$(awk '
  /eq \.Tab "history"/   { seen = 1 }
  seen && /table-wrap/   { wrapped = 1 }
  seen && /<table/       { print (wrapped ? "yes" : "no"); exit }
' "$TPL" 2>/dev/null)"
if [ "${A2_VERDICT:-}" = "yes" ]; then
  ok "A2: the History tab's own table is wrapped"
else
  bad "A2: the History tab's table is still unwrapped (verdict: ${A2_VERDICT:-no table found after the History marker})"
fi

# ---------------------------------------------------------------------------
# B. the frozen budget for the rest
# ---------------------------------------------------------------------------
hdr "B — the remaining pages are a frozen budget that may only go down"

UNWRAPPED="$SKY_TMP/unwrapped.txt"
: > "$UNWRAPPED"
for f in $(grep -rl '<table' internal/handlers/templates --include='*.html'); do
  t=$(grep -c '<table' "$f")
  w=$(grep -c 'table-wrap' "$f")
  if [ "$t" -gt "$w" ]; then
    printf '%s\n' "${f#internal/handlers/templates/}" >> "$UNWRAPPED"
  fi
done
sort -o "$UNWRAPPED" "$UNWRAPPED"

if [ -f "$BUDGET" ]; then
  BUD="$SKY_TMP/budget.txt"
  grep -v '^#' "$BUDGET" | grep -v '^$' | sort > "$BUD"
  NEW=$(comm -23 "$UNWRAPPED" "$BUD")
  if [ -n "$NEW" ]; then
    bad "B1: a template with an unwrapped table is not in the budget: $(printf '%s' "$NEW" | tr '\n' ' ')"
  else
    ok "B1: no NEW unwrapped table (budget $(wc -l < "$BUD") file(s))"
  fi
  GONE=$(comm -13 "$UNWRAPPED" "$BUD")
  if [ -n "$GONE" ]; then
    bad "B2: the budget still lists file(s) that are fixed — remove them: $(printf '%s' "$GONE" | tr '\n' ' ')"
  else
    ok "B2: the budget matches the tree exactly (it ratchets only downwards)"
  fi
else
  bad "B2: $BUDGET is missing — the frozen budget is the contract"
fi

# ---------------------------------------------------------------------------
# C. the CSS prerequisite
# ---------------------------------------------------------------------------
hdr "C — the wrapper actually scrolls"

if grep -q '\.table-wrap{overflow-x:auto' "$CSS" 2>/dev/null; then
  ok "C1: .table-wrap sets overflow-x:auto (the only element that may scroll a table)"
else
  bad "C1: .table-wrap lost overflow-x — wrapping a table would then do nothing"
fi
if grep -q 'white-space:nowrap' "$CSS" 2>/dev/null; then
  ok "C2: th{white-space:nowrap} still holds, so the wrapper is the only escape hatch"
else
  skip "C2: headers are allowed to wrap now (the wrapper matters less)"
fi

# ---------------------------------------------------------------------------
# D. registered, tracked, documented
# ---------------------------------------------------------------------------
hdr "D — registered, tracked, documented"

if git ls-files --error-unmatch scripts/check_b410_table_overflow.sh >/dev/null 2>&1; then
  ok "D1: this script is tracked by git (trap #11)"
else
  bad "D1: this script is not tracked"
fi
if grep -q 'check_b410_table_overflow' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "D2: registered in scripts/verify_pre_deploy.sh"
else
  bad "D2: not registered in the catalog"
fi
if git ls-files --error-unmatch "$BUDGET" >/dev/null 2>&1; then
  ok "D3: the budget file is tracked (a local-only budget is not a contract)"
else
  bad "D3: the budget file is not tracked"
fi
if grep -q '^\- \*\*B410\*\*' AGENTS.md 2>/dev/null; then
  ok "D4: one-line entry in the AGENTS.md block index"
else
  bad "D4: no B410 entry in the AGENTS block index"
fi

printf '\n\033[1mB410: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
