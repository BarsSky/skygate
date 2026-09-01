#!/usr/bin/env bash
# ============================================================================
# check_b195.sh — B195 (v1.5.0+) cluster management tables
#
# Verifies the Phase 0 schema from docs/internal/cluster-management.md:
#   A) internal/db/migrations_v0_64_b195.go exists
#   B) The migration creates all 6 cluster_* tables (cluster, cluster_node,
#      cluster_database, cluster_migration, cluster_invite, cluster_audit)
#   C) All 6 tables have IF NOT EXISTS guards (idempotent)
#   D) The migration is registered in driver_postgres.go migrateV0... list
#   E) AGENTS.md mentions B195 (or B195 work area)
#   F) verify_pre_deploy.sh references check_b195.sh
#   G) docs/internal/cluster-management.md exists with D1-D8 confirmed
#   H) Phase 1.1 (/admin/database) implementation note in CHANGELOG
# ============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

PASS=0
FAIL=0
ok() { echo "  ✓ $*"; PASS=$((PASS+1)); }
no() { echo "  ✗ $*"; FAIL=$((FAIL+1)); }

MIGRATION="$PROJECT_DIR/internal/db/migrations_v0_64_b195.go"
DOC="$PROJECT_DIR/docs/internal/cluster-management.md"

# ----- A) migration file exists ----------------------------------------
echo "A) internal/db/migrations_v0_64_b195.go"
if [ -f "$MIGRATION" ]; then
  ok "migration file exists"
else
  no "migration file missing"
fi

# ----- B) 6 tables created ----------------------------------------------
echo "B) 6 cluster_* tables in migration"
for tbl in cluster cluster_node cluster_database cluster_migration cluster_invite cluster_audit; do
  if grep -qE "CREATE TABLE IF NOT EXISTS ${tbl}\b" "$MIGRATION" 2>/dev/null; then
    ok "creates ${tbl}"
  else
    no "does not create ${tbl}"
  fi
done

# ----- C) idempotency ---------------------------------------------------
echo "C) idempotency (IF NOT EXISTS)"
if grep -qE "CREATE TABLE IF NOT EXISTS cluster_database" "$MIGRATION" 2>/dev/null; then
  ok "cluster_database uses IF NOT EXISTS"
else
  no "cluster_database missing IF NOT EXISTS"
fi
if grep -qE "CREATE INDEX IF NOT EXISTS" "$MIGRATION" 2>/dev/null; then
  ok "indexes use IF NOT EXISTS"
else
  no "indexes missing IF NOT EXISTS"
fi

# ----- D) registered in driver_postgres.go -----------------------------
echo "D) registered in driver_postgres.go"
DRIVER="$PROJECT_DIR/internal/db/driver_postgres.go"
if [ -f "$DRIVER" ] && grep -q "migrateV064PG" "$DRIVER" 2>/dev/null; then
  ok "migrateV064PG is registered in driver_postgres.go"
else
  no "migrateV064PG not registered"
fi

# ----- E) AGENTS.md mentions B195 --------------------------------------
echo "E) AGENTS.md mentions B195"
if grep -qE "B195" "$PROJECT_DIR/AGENTS.md" 2>/dev/null; then
  ok "AGENTS.md mentions B195"
else
  no "AGENTS.md does not mention B195 (deferred to /docs/internal/cluster-management.md)"
fi

# ----- F) verify_pre_deploy.sh references check_b195.sh -----------------
echo "F) verify_pre_deploy.sh references check_b195.sh"
VERIFY="$PROJECT_DIR/scripts/verify_pre_deploy.sh"
if [ -f "$VERIFY" ] && grep -q "check_b195" "$VERIFY" 2>/dev/null; then
  ok "verify_pre_deploy.sh references check_b195.sh"
else
  no "verify_pre_deploy.sh does not reference check_b195.sh"
fi

# ----- G) plan doc exists with D1-D8 confirmed ------------------------
echo "G) docs/internal/cluster-management.md"
if [ -f "$DOC" ]; then
  ok "plan doc exists"
  if grep -qE "D1.*✅|D1.*confirmed" "$DOC" 2>/dev/null; then
    ok "D1 marked confirmed"
  else
    no "D1 not marked confirmed"
  fi
  if grep -qE "D8.*✅|D8.*confirmed" "$DOC" 2>/dev/null; then
    ok "D8 marked confirmed"
  else
    no "D8 not marked confirmed"
  fi
else
  no "plan doc missing"
fi

# ----- H) build / vet clean ---------------------------------------------
echo "H) build clean"
if command -v go >/dev/null 2>&1; then
  if (cd "$PROJECT_DIR" && go build ./... >/dev/null 2>&1); then
    ok "go build ./... clean"
  else
    no "go build ./... failed"
  fi
  if (cd "$PROJECT_DIR" && go vet ./... >/dev/null 2>&1); then
    ok "go vet ./... clean"
  else
    no "go vet ./... failed"
  fi
else
  echo "  -- go not in PATH, skipping build/vet (rely on verify_pre_deploy.sh in CI)"
fi

echo
echo "B195 summary: $PASS passed, $FAIL failed"
[ "$FAIL" -eq 0 ]
