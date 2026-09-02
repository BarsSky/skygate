#!/usr/bin/env bash
# check_b215.sh — B215 (v1.5.0+) bootstrap state machine
# audit events. Phase 2.6 of
# docs/internal/cluster-management.md.
#
# Closes the "bootstrap events (init/join/drain/leave)
# are silent in cluster_audit" gap. Pre-B215, only the
# failover path wrote to cluster_audit — the bootstrap
# paths (init via B211, join via B212, leave via the
# admin Remove button, drain via failover) had no audit
# trail. Operators couldn't answer "who bootstrapped
# this node, when did the last standby join, when did
# we drain the old primary" without parsing logs.
#
# Each `check` is one row; pass = exit 0, fail = exit 1.
# Run from the repo root:
#
#   bash scripts/check_b215.sh
set -euo pipefail

# REPO_ROOT resolution (same pattern as check_b211/b212/b213/b214.sh).
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
if [[ -n "${SKYGATE_REPO_ROOT:-}" ]]; then
  REPO_ROOT="$SKYGATE_REPO_ROOT"
else
  REPO_ROOT="$SCRIPT_DIR"
  while [[ "$REPO_ROOT" != "/" ]] && [[ ! -f "$REPO_ROOT/go.mod" ]]; do
    REPO_ROOT="$(dirname "$REPO_ROOT")"
  done
  if [[ ! -f "$REPO_ROOT/go.mod" ]]; then
    REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
  fi
fi
cd "$REPO_ROOT"

# Go binary resolution.
if [[ -n "${GO_BIN:-}" ]]; then
  GO="$GO_BIN"
elif command -v go >/dev/null 2>&1; then
  GO="go"
elif [[ -x "/c/Program Files/Go/bin/go.exe" ]]; then
  GO="/c/Program Files/Go/bin/go.exe"
elif [[ -x "/usr/local/go/bin/go" ]]; then
  GO="/usr/local/go/bin/go"
else
  GO="go"
fi

PASS=0
FAIL=0

check() {
  local label="$1"
  local want="$2"
  local got="$3"
  if [[ "$got" == "$want" ]]; then
    echo "[ok]   $label"
    PASS=$((PASS + 1))
  else
    echo "[FAIL] $label"
    echo "       want: $want"
    echo "       got:  $got"
    FAIL=$((FAIL + 1))
  fi
}

# A: cluster_audit.go exists (the helper module)
[[ -f "$REPO_ROOT/internal/db/cluster_audit.go" ]]
check "A: internal/db/cluster_audit.go exists" "0" "$?"

# B: 4 new actions defined as typed constants
grep -q 'NodeInit\s*ClusterAuditAction\s*=\s*"node_init"' "$REPO_ROOT/internal/db/cluster_audit.go"
check "B: NodeInit constant defined" "0" "$?"
grep -q 'NodeJoin\s*ClusterAuditAction\s*=\s*"node_join"' "$REPO_ROOT/internal/db/cluster_audit.go"
check "C: NodeJoin constant defined" "0" "$?"
grep -q 'NodeDrain\s*ClusterAuditAction\s*=\s*"node_drain"' "$REPO_ROOT/internal/db/cluster_audit.go"
check "D: NodeDrain constant defined" "0" "$?"
grep -q 'NodeLeave\s*ClusterAuditAction\s*=\s*"node_leave"' "$REPO_ROOT/internal/db/cluster_audit.go"
check "E: NodeLeave constant defined" "0" "$?"

# F: helper accepts *sql.DB OR *sql.Tx (auditExec interface)
grep -q "type auditExec interface" "$REPO_ROOT/internal/db/cluster_audit.go"
check "F: auditExec interface accepts *sql.DB + *sql.Tx" "0" "$?"

# G: helper is called by runInitBootstrap
grep -q "db.NodeInit" "$REPO_ROOT/cmd/skygate/init.go"
check "G: runInitBootstrap emits node_init audit event" "0" "$?"

# H: helper is called by cluster.Join
grep -q "db.NodeJoin" "$REPO_ROOT/internal/cluster/join.go"
check "H: cluster.Join emits node_join audit event" "0" "$?"

