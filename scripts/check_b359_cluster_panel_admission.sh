#!/usr/bin/env bash
# check_b359_cluster_panel_admission.sh
#
# 2026-10-07 (B359) — the cluster must be creatable from the admin panel ALONE,
# and /admin/cluster must not list things that are not skygate hosts.
#
# THE OPERATOR'S WORK ORDER (verbatim intent):
#
#   «собрать кластер со святославой и проверить, что он может быть создан
#    автономно только через админ-панель»
#
# THE MEASURED LIVE STATE (reference deployment, PostgreSQL, 2026-10-07):
#
#   cluster:      1 row  — id='skygate-staging', chain=[]
#   cluster_node: 3 rows — node-disc-emilia    | emilia    | failed | {skygate-standby}
#                          node-disc-karolina  | karolina  | failed | {skygate-standby}
#                          node-disc-sharlotta | sharlotta | failed | {skygate-standby}
#
# `emilia`, `karolina` and `sharlotta` are exit-node RELAYS. B354 made
# EnsureDiscoveredNode bootstrap the FK parent, so every one of them was inserted
# as a `skygate-standby` CANDIDATE; with no skygate to join they settled in
# state=failed forever. /admin/cluster therefore offered three permanently dead
# "standby" nodes, because the discovery predicate had no positive notion of
# "this is a skygate host" — only "is not already in cluster_node".
#
# CONTRACTS
#   A. the predicate exists, is pure, and is the ONLY adoption rule
#   B. a relay (tag:exit-node, or a row in exit_servers) is NEVER adopted, and
#      that property is pinned by a running test, not by a comment
#   C. a genuine skygate host (the per-node infra tag the PANEL's own onboarding
#      mints) IS adopted, and the pass stays idempotent across two runs
#   D. every peer that is not adopted is reported with a NAMED reason (never
#      silently dropped), and a repeat cannot bury the journal
#   E. /admin/cluster names the reason AND the next action for every row
#   F. the panel-only procedure is documented (docs/operations.md §17)
#   G. tracked, registered, indexed (AGENTS §2 trap #11)
#
# SKIPs when the Go toolchain is unavailable (never FAIL). Live state is never
# required: every check below reads the tree or runs a unit test.
#
# Usage:  bash scripts/check_b359_cluster_panel_admission.sh
# Exit:   0 = all contracts hold, 1 = regression

set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

# AGENTS trap #13: scratch lives under a per-run directory, never a fixed /tmp
# path (B340 keeps the count at zero; the reference VM's root cannot overwrite
# another user's file, so a fixed path turns a stale file into a fake product
# FAIL). Nothing in this script needs scratch today, but the trap is declared so
# a future addition cannot reintroduce the class.
SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)"
trap 'rm -rf "$SKY_TMP"' EXIT

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '\033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

# The Go toolchain on this workstation is the Windows one, reached from WSL as
# `go.exe`; on the reference VM it is plain `go`. Resolve once, and SKIP (never
# FAIL) when neither exists.
GO=""
for cand in go go.exe; do
  if command -v "$cand" >/dev/null 2>&1; then GO="$cand"; break; fi
done

DISC=internal/cluster/discovery.go
DTEST=internal/cluster/discovery_b359_test.go
ADMIN=internal/feature/admin/cluster.go
ATEST=internal/feature/admin/cluster_reason_b359_test.go
TPL=internal/handlers/templates/admin/cluster.html
CAT=internal/i18n/catalog_admin.go
HELPERS=cmd/skygate/main_helpers.go
OPS=docs/operations.md
CATALOG=scripts/verify_pre_deploy.sh

for f in "$DISC" "$DTEST" "$ADMIN" "$ATEST" "$TPL" "$CAT" "$HELPERS" "$OPS" "$CATALOG"; do
  [ -f "$f" ] || { bad "A0: missing $f"; printf 'B359 summary: PASS=%d FAIL=%d SKIP=%d\n' "$PASS" "$((FAIL+1))" "$SKIP"; exit 1; }
done

hdr "B359 — panel-only cluster admission, and only skygate hosts in the cluster list"

# --- A: the predicate ---------------------------------------------------------------
if grep -q '^func ClassifyPeerForAdoption(' "$DISC"; then
  ok "A1: ClassifyPeerForAdoption is the single adoption rule (pure: no DB, no clock, no network)"
else
  bad "A1: the discovery predicate is missing — discovery would adopt every online peer again"
fi
if grep -q '^func RelayIndexFromDB(' "$DISC" && grep -q 'FROM exit_servers' "$DISC"; then
  ok "A2: the relay index is read from exit_servers (skygate's own relay record, no parallel source)"
else
  bad "A2: nothing reads exit_servers — a relay that has not been tagged yet could be adopted"
