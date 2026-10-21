#!/usr/bin/env bash
# check_b408_verdict_budget.sh
#
# 2026-10-21 (B408) — A TEST MUST NOT BLAME A HOST FOR ITS OWN DEADLINE.
#
# WHY (measured on the operator's installation, build v1.5.151)
#   tailnet.all_nodes_reachability :: FAIL   duration 5.000534369s
#   tailnet.split_suspected        :: FAIL   duration 5.000250288s
#     100.64.0.7 cyborg         ERROR dial tcp 100.64.0.7:18022: i/o timeout
#     100.64.0.3 emilia         76ms :22
#     100.64.0.2 karolina       132ms :22
#     100.64.0.4 sharlotta      134ms :22
#
#   Three hosts answered in ~100 ms, then EVERY remaining host "timed out", and the
#   duration was exactly the harness ceiling — the probes were cancelled by the
#   per-test context, not by the network. The verdict was a fact about the RUNNER
#   reported as a fact about the tailnet (the same class B403 removed from the probe
#   itself, one layer up).
#
# CONTRACTS
#   A. a test declares its budget, and a fleet sweep gets a fleet-sized one
#   B. an exhausted budget is SAID, not disguised as silence
#   C. pinned by tests
#   D. registered, tracked, documented
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/feature/admin/system_tests_budget_b408.go ] || {
  printf 'B408: cannot locate the tree from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b408.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

B408=internal/feature/admin/system_tests_budget_b408.go
RUN=internal/feature/admin/system_tests_run_state_b394.go
DEF=internal/feature/admin/system_tests.go
TEST=internal/feature/admin/system_tests_budget_b408_test.go

has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }
src() { cat "$1" 2>/dev/null || true; }

B408_SRC="$(src "$B408")"; RUN_SRC="$(src "$RUN")"; DEF_SRC="$(src "$DEF")"; TEST_SRC="$(src "$TEST")"

hdr "B408 — a test must not blame a host for its own deadline"

# ---------------------------------------------------------------------------
# A. the budget is declared and sized by category
# ---------------------------------------------------------------------------
hdr "A — a fleet sweep gets a budget it can finish in"

if has 'Timeout time.Duration' "$DEF_SRC"; then
  ok "A1: SystemTestDef declares its own Timeout"
else
  bad "A1: the definition cannot declare a budget"
fi
if has 'func systemTestBudget(' "$B408_SRC"; then
  ok "A2: one function size-s the budget"
else
  bad "A2: no single source for the budget"
fi
if has 'networkTestBudget' "$B408_SRC" && has '75 * time.Second' "$B408_SRC"; then
  ok "A3: the network/headscale/derp categories get a fleet-sized budget (75 s)"
else
  bad "A3: the fleet categories still share the 5 s ceiling"
fi
if has 'defaultTestBudget = 5 * time.Second' "$B408_SRC"; then
  ok "A4: an ordinary in-process test keeps 5 s (the budget was not simply raised for everything)"
else
  bad "A4: the ordinary budget changed silently"
fi
if has 'if t.Timeout > 0 {' "$B408_SRC"; then
  ok "A5: a declared Timeout wins over the category default"
else
  bad "A5: a declared Timeout is ignored"
fi

# ---------------------------------------------------------------------------
# B. an exhausted budget is said out loud
# ---------------------------------------------------------------------------
hdr "B — the runner says when the runner stopped it"

if has 'budgetExhausted(testCtx)' "$RUN_SRC"; then
  ok "B1: the runner checks whether the test died on its own deadline"
else
  bad "B1: the runner cannot tell a deadline from a verdict"
fi
if has 'output += budgetNote(' "$RUN_SRC"; then
  ok "B2: the note is appended to the output the operator reads"
else
  bad "B2: the exhausted budget is invisible in the output"
fi
if has 'SIGNAL' "$RUN_SRC" 2>/dev/null; then :; fi
if has 'NOT measured' "$B408_SRC"; then
  ok "B3: the note says unprobed hosts were NOT measured"
else
  bad "B3: the note does not separate 'not asked' from 'refused'"
fi
if has 'status = SystemTestFail' "$RUN_SRC"; then
  ok "B4: an exhausted run can never be reported as PASS"
else
  bad "B4: a cut-off run could still pass"
fi

# ---------------------------------------------------------------------------
# C. pinned by tests
# ---------------------------------------------------------------------------
hdr "C — pinned"

for n in TestB408_NetworkTestsGetAFleetBudget TestB408_OrdinaryTestsKeepFiveSeconds \
         TestB408_DeclaredTimeoutWins TestB408_ExhaustedBudgetIsVisible; do
  if has "func $n(" "$TEST_SRC"; then ok "C1: $n pins a rule"; else bad "C1: $n is missing"; fi
done
if go test -count=1 -run 'TestB408' ./internal/feature/admin/ >"$SKY_TMP/b408.log" 2>&1; then
  ok "C2: the B408 tests pass"
else
  bad "C2: TestB408 fails: $(tail -3 "$SKY_TMP/b408.log" | tr '\n' ' ')"
fi

# ---------------------------------------------------------------------------
# D. registered, tracked, documented
# ---------------------------------------------------------------------------
hdr "D — registered, tracked, documented"

if git ls-files --error-unmatch scripts/check_b408_verdict_budget.sh >/dev/null 2>&1; then
  ok "D1: this script is tracked by git (trap #11)"
else
  bad "D1: this script is not tracked"
fi
if grep -q 'check_b408_verdict_budget' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "D2: registered in scripts/verify_pre_deploy.sh"
else
  bad "D2: not registered in the catalog"
fi
if grep -q '^\- \*\*B408\*\*' AGENTS.md 2>/dev/null; then
  ok "D3: one-line entry in the AGENTS.md block index"
else
  bad "D3: no B408 entry in the AGENTS block index"
fi
if grep -q 'B408' docs/ROADMAP.md 2>/dev/null && grep -q 'B408' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "D4: both roadmaps record the block (rule 12)"
else
  bad "D4: a roadmap does not record the block"
fi

printf '\n\033[1mB408: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
