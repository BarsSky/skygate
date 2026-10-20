#!/usr/bin/env bash
# check_b402_cluster_truth.sh
#
# 2026-10-20 (B402) — A HEARTBEAT THAT ARRIVES MUST BEAT A STATE THAT SAYS IT DID NOT,
# AND THE PANEL MUST BE ABLE TO SETTLE IT BY HAND.
#
# WHY (measured on the operator's installation, verbatim state)
#   cluster_node : svyatoslava | roles={skygate-standby} | state=failed
#                  skygate_version=v1.5.108 | joined 2026-10-09 09:45
#                  last_seen_at = 2026-10-20 07:45      ← the CURRENT minute
#   cluster_audit: 2026-10-09 17:40 node_health {"to":"failed","from":"ready",
#                  "actor":"elector","reason":"last_seen 17m43s ago (3+ …"}
#
#   Two independent freezes:
#   1. `Heartbeat()` moved only pending → ready, while the elector marks `failed` when
#      heartbeats stop. NOTHING moved it back — `RejoinNode` had exactly one caller, the
#      rolling-upgrade orchestrator — so a node that kept heartbeating every 30 s stayed
#      "dead" for eleven days and only hand-written SQL could fix it.
#   2. `HeartbeatRequest` carried node_id + token only, so `skygate_version` could only
#      ever hold what the JOIN wrote: a standby on v1.5.108 and one on the current build
#      were indistinguishable.
#
# CONTRACTS
#   A. the heartbeat reverses `failed` and records WHY (and never touches draining)
#   B. the build travels with every heartbeat and an old agent cannot erase it
#   C. the page NAMES the contradiction and offers Mark Ready (RejoinNode, by hand)
#   D. version drift is derived from the row and rendered
#   E. pinned by tests, registered, documented
#
# Scratch under mktemp -d (AGENTS trap #13); no `producer | grep -q` (trap #9).

set -uo pipefail

if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f internal/cluster/join.go ] || {
  printf 'B402: cannot locate the tree from %s (run from the repo root or set SKYGATE_REPO)\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

SKY_TMP="$(mktemp -d 2>/dev/null || mktemp -d "${TMPDIR:-/tmp}/skygate-b402.XXXXXX")"
trap 'rm -rf "$SKY_TMP"' EXIT

JOIN=internal/cluster/join.go
DAEMON=cmd/skygate/cluster.go
CL=internal/feature/admin/cluster.go
TPL=internal/handlers/templates/admin/cluster.html
ROUTES=cmd/skygate/routes.go
CAT=internal/i18n/catalog_admin.go
T_CL=internal/cluster/heartbeat_b402_test.go
T_PAGE=internal/feature/admin/cluster_b402_test.go

has() { case "$2" in *"$1"*) return 0 ;; *) return 1 ;; esac; }
src() { cat "$1" 2>/dev/null || true; }

JOIN_SRC="$(src "$JOIN")"; DAEMON_SRC="$(src "$DAEMON")"; CL_SRC="$(src "$CL")"
TPL_SRC="$(src "$TPL")"; ROUTES_SRC="$(src "$ROUTES")"; CAT_SRC="$(src "$CAT")"
TCL_SRC="$(src "$T_CL")"; TPAGE_SRC="$(src "$T_PAGE")"

hdr "B402 — a heartbeat that arrives must beat a state that says it did not"

# ---------------------------------------------------------------------------
# A. the heartbeat reverses failed, and says so
# ---------------------------------------------------------------------------
hdr "A — the heartbeat is the fresher fact"

if has "WHEN state = 'failed'  THEN 'ready'" "$JOIN_SRC"; then
  ok "A1: an authenticated heartbeat moves failed → ready"
else
  bad "A1: failed is still terminal for the heartbeat — the live eleven-day loop is intact"
fi
if has "WHEN state = 'pending' THEN 'ready'" "$JOIN_SRC"; then
  ok "A2: pending → ready is unchanged"
else
  bad "A2: the first-heartbeat transition was lost"
fi
if has 'db.NodeHealth' "$JOIN_SRC" && has 'authenticated heartbeat resumed' "$JOIN_SRC"; then
  ok "A3: the reversal is audited as node_health WITH its reason (no unexplained resurrection)"
else
  bad "A3: failed → ready would leave no audit trail"
fi
if has 'prevState == NodeStateFailed && state == NodeStateReady' "$JOIN_SRC"; then
  ok "A4: the audit fires only on a real reversal, not on every heartbeat"
else
  bad "A4: the audit condition is wrong"
fi
if has "WHEN state = 'draining'" "$JOIN_SRC"; then
  bad "A5: draining is now cleared by a heartbeat — that is the operator's decision, not the network's"
else
  ok "A5: draining is never touched by a heartbeat"
fi

# ---------------------------------------------------------------------------
# B. the build travels with the heartbeat
# ---------------------------------------------------------------------------
hdr "B — the version is measured, not frozen at join time"

if has 'Version string `json:"version,omitempty"`' "$JOIN_SRC"; then
  ok "B1: HeartbeatRequest carries the build"
else
  bad "B1: the heartbeat still cannot report a build — cluster_node.skygate_version is frozen at join"
fi
if has 'skygate_version = CASE WHEN $3 <> '"''"' THEN $3 ELSE skygate_version END' "$JOIN_SRC"; then
  ok "B2: the row follows the reported build, and an EMPTY report cannot erase it"
