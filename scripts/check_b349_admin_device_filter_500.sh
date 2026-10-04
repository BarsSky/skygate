#!/usr/bin/env bash
# check_b349_admin_device_filter_500.sh — B349: the admin per-device drill-down
# answered every request with a 500.
#
# WHY THIS FILE EXISTS (measured on the live host, 2026-10-04)
# -----------------------------------------------------------
# `GET /admin/exit-rules?device=cyborg` returned **HTTP 500** with the body
#
#	sql: expected 12 destination arguments in Scan, not 13
#
# B277.4 added `COALESCE(r.all_devices, 0) AS all_devices` to
# `qSelectAllRulesForAdmin` AND extended the shared scan loop
# (`getAllRulesForAdminQuery`) to 13 destinations — but not to
# `qSelectAllRulesForAdminByDevice`, which kept its 12-column list. Both go through
# that one scan, so the FILTERED view failed on every call while the unfiltered view
# worked. The page rendered the error as raw text, which the operator reported as
# «пропали правила из админ страницы exit rules и по остальным устройствам что не
# skyworker»: the rules were fine, the filtered view of them was a 500 — and the
# B348 device index (added the same day) linked straight into it.
#
# Why a grep-only contract is not enough, and what this script does about it:
# a text assertion can see that a column is missing, but only RUNNING the query
# proves the SELECT and the scan agree. The negative control was measured by hand:
# removing the column again makes TestGetAllRulesForAdminQueries_B349 fail with the
# exact production error, and restoring it makes the test pass — so the test, not
# the grep, is the load-bearing half here (contract C1 runs it).
#
# What this script verifies:
#   A. both admin views select the same trailing column set, and the filtered one
#      carries all_devices
#   B. the shared scan reads one destination per selected column (the arity that
#      broke)
#   C. the behavioural test exists, is registered, and PASSES (both views)
#   D. tracked, registered, indexed, gofmt-clean
#
# Usage:  bash scripts/check_b349_admin_device_filter_500.sh
# Exit:   0 = all contracts hold, 1 = regression

set -uo pipefail
cd "$(dirname "$0")/.."

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

QUERIES=internal/db/queries.go
RULES=internal/db/device_rules.go
TEST=internal/db/admin_rules_queries_b349_test.go
CAT=scripts/verify_pre_deploy.sh

for f in "$QUERIES" "$RULES" "$TEST" "$CAT"; do
  [ -f "$f" ] || { bad "A0: missing $f"; echo "B349 summary: $PASS passed, $FAIL failed, $SKIP skipped"; exit 1; }
done

count() { grep -c -- "$1" "$2" 2>/dev/null || true; }

hdr "A. both admin views select the same trailing column set"

if grep -q 'const qSelectAllRulesForAdmin = .*COALESCE(r.all_devices, 0) AS all_devices FROM' "$QUERIES"; then
  ok "A1: the unfiltered view carries all_devices (it always did — that is why only the filtered one broke)"
else
  bad "A1: the unfiltered admin view lost its all_devices column"
fi
if grep -q 'const qSelectAllRulesForAdminByDevice = .*COALESCE(r.all_devices, 0) AS all_devices FROM' "$QUERIES"; then
  ok "A2: the FILTERED view carries all_devices too (the B349 fix)"
else
  bad "A2: the filtered admin view is missing all_devices — it would 500 again with 'expected 12 destination arguments in Scan, not 13'"
fi

# The tail of both SELECT lists must be identical: the shared scan reads the
# columns POSITIONALLY, so a difference anywhere after the last common column
# shifts every field.
TAIL_UNFILTERED="$(grep -o 'COALESCE(r.device_ip, .\{0,3\}) AS device_ip.*FROM device_rules' "$QUERIES" | head -1)"
TAIL_FILTERED="$(grep -o 'COALESCE(r.device_ip, .\{0,3\}) AS device_ip.*FROM device_rules' "$QUERIES" | sed -n '2p')"
if [ -n "$TAIL_UNFILTERED" ] && [ "$TAIL_UNFILTERED" = "$TAIL_FILTERED" ]; then
  ok "A3: the tail of both column lists is byte-identical (device_ip → user_name → all_devices)"
else
  bad "A3: the two admin views select different columns near the end: unfiltered=[$TAIL_UNFILTERED] filtered=[$TAIL_FILTERED]"
fi
if [ "$(count 'COALESCE(r.all_devices, 0) AS all_devices' "$QUERIES")" -ge 3 ]; then
  ok "A4: all three admin views (plain, paged, by-device) carry the column"
else
  bad "A4: only $(count 'COALESCE(r.all_devices, 0) AS all_devices' "$QUERIES") of the three admin views carry all_devices"
fi

hdr "B. the shared scan reads one destination per column"

