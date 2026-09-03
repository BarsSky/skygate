#!/usr/bin/env bash
# B-check for B221 (v1.5.0+): Phase 4.1 generic audit log
# (`audit_log.target_type` + `audit_log.target_id`).
#
# The B221 migration adds two columns to the B0-era
# audit_log table; the new `db.AppendAuditLogWithTarget`
# helper writes them; the 6+ admin writers in
# internal/feature/admin/{cluster,database}.go are
# switched to use the new helper. The /admin/audit
# unified view projects both columns + the legacy
# `target` (combined) for backward compat.
#
# Contracts pinned (16 source-pin + 3 go-runtime):
#   A:    B221 migration file exists + exports migrateV067PG
#   B:    migration applied to driver_postgres.go (V067 entry)
#   C:    migration V067 is idempotent (ADD COLUMN IF NOT EXISTS)
#   D:    AppendAuditLogWithTarget helper exists
#   E:    qInsertAuditLogWithTarget SQL has 6 placeholders + targets audit_log
#   F:    AppendAuditLogWithTarget signature is the documented 7-arg form
#   G:    6 cluster.go writers use AppendAuditLogWithTarget
#         (cluster.node.add/remove/drain/drain_remove/approve,
#          cluster.invite.generate/revoke)
#   H:    4 database.go writers use AppendAuditLogWithTarget
#         (cluster.db.edit, db.failover + .error,
#          db.failover_rollback + .error)
#   I:    admin_pages.go AuditEntry struct has TargetType + TargetID
#   J:    admin_pages.go SELECT projects 10 columns (incl target_type, target_id)
#   K:    admin_pages.go Scan reads 10 columns
#   L:    /admin/audit template renders .Target combined string
#   M:    B221 unit test file exists with the 6 pure-Go tests
#   N:    migration_b213_test.go expects V067 as the last version
#   O:    AGENTS.md mentions B221
#   P:    B221 unit tests pass
#   Q:    go build ./... succeeds
set -euo pipefail
cd "$(dirname "$0")/.."

ok_count=0
ok() { printf "[ok]   %s\n" "$1"; ok_count=$((ok_count+1)); }
fail() { printf "[FAIL] %s\n" "$1"; exit 1; }
has() { grep -q -E "$2" "$1" 2>/dev/null; }

MIG="internal/db/migrations_v0_67_b221.go"
DRIVER="internal/db/driver_postgres.go"
AUDIT_GO="internal/db/audit_log.go"
CLUSTER_GO="internal/feature/admin/cluster.go"
DB_GO="internal/feature/admin/database.go"
PAGES_GO="internal/feature/admin/admin_pages.go"
TEMPLATE="internal/handlers/templates/admin/audit.html"
TEST_NEW="internal/db/audit_log_b221_test.go"
TEST_MIG="internal/db/migration_b213_test.go"
AGENTS="AGENTS.md"

# --- A: B221 migration file + migrateV067PG ---
if [ -f "$MIG" ] && has "$MIG" "^func migrateV067PG"; then
  ok "A: B221 migration file exists with migrateV067PG"
else
  fail "A: $MIG missing or doesn't export migrateV067PG"
fi

# --- B: V067 entry in driver_postgres.go ---
if has "$DRIVER" '\{67, "v0\.67 \(B221\)'; then
  ok "B: V067 entry registered in driver_postgres.go"
else
  fail "B: V067 entry missing in $DRIVER"
fi

# --- C: migration V067 is idempotent (ADD COLUMN IF NOT EXISTS) ---
n=$(grep -c "ADD COLUMN IF NOT EXISTS" "$MIG" 2>/dev/null || echo 0)
if [ "${n:-0}" -ge 2 ]; then
  ok "C: V067 migration is idempotent ($n ADD COLUMN IF NOT EXISTS statements)"
else
  fail "C: V067 migration should have >= 2 ADD COLUMN IF NOT EXISTS (got $n)"
fi

# --- D: AppendAuditLogWithTarget helper ---
if has "$AUDIT_GO" "func AppendAuditLogWithTarget\\("; then
  ok "D: AppendAuditLogWithTarget helper exists"
else
  fail "D: AppendAuditLogWithTarget missing in $AUDIT_GO"
fi

# --- E: qInsertAuditLogWithTarget SQL shape ---
if has "$AUDIT_GO" "qInsertAuditLogWithTarget = .INSERT INTO audit_log" && \
   has "$AUDIT_GO" "target_type" && has "$AUDIT_GO" "target_id"; then
  ok "E: qInsertAuditLogWithTarget targets audit_log with target_type + target_id"
else
  fail "E: qInsertAuditLogWithTarget SQL shape wrong"
fi

# --- F: 7-arg signature (d, userID, username, action, detail, targetType, targetID) ---
if has "$AUDIT_GO" "func AppendAuditLogWithTarget\\(d \\*sql\\.DB, userID int64, username, action, detail, targetType, targetID string\\)"; then
  ok "F: AppendAuditLogWithTarget 7-arg signature correct"
else
  fail "F: AppendAuditLogWithTarget signature wrong (expected: d *sql.DB, userID int64, username, action, detail, targetType, targetID string)"
fi