# I: helper is called by cluster.RemoveNode
grep -q "db.NodeLeave" "$REPO_ROOT/internal/cluster/node.go"
check "I: cluster.RemoveNode emits node_leave audit event" "0" "$?"

# J: helper is called by db.FailoverClusterNode
# (inside the db package the constant is unqualified,
# so we grep for just "NodeDrain" not "db.NodeDrain")
grep -q "InsertClusterAudit(tx, \"skygate-staging\", NodeDrain," "$REPO_ROOT/internal/db/cluster_failover.go"
check "J: FailoverClusterNode emits node_drain audit event" "0" "$?"

# K: /admin/ha page filter includes the new actions
grep -q "'node_init'" "$REPO_ROOT/internal/feature/admin/ha.go"
check "K: /admin/ha filter includes node_init" "0" "$?"
grep -q "'node_join'" "$REPO_ROOT/internal/feature/admin/ha.go"
check "L: /admin/ha filter includes node_join" "0" "$?"
grep -q "'node_drain'" "$REPO_ROOT/internal/feature/admin/ha.go"
check "M: /admin/ha filter includes node_drain" "0" "$?"
grep -q "'node_leave'" "$REPO_ROOT/internal/feature/admin/ha.go"
check "N: /admin/ha filter includes node_leave" "0" "$?"

# O: ha.html renders the new action badges
grep -q 'eq .Action "node_init"' "$REPO_ROOT/internal/handlers/templates/admin/ha.html"
check "O: ha.html renders node_init badge" "0" "$?"
grep -q 'eq .Action "node_join"' "$REPO_ROOT/internal/handlers/templates/admin/ha.html"
check "P: ha.html renders node_join badge" "0" "$?"
grep -q 'eq .Action "node_drain"' "$REPO_ROOT/internal/handlers/templates/admin/ha.html"
check "Q: ha.html renders node_drain badge" "0" "$?"
grep -q 'eq .Action "node_leave"' "$REPO_ROOT/internal/handlers/templates/admin/ha.html"
check "R: ha.html renders node_leave badge" "0" "$?"

# S: i18n keys present (RU + EN, 4 each = 8 total)
grep -q '"ha.action_node_init"' "$REPO_ROOT/internal/i18n/catalog_admin.go"
check "S: i18n ha.action_node_init present" "0" "$?"
grep -q '"ha.action_node_join"' "$REPO_ROOT/internal/i18n/catalog_admin.go"
check "T: i18n ha.action_node_join present" "0" "$?"
grep -q '"ha.action_node_drain"' "$REPO_ROOT/internal/i18n/catalog_admin.go"
check "U: i18n ha.action_node_drain present" "0" "$?"
grep -q '"ha.action_node_leave"' "$REPO_ROOT/internal/i18n/catalog_admin.go"
check "V: i18n ha.action_node_leave present" "0" "$?"

# W: unit tests exist
[[ -f "$REPO_ROOT/internal/db/cluster_audit_b215_test.go" ]]
check "W: internal/db/cluster_audit_b215_test.go exists" "0" "$?"

# X: go test passes
if command -v "$GO" >/dev/null 2>&1 || [[ -x "$GO" ]]; then
  if $GO test ./internal/db/ ./cmd/skygate/ -run "TestClusterAuditAction|TestInsertClusterAudit" -count=1 -short >/dev/null 2>&1; then
    check "X: go test (B215 unit tests) passes" "pass" "pass"
  else
    check "X: go test (B215 unit tests) passes" "pass" "fail"
  fi
else
  echo "[skip] X: go not on PATH — run on a host with go installed (e.g. the agent)"
fi

# Y: go build works
if command -v "$GO" >/dev/null 2>&1 || [[ -x "$GO" ]]; then
  if $GO build ./... >/dev/null 2>&1; then
    check "Y: go build ./... succeeds" "pass" "pass"
  else
    check "Y: go build ./... succeeds" "pass" "fail"
  fi
else
  echo "[skip] Y: go not on PATH — run on a host with go installed"
fi

echo ""
echo "B215 B-check: $PASS passed, $FAIL failed"
[[ "$FAIL" == "0" ]]