# The scan is the other half of the arity: count its destinations and compare with
# the number of commas+1 in each SELECT list, ignoring commas inside parentheses
# (COALESCE(a, b) has one).
SCAN_DESTS="$(awk '/func getAllRulesForAdminQuery\(/,/^}/' "$RULES" | grep -o '&[a-zA-Z_.]*' | wc -l | tr -d ' ')"
if [ "$SCAN_DESTS" = "13" ]; then
  ok "B1: the shared scan reads 13 destinations (the arity B277.4 introduced)"
else
  bad "B1: the shared scan reads $SCAN_DESTS destinations, expected 13 — every caller must select exactly that many columns"
fi

# Depth-aware column count of one SELECT list: commas at nesting depth 0 + 1.
cols_of() {
  awk -v src="$1" 'BEGIN{
    i = index(src, "SELECT "); if (i == 0) { print 0; exit }
    s = substr(src, i + 7)
    j = index(s, " FROM "); if (j > 0) s = substr(s, 1, j - 1)
    depth = 0; n = 1
    for (k = 1; k <= length(s); k++) {
      c = substr(s, k, 1)
      if (c == "(") depth++
      else if (c == ")") depth--
      else if (c == "," && depth == 0) n++
    }
    print n
  }'
}
U_LINE="$(grep -o 'const qSelectAllRulesForAdmin = `[^`]*`' "$QUERIES" | head -1)"
F_LINE="$(grep -o 'const qSelectAllRulesForAdminByDevice = `[^`]*`' "$QUERIES" | head -1)"
U_COLS="$(cols_of "$U_LINE")"
F_COLS="$(cols_of "$F_LINE")"
if [ "$U_COLS" = "$F_COLS" ] && [ "$U_COLS" = "$SCAN_DESTS" ]; then
  ok "B2: both views select $U_COLS columns and the scan reads $SCAN_DESTS — the arity agrees"
else
  bad "B2: column/destination mismatch: unfiltered=$U_COLS filtered=$F_COLS scan=$SCAN_DESTS"
fi

hdr "C. the behavioural test (the load-bearing half)"

if grep -q 'func TestGetAllRulesForAdminQueries_B349(' "$TEST"; then
  ok "C1: the test runs BOTH admin views through the real helpers"
else
  bad "C1: the behavioural regression test is missing"
fi
if grep -q 'expected 12 destination arguments\|SELECT and the shared scan disagree' "$TEST"; then
  ok "C2: its failure message names the defect (measured: removing the column reproduces 'expected 12 destination arguments in Scan, not 13')"
else
  bad "C2: the test does not name the defect it guards"
fi
GO_BIN=""
for cand in "$(command -v go 2>/dev/null)" /usr/local/go/bin/go /usr/lib/go/bin/go "$HOME/go/bin/go"; do
  if [ -n "$cand" ] && [ -x "$cand" ]; then GO_BIN="$cand"; break; fi
done
if [ -z "$GO_BIN" ]; then
  skip "C3: go is not on PATH — run the B349 tests on the VM"
else
  if OUT="$("$GO_BIN" test ./internal/db/ -run 'B349' -count=1 2>&1)"; then
    ok "C3: the B349 test passes ($(printf '%s' "$OUT" | tail -1))"
  else
    bad "C3: the B349 test fails: $(printf '%s' "$OUT" | tail -3)"
  fi
fi

hdr "D. tracked, registered, indexed, gofmt-clean"

if git ls-files --error-unmatch scripts/check_b349_admin_device_filter_500.sh >/dev/null 2>&1; then
  ok "D1: this script is tracked by git (AGENTS trap #11)"
else
  bad "D1: NOT tracked by git"
fi
if grep -q 'check_b349_admin_device_filter_500.sh' "$CAT"; then
  ok "D2: registered in scripts/verify_pre_deploy.sh"
else
  bad "D2: not registered — it would never run"
fi
if grep -q '\*\*B349\*\*' AGENTS.md; then
  ok "D3: recorded in the AGENTS.md block index"
else
  bad "D3: no B349 line in the AGENTS.md block index"
fi
if [ -z "$(command -v gofmt || true)" ]; then
  skip "D4: gofmt not on PATH — the VM run covers rule 3"
else
  DIRTY="$(gofmt -l "$QUERIES" "$TEST" 2>/dev/null || true)"
  if [ -z "$DIRTY" ]; then
    ok "D4: the touched files are gofmt-clean"
  else
    bad "D4: gofmt is not clean for: $(printf '%s' "$DIRTY" | tr '\n' ' ')"
  fi
fi

hdr "B349 summary: $PASS passed, $FAIL failed, $SKIP skipped"

if [ "$FAIL" -gt 0 ]; then
  exit 1
fi
exit 0