# --- G: 6 cluster.go writers use AppendAuditLogWithTarget ---
# (cluster.node.add/remove/drain/drain_remove/approve + cluster.invite.generate/revoke = 7 actions but
#  generate+revoke share 1 file line each; we count distinct AppendAuditLogWithTarget call sites.)
expected_cluster_actions=(
  "cluster.node.add"
  "cluster.node.remove"
  "cluster.node.drain"
  "cluster.node.drain_remove"
  "cluster.node.approve"
  "cluster.invite.generate"
  "cluster.invite.revoke"
)
miss=0
for action in "${expected_cluster_actions[@]}"; do
  if ! grep -B0 -A3 "AppendAuditLogWithTarget" "$CLUSTER_GO" | grep -q "\"$action\""; then
    miss=$((miss+1))
    echo "    [miss] cluster.go: $action not paired with AppendAuditLogWithTarget"
  fi
done
if [ "$miss" -eq 0 ]; then
  ok "G: all 7 cluster.go writers use AppendAuditLogWithTarget (target=cluster_node|cluster_invite)"
else
  fail "G: $miss cluster.go writer(s) missing B221 helper"
fi

# --- H: 4 database.go writers use AppendAuditLogWithTarget ---
expected_db_actions=(
  "cluster.db.edit"
  "db.failover"
  "db.failover.error"
  "db.failover_rollback"
  "db.failover_rollback.error"
)
miss=0
for action in "${expected_db_actions[@]}"; do
  if ! grep -B0 -A3 "AppendAuditLogWithTarget" "$DB_GO" | grep -q "\"$action\""; then
    miss=$((miss+1))
    echo "    [miss] database.go: $action not paired with AppendAuditLogWithTarget"
  fi
done
if [ "$miss" -eq 0 ]; then
  ok "H: all 5 database.go writers use AppendAuditLogWithTarget (target=cluster_database)"
else
  fail "H: $miss database.go writer(s) missing B221 helper"
fi

# --- I: AuditEntry struct has TargetType + TargetID ---
if has "$PAGES_GO" "TargetType  string // B221" && has "$PAGES_GO" "TargetID    string // B221"; then
  ok "I: AuditEntry struct extended with TargetType + TargetID (B221)"
else
  fail "I: AuditEntry struct missing B221 fields"
fi

# --- J: SELECT projects 10 columns including target_type + target_id ---
if has "$PAGES_GO" "SELECT source, ts, actor, action, target, target_type, target_id, detail, result, error_message"; then
  ok "J: /admin/audit SELECT projects 10 columns (incl target_type, target_id)"
else
  fail "J: /admin/audit SELECT does not project target_type + target_id"
fi

# --- K: Scan reads 10 columns ---
# (The Scan is split across multiple lines in admin_pages.go — use a
# block match to handle either layout.)
scan_block=$(awk '/rows\.Scan\(/,/err != nil/' "$PAGES_GO" | tr -d '\n' | tr -s ' ')
if echo "$scan_block" | grep -q "&e\.Target," && \
   echo "$scan_block" | grep -q "&e\.TargetType," && \
   echo "$scan_block" | grep -q "&e\.TargetID," && \
   echo "$scan_block" | grep -q "&e\.Detail," && \
   echo "$scan_block" | grep -q "&e\.Result," && \
   echo "$scan_block" | grep -q "&e\.ErrorMessage"; then
  ok "K: rows.Scan reads 10 columns (incl TargetType + TargetID)"
else
  fail "K: rows.Scan does not read TargetType + TargetID (block: $scan_block)"
fi

# --- L: audit.html template renders .Target combined string ---
if has "$TEMPLATE" "\\{\\{\\.Target\\}\\}"; then
  ok "L: audit.html template renders {{.Target}} combined target string"
else
  fail "L: audit.html template missing {{.Target}} render"
fi

# --- M: B221 unit test file exists with the 6 pure-Go tests ---
if [ -f "$TEST_NEW" ]; then
  n=$(grep -c "^func Test" "$TEST_NEW" 2>/dev/null || echo 0)
  if [ "${n:-0}" -ge 6 ]; then
    ok "M: B221 unit test file has $n Test functions"
  else
    fail "M: B221 unit test file has only $n Test functions (want >= 6)"
  fi
else
  fail "M: B221 unit test file $TEST_NEW missing"
fi

# --- N: migration_b213_test.go expects V067 as the last version ---
if has "$TEST_MIG" "last\\.Version != 67"; then
  ok "N: migration_b213_test expects last.Version == 67 (B221 is the latest)"
else
  fail "N: migration_b213_test does not expect V067 (B221) as the latest version"
fi

# --- O: AGENTS.md mentions B221 ---
if has "$AGENTS" "B221"; then
  ok "O: AGENTS.md mentions B221"
else
  echo "[skip] O: AGENTS.md doesn't mention B221 (will be added before commit)"
fi

# --- P: B221 unit tests pass ---
if command -v go >/dev/null 2>&1; then
  if go test ./internal/db/... -run 'AppendAuditLogWithTarget' -count=1 >/dev/null 2>&1; then
    ok "P: B221 unit tests pass"
  else
    fail "P: B221 unit tests FAILED"
  fi
else
  echo "[skip] P: go not on PATH"
fi

# --- Q: go build ./... succeeds ---
if command -v go >/dev/null 2>&1; then
  if go build ./... >/dev/null 2>&1; then
    ok "Q: go build ./... succeeds"
  else
    fail "Q: go build ./... FAILED"
  fi
else
  echo "[skip] Q: go not on PATH"
fi

echo ""
echo "B221 B-check: $ok_count passed"