else
  bad "B2: an old agent would blank the recorded version"
fi
if has 'Version: selfVersion()' "$DAEMON_SRC"; then
  ok "B3: the daemon sends the same selfVersion() /healthz and the panel footer use (B366)"
else
  bad "B3: the daemon does not send a version"
fi

# ---------------------------------------------------------------------------
# C. the page names the contradiction and offers the button
# ---------------------------------------------------------------------------
hdr "C — the operator can see the contradiction and settle it"

if has 'HeartbeatAlive bool' "$CL_SRC"; then
  ok "C1: the row carries the contradiction"
else
  bad "C1: the page cannot tell «stale state» from «dead node»"
fi
if has 'now-lastSeenAt.Time.Unix() <= OnlineThresholdSec' "$CL_SRC"; then
  ok "C2: it is derived from the elector's own 90 s window, not a second invented number"
else
  bad "C2: the freshness rule diverged from the elector's"
fi
if has 'cluster.reason_failed_heartbeat_alive' "$CL_SRC" && has 'cluster.action_failed_heartbeat_alive' "$CL_SRC"; then
  ok "C3: the reason AND the next action are named"
else
  bad "C3: the contradiction has no named reason"
fi
if has 'PostAdminClusterNodeRejoin' "$CL_SRC" && has 'cluster.RejoinNode' "$CL_SRC"; then
  ok "C4: Mark Ready is the operator's own RejoinNode"
else
  bad "C4: RejoinNode is still unreachable by hand"
fi
if has '/admin/cluster/node/rejoin' "$ROUTES_SRC"; then
  ok "C5: the route is registered"
else
  bad "C5: the button would post into a 404"
fi
if has 'cluster/node/rejoin' "$TPL_SRC" && has 'cluster.node_rejoin_btn' "$TPL_SRC"; then
  ok "C6: the button is rendered in the row"
else
  bad "C6: no button — the state cannot be settled from the panel"
fi
n1="$(grep -c '"cluster.reason_failed_heartbeat_alive"' "$CAT" 2>/dev/null || true)"
n2="$(grep -c '"cluster.node_rejoin_btn"' "$CAT" 2>/dev/null || true)"
if [ "${n1:-0}" -ge 2 ] && [ "${n2:-0}" -ge 2 ]; then
  ok "C7: the texts exist in RU + EN ($n1 / $n2 entries)"
else
  bad "C7: the texts are not in both catalogues ($n1 / $n2)"
fi

# ---------------------------------------------------------------------------
# D. version drift is derived and rendered
# ---------------------------------------------------------------------------
hdr "D — a three-month-old standby is visible"

if has 'func sameBuild(' "$CL_SRC"; then
  ok "D1: one comparison decides drift (the +commit suffix is part of the identity)"
else
  bad "D1: drift has no definition"
fi
if has 'VersionDrift bool' "$CL_SRC" && has 'cluster.version_drift' "$TPL_SRC"; then
  ok "D2: the row carries it and the table renders it"
else
  bad "D2: the drift is computed nowhere or shown nowhere"
fi
if has 'func TestB402_VersionDriftIsMeasuredVerbatim(' "$TPAGE_SRC"; then
  ok "D3: the comparison rule is pinned (including the hex-case and empty-side cases)"
else
  bad "D3: nothing pins the drift comparison"
fi

# ---------------------------------------------------------------------------
# E. tests, registration, documentation
# ---------------------------------------------------------------------------
hdr "E — pinned, registered, documented"

for n in TestB402_HeartbeatClearsFailed TestB402_DrainingIsNotClearedByAHeartbeat TestB402_OlderAgentDoesNotEraseTheVersion TestB402_TheReverseTransitionIsAudited; do
  if has "func $n(" "$TCL_SRC"; then ok "E1: $n pins the heartbeat half"; else bad "E1: $n is missing"; fi
done
for n in TestB402_FailedRowWithAFreshHeartbeatIsNamedAndActionable TestB402_PageSetsTheContradictionFromTheDatabase; do
  if has "func $n(" "$TPAGE_SRC"; then ok "E2: $n pins the page half"; else bad "E2: $n is missing"; fi
done
if git ls-files --error-unmatch scripts/check_b402_cluster_truth.sh >/dev/null 2>&1; then
  ok "E3: this script is tracked by git (trap #11)"
else
  bad "E3: this script is not tracked"
fi
if grep -q 'check_b402_cluster_truth' scripts/verify_pre_deploy.sh 2>/dev/null; then
  ok "E4: registered in scripts/verify_pre_deploy.sh"
else
  bad "E4: not registered in the catalog"
fi
if grep -q '^\- \*\*B402\*\*' AGENTS.md 2>/dev/null; then
  ok "E5: one-line entry in the AGENTS.md block index"
else
  bad "E5: no B402 entry in the AGENTS block index"
fi
if grep -q 'B402' docs/ROADMAP.md 2>/dev/null && grep -q 'B402' docs/ru/ROADMAP.md 2>/dev/null; then
  ok "E6: both roadmaps record the block (rule 12)"
else
  bad "E6: a roadmap does not record the block"
fi

printf '\n\033[1mB402: %d PASS / %d FAIL / %d SKIP\033[0m\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