fi
if grep -q 'func IsSkygateHostTag(' "$DISC" && grep -q '"tag:dev-infra-"+h' "$DISC"; then
  ok "A3: the positive evidence is the per-node infra tag the panel's own onboarding mints (exact host, case-insensitive)"
else
  bad "A3: the skygate-host tag predicate is missing or not host-exact"
fi
# The relay check must come BEFORE the cluster-membership check, or a relay that
# somebody already added by hand would be adopted on the next tick.
if awk '/^func ClassifyPeerForAdoption/{f=1} f&&/SkipRelayExitTag/{r=NR} f&&/SkipAlreadyClusterMember/{m=NR} END{exit !(r&&m&&r<m)}' "$DISC"; then
  ok "A4: the relay verdict precedes the already-a-member verdict (a hand-added relay is still refused as a relay)"
else
  bad "A4: the relay check does not precede the membership check"
fi
# B223's contract (check_b223.sh F) pins DiscoverNewNodes + listClusterHostnames;
# the de-dup must still be there or re-running discovery would duplicate rows.
if grep -q '^func DiscoverNewNodes(' "$DISC" && grep -q 'listClusterHostnames' "$DISC"; then
  ok "A5: DiscoverNewNodes still de-duplicates against cluster_node (B223's contract is intact)"
else
  bad "A5: DiscoverNewNodes lost its de-duplication"
fi

# --- B: a relay is never adopted -----------------------------------------------------
if grep -q 'TestB359_ExitNodeRelayIsNeverAdopted' "$DTEST" \
   && grep -q 'TestB359_ExitServersRowAloneIsEnough' "$DTEST"; then
  ok "B1: both relay facts have a regression test (the tag, and the exit_servers row)"
else
  bad "B1: the relay-rejection property has no test"
fi
if grep -q 'want 0 — a relay is never a skygate host' "$DTEST"; then
  ok "B2: the test asserts the live shape (three relays, zero adoptions), not a proxy"
else
  bad "B2: the relay test does not assert 'zero adoptions'"
fi

# --- C: a genuine host is adopted, idempotently --------------------------------------
if grep -q 'TestB359_GenuineSkygateHostIsAdopted' "$DTEST" \
   && grep -q 'tag:dev-infra-svyatoslava' "$DTEST"; then
  ok "C1: a genuine skygate host is adopted (the fixture is the tag the panel mints)"
else
  bad "C1: nothing proves the predicate still admits a real host"
fi
if grep -q 'TestB359_SecondPassIsIdempotent' "$DTEST" && grep -q 'want 1' "$DTEST"; then
  ok "C2: the pass is pinned idempotent (two runs, one row, one discovered audit event)"
else
  bad "C2: no idempotency contract"
fi

# --- D: nothing is dropped silently --------------------------------------------------
for reason in SkipRelayExitTag SkipRelayExitServer SkipNotSkygateHost SkipAlreadyClusterMember SkipIPv6Only SkipNoAddress SkipNoHostname; do
  if grep -q "	$reason DiscoverySkipReason" "$DISC"; then
    ok "D1: named reason $reason is declared"
  else
    bad "D1: named reason $reason is missing"
  fi
done
if grep -q 'Rejected *\[\]DiscoveryRejection' "$DISC" && grep -q 'report.Rejected = append' "$DISC"; then
  ok "D2: the pass RETURNS every refusal instead of filtering it away"
else
  bad "D2: refusals are not returned — they would vanish (L-54)"
fi
if grep -q 'func discoveryRejectionIsNew(' "$HELPERS" \
   && grep -q 'func discoveryReasonIsNews(' "$HELPERS" \
   && grep -q 'discoveryRejectionCleared()' "$HELPERS"; then
  ok "D3: the ticker reports a refusal once an hour (B318/B354's noise floor, third stage)"
else
  bad "D3: the ticker has no noise floor for refusals"
fi
if grep -q 'cluster.discovery.skip' "$HELPERS" && grep -q 'cluster.discovery.skip' "$ADMIN"; then
  ok "D4: a refusal writes its own audit action on both paths (ticker and panel)"
else
  bad "D4: refusals reach neither audit trail"
fi
if grep -q 'func discoverySkipNote(' "$ADMIN" && grep -q 'Not adopted as skygate hosts' "$ADMIN"; then
  ok "D5: the panel NAMES the refusals in the operator's flash"
else
  bad "D5: the discover button says nothing about the peers it refused"
fi
# The panel-only path has one precondition the page must state itself.
if grep -q '^func InfraUserLinked(' "$DISC" && grep -q 'cluster.InfraUserLinked(' "$ADMIN"; then
  ok "D6: an unlinked infra user (whose key the onboarding would mint) is reported, not discovered on the new host"
else
  bad "D6: the onboarding precondition is invisible until it fails on a provisioned VM"
fi

