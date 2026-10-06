#!/usr/bin/env bash
# check_b354_cluster_bootstrap.sh
#
# 2026-10-06 (B354) — the cluster tree must bootstrap itself, and a broken insert
# must not fill the journal.
#
# The live symptom, every five minutes on the reference deployment:
#
#   🔎 discovery-ticker: ensure "karolina" failed: insert discovered node: ERROR:
#      insert or update on table "cluster_node" violates foreign key constraint
#      "cluster_node_cluster_id_fkey" (SQLSTATE 23503)
#
# three times per tick (karolina, sharlotta, emilia). Measured on the live database:
# `cluster` had 0 rows, `cluster_node` had 0 rows, and the FK really is
# cluster_node_cluster_id_fkey -> cluster. The ticker inserts with
# cluster_id='skygate-staging', and that row was created ONLY by the admin handlers'
# first-use paths (AddNode / IssueInvite) — so an operator who never opened
# /admin/cluster got a permanently empty cluster tree, /admin/cluster empty, and 864
# log lines + 864 audit rows a day.
#
# CONTRACTS
#   A. discovery bootstraps the row it depends on, without touching an existing one
#   B. the ensure stage has a noise floor (B318's rule, applied to the second stage)
#   C. the tests run, and the live symptom is recorded where the fix lives
#   D. the check itself cannot be dropped silently
#
# SKIPs when the Go toolchain is unavailable (never FAIL).

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f cmd/skygate/main.go ] || {
  printf 'B354: cannot locate cmd/skygate/main.go from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

DISC=internal/cluster/discovery.go
CLUSTER=internal/cluster/cluster.go
HELPERS=cmd/skygate/main_helpers.go
DTEST=internal/cluster/discovery_b354_test.go
TTEST=cmd/skygate/discovery_throttle_b354_test.go

hdr "B354 — the cluster tree bootstraps itself, and a broken insert stays quiet"

# --- A: the bootstrap ---------------------------------------------------------
if grep -q 'func EnsureDiscoveredNode(' "$DISC"; then
  ok "A1: the discovery insert path exists"
else
  bad "A1: EnsureDiscoveredNode is missing"
fi
if grep -qE 'LookupCluster\(d, clusterID\)' "$DISC" && grep -qE 'EnsureCluster\(d, clusterID, clusterID\)' "$DISC"; then
  ok "A2: the row the INSERT depends on is bootstrapped first (same id/name as the B200 path)"
else
  bad "A2: EnsureDiscoveredNode still inserts into a cluster tree that may not exist"
fi
# The bootstrap must be ADDITIVE: a pre-existing row (an operator's own cluster, with
# its own name and its approved nodes) must survive a discovery tick.
if grep -q 'ON CONFLICT (id) DO NOTHING' "$CLUSTER"; then
  ok "A3: EnsureCluster is idempotent, so an existing cluster row is never clobbered"
else
  bad "A3: EnsureCluster can overwrite an existing row"
fi
# The reason has to be where the next reader looks: the FK name from the live log.
if grep -q 'cluster_node_cluster_id_fkey' "$DISC"; then
  ok "A4: the live symptom is recorded at the fix (the next reader sees what it was)"
else
  bad "A4: the fix does not name the failure it repairs"
fi

# --- B: the noise floor -------------------------------------------------------
if grep -q 'func discoveryEnsureErrorIsNew(' "$HELPERS" && grep -q 'discoveryEnsureErrorCleared' "$HELPERS"; then
  ok "B1: the ensure stage is throttled like the discover stage (B318's rule, second stage)"
else
  bad "B1: the ensure stage still logs and audits every failure on every tick"
fi
if grep -q 'discoveryEnsureLast' "$HELPERS" && grep -q 'discoveryErrRepeatAfter' "$HELPERS"; then
  ok "B2: the throttle is keyed on the message and expires after an hour"
else
  bad "B2: the throttle has no key or no window"
fi
if grep -q 'still failing for %d peer(s)' "$HELPERS"; then
  ok "B3: a suppressed repeat says so instead of pretending nothing happened"
else
  bad "B3: a suppressed repeat is silent"
fi

# --- C: tests and the live evidence ------------------------------------------
if command -v go >/dev/null 2>&1; then
  if [ -f "$DTEST" ] && [ -f "$TTEST" ]; then
    ok "C1: both halves have tests (the bootstrap + the noise floor)"
  else
    bad "C1: missing test file(s): $DTEST / $TTEST"
  fi
  OUT="$(go test ./internal/cluster/ -run 'TestB354' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "C2: the bootstrap tests pass (they start from the live state: an empty cluster table)"
  else
    bad "C2: the cluster B354 tests failed: $(tail -3 <<< "$OUT" | tr '\n' ' ')"
  fi
  OUT2="$(go test ./cmd/skygate/ -run 'TestB354' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT2"; then
    ok "C3: the throttle tests pass"
  else
    bad "C3: the throttle tests failed: $(tail -3 <<< "$OUT2" | tr '\n' ' ')"
  fi
  # The regression test must actually exercise the FK: without enforcement an empty
  # cluster table is harmless on SQLite, and the test would pass for the wrong reason.
  if grep -q 'SQLSTATE 23503\|FOREIGN KEY constraint failed' "$DTEST"; then
    ok "C4: the test records the real constraint failure it reproduces"
  else
    bad "C4: the test does not name the failure it guards"
  fi
else
  skip "C1-C4: no Go toolchain — the B354 test contracts were not run"
fi

# --- D: this check cannot be dropped silently --------------------------------
if git ls-files --error-unmatch scripts/check_b354_cluster_bootstrap.sh >/dev/null 2>&1; then
  ok "D1: this script is tracked by git"
else
  bad "D1: scripts/check_b354_cluster_bootstrap.sh is NOT tracked by git"
fi
if grep -q 'check_b354_cluster_bootstrap.sh' scripts/verify_pre_deploy.sh; then
  ok "D2: verify_pre_deploy.sh registers B354"
else
  bad "D2: the gate does not run this contract"
fi
if grep -q '^- \*\*B354\*\*' AGENTS.md; then
  ok "D3: AGENTS.md's block index carries B354"
else
  bad "D3: the block index does not know B354"
fi

hdr "B354 summary"
printf 'PASS=%d FAIL=%d SKIP=%d\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