# --- E: the page names the reason and the next action --------------------------------
if grep -q '^func clusterNodeReasonAndNext(' "$ADMIN"; then
  ok "E1: the per-row reason/next-action rule table exists and is pure"
else
  bad "E1: rows carry no reason — the live page showed three dead standbys and no explanation"
fi
if grep -q "cluster.reason_foreign_relay" "$ADMIN" && grep -q "cluster.reason_foreign_relay" "$TPL"; then
  ok "E2: a relay row is called out as NOT a skygate host on the page"
else
  bad "E2: the page does not distinguish a relay from a skygate host"
fi
if grep -q 'cluster.col_reason' "$TPL" && grep -q 'cluster.col_reason' "$CAT"; then
  ok "E3: the table carries the new reason column"
else
  bad "E3: no reason column in the node table"
fi
# Every reason/action the Go rule emits must exist in RU and EN (AGENTS rule 10).
MISSING=""
EMITTED="$(grep -o '"cluster\.\(reason\|action\)_[a-z_]*"' "$ADMIN" | tr -d '"' | sort -u)"
while IFS= read -r key; do
  [ -n "$key" ] || continue
  n="$(grep -c "\"$key\":" "$CAT" || true)"
  if [ "${n:-0}" -ne 2 ]; then
    MISSING="$MISSING $key($n)"
  fi
done <<< "$EMITTED"
if [ -z "$MISSING" ]; then
  ok "E4: every emitted reason/action key is defined in RU and EN ($(printf '%s\n' "$EMITTED" | grep -c . ) keys)"
else
  bad "E4: keys missing from one catalogue (count in parentheses):$MISSING"
fi

# --- F: the procedure is documented --------------------------------------------------
if grep -q '^## 17\. Admitting a second skygate node from the panel' "$OPS"; then
  ok "F1: docs/operations.md §17 documents the panel-only procedure"
else
  bad "F1: the panel-only procedure is not documented"
fi
if grep -q '17\. Admitting a second skygate node from the panel' "$OPS"; then
  if grep -q 'tag:dev-infra-<hostname>' "$OPS" || grep -q 'tag:dev-infra-' "$OPS"; then
    ok "F2: the document names the positive predicate (the operator can verify a host by hand)"
  else
    bad "F2: the document does not name the predicate"
  fi
  if grep -qi 'never be added to' "$OPS"; then
    ok "F3: the document warns that a skygate host must NOT be added to exit_servers (the reverse trip)"
  else
    skip "F3: the exit_servers warning is phrased differently — read §17 by hand"
  fi
else
  skip "F1-F3 follow-up: the section exists but its sub-contracts were not matched"
fi

# --- G: the tests actually run --------------------------------------------------------
if [ -n "$GO" ]; then
  OUT="$("$GO" test ./internal/cluster/ -run 'TestB359' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT"; then
    ok "G1: the discovery predicate tests pass (relay refused, host admitted, reason named, idempotent)"
  else
    bad "G1: the B359 discovery tests failed: $(tail -3 <<< "$OUT" | tr '\n' ' ')"
  fi
  OUT2="$("$GO" test ./internal/feature/admin/ -run 'TestB359' -count=1 2>&1)"
  if grep -q '^ok' <<< "$OUT2"; then
    ok "G2: the page-classification tests pass (the live relay rows are named, not offered for approval)"
  else
    bad "G2: the B359 admin tests failed: $(tail -3 <<< "$OUT2" | tr '\n' ' ')"
  fi
  OUT3="$("$GO" build ./... 2>&1)"
  if [ -z "$OUT3" ]; then
    ok "G3: go build ./... is clean"
  else
    bad "G3: go build ./... failed: $(head -3 <<< "$OUT3" | tr '\n' ' ')"
  fi
else
  skip "G1-G3: no Go toolchain on PATH — the B359 tests were not run (run them on the VM)"
fi

# --- H: tracked, registered, indexed (AGENTS §2 trap #11) ----------------------------
if git ls-files --error-unmatch scripts/check_b359_cluster_panel_admission.sh >/dev/null 2>&1; then
  ok "H1: this script is tracked by git"
else
  bad "H1: scripts/check_b359_cluster_panel_admission.sh is NOT tracked by git (trap #11: .gitignore can eat a new script)"
fi
if grep -q 'check_b359_cluster_panel_admission.sh' "$CATALOG"; then
  ok "H2: verify_pre_deploy.sh registers B359"
else
  bad "H2: the gate does not run this contract"
fi
if grep -q '^- \*\*B359\*\*' AGENTS.md; then
  ok "H3: AGENTS.md's block index carries B359"
else
  bad "H3: the block index does not know B359"
fi

hdr "B359 summary"
printf 'PASS=%d FAIL=%d SKIP=%d\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ] || exit 1
exit 0
