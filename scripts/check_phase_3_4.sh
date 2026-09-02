#!/usr/bin/env bash
# Phase 3.4 (v1.5.0+) — "Force cluster node failover" button
# on /admin/ha. Operator-driven counterpart to the B204
# HA elector's automatic failover_recommend.
#
# Phase 3.4 of docs/internal/cluster-management.md. Closes
# the "the elector recommends, the operator has no in-UI
# way to act on the recommendation" gap. Pre-Phase 3.4 the
# only path to swap the skygate primary was SSH into the
# agent + psql + UPDATE cluster_node + write the audit
# row manually. Post-Phase 3.4 the operator clicks a button
# on /admin/ha → the handler calls db.FailoverClusterNode
# (single transaction) → the audit row appears in
# /admin/ha's "Last 20 events" table immediately.
#
# The contracts:
#
#   1. internal/db/cluster_failover.go exists
#   2. db.FailoverClusterNode signature: func(d *sql.DB, targetID, actor, reason string) (fromID, toID string, err error)
#   3. db.ErrNoPrimary sentinel exists
#   4. db.ErrNotEligibleForFailover sentinel exists
#   5. db.FailoverClusterNode uses a transaction (sql.Tx)
#   6. internal/feature/admin/ha.go has PostAdminHAClusterFailover handler
#   7. adminSvc.PostAdminHAClusterFailover is registered in main.go as POST /admin/ha/cluster/failover
#   8. The handler is behind authMW (admin-only)
#   9. internal/feature/admin/ha.go has haClusterNodeRow struct
#  10. haPageData has ClusterNodes []haClusterNodeRow field
#  11. collectHAPageData populates data.ClusterNodes from cluster_node table
#  12. The eligibility logic checks: state=ready, roles contains skygate-standby, roles does NOT contain skygate
#  13. The template (internal/handlers/templates/admin/ha.html) renders a per-row "Promote" form for eligible rows
#  14. i18n keys added (catalog_admin.go RU + EN): ha.cluster_failover, ha.cluster_failover_help, ha.cluster_failover_btn, etc.
#  15. Build + vet + tests pass
#  16. AGENTS.md mentions Phase 3.4 (or B212)
#  17. verify_pre_deploy.sh has a run_check for this

set -u

if [ -n "${SKYGATE_PROJECT_DIR:-}" ]; then
  cd "$SKYGATE_PROJECT_DIR"
else
  cd "$(dirname "$0")/.."
fi

PASS=0
FAIL=0
fails=()

check() {
  local name="$1"
  local result="$2"
  if [ "$result" = "ok" ]; then
    printf "  \033[32m✓\033[0m %s\n" "$name"
    PASS=$((PASS+1))
  else
    printf "  \033[31m✗\033[0m %s\n" "$name"
    FAIL=$((FAIL+1))
    fails+=("$name")
  fi
}

file_exists() { [ -f "$1" ]; }
file_grep() { grep -qE "$1" "$2" 2>/dev/null; return $?; }

# 1. cluster_failover.go exists
file_exists "internal/db/cluster_failover.go" \
  && check "internal/db/cluster_failover.go exists" ok \
  || check "internal/db/cluster_failover.go exists" fail

# 2. FailoverClusterNode signature
if file_exists "internal/db/cluster_failover.go"; then
  file_grep "^func FailoverClusterNode\(d \*sql\.DB, targetID, actor, reason string\)" "internal/db/cluster_failover.go" \
    && check "db.FailoverClusterNode signature" ok \
    || check "db.FailoverClusterNode signature" fail
fi

# 3 + 4. ErrNoPrimary + ErrNotEligibleForFailover
if file_exists "internal/db/cluster_failover.go"; then
  file_grep "^var ErrNoPrimary" "internal/db/cluster_failover.go" \
    && file_grep "^var ErrNotEligibleForFailover" "internal/db/cluster_failover.go" \
    && check "db.ErrNoPrimary + db.ErrNotEligibleForFailover sentinels defined" ok \
    || check "db.ErrNoPrimary + db.ErrNotEligibleForFailover sentinels defined" fail
fi

# 5. Uses sql.Tx
if file_exists "internal/db/cluster_failover.go"; then
  file_grep "tx, err := d\.BeginTx" "internal/db/cluster_failover.go" \
    && file_grep "tx\.Commit" "internal/db/cluster_failover.go" \
    && check "db.FailoverClusterNode uses a transaction" ok \
    || check "db.FailoverClusterNode uses a transaction" fail
fi

# 6. Handler exists
file_grep "func \(s \*Service\) PostAdminHAClusterFailover" "internal/feature/admin/ha.go" \
  && check "admin.Service.PostAdminHAClusterFailover handler exists" ok \
  || check "admin.Service.PostAdminHAClusterFailover handler exists" fail

# 7. Route registered
file_grep 'POST /admin/ha/cluster/failover' "cmd/skygate/main.go" \
  && check "main.go registers POST /admin/ha/cluster/failover" ok \
  || check "main.go registers POST /admin/ha/cluster/failover" fail

# 8. Behind authMW
if file_grep 'authMW\(http\.HandlerFunc\(adminSvc\.PostAdminHAClusterFailover\)' "cmd/skygate/main.go"; then
  check "POST /admin/ha/cluster/failover is behind authMW" ok
else
  check "POST /admin/ha/cluster/failover is behind authMW" fail
fi

# 9. haClusterNodeRow struct
file_grep "^type haClusterNodeRow struct" "internal/feature/admin/ha.go" \
  && check "admin.haClusterNodeRow struct defined" ok \
  || check "admin.haClusterNodeRow struct defined" fail

# 10. ClusterNodes field
file_grep "ClusterNodes\s+\[\]haClusterNodeRow" "internal/feature/admin/ha.go" \
  && check "haPageData has ClusterNodes []haClusterNodeRow field" ok \
  || check "haPageData has ClusterNodes []haClusterNodeRow field" fail

# 11. collectHAPageData populates
file_grep "FROM cluster_node" "internal/feature/admin/ha.go" \
  && file_grep "data\.ClusterNodes = append" "internal/feature/admin/ha.go" \
  && check "collectHAPageData populates data.ClusterNodes from cluster_node" ok \
  || check "collectHAPageData populates data.ClusterNodes from cluster_node" fail

# 12. Eligibility logic (state, skygate-standby, not skygate)
file_grep "EligibleForPromote = true" "internal/feature/admin/ha.go" \
  && file_grep "EligibleForPromote = false" "internal/feature/admin/ha.go" \
  && file_grep "skygate-standby" "internal/feature/admin/ha.go" \
  && check "Eligibility logic checks state=ready + skygate-standby role" ok \
  || check "Eligibility logic checks state=ready + skygate-standby role" fail

# 13. Template renders Promote form
file_grep "action=\"/admin/ha/cluster/failover\"" "internal/handlers/templates/admin/ha.html" \
  && check "Template renders the per-row Promote form" ok \
  || check "Template renders the per-row Promote form" fail

# 14. i18n keys (both RU + EN)
i18n_ok=1
for key in ha.cluster_failover ha.cluster_failover_help ha.cluster_failover_btn; do
  count=$(grep -c "\"$key\":" "internal/i18n/catalog_admin.go")
  if [ "$count" -lt "2" ]; then
    i18n_ok=0
    break
  fi
done
[ "$i18n_ok" = "1" ] && check "i18n keys present in both RU + EN" ok \
  || check "i18n keys present in both RU + EN" fail

# 15. build + vet + tests
GO=""
if command -v go >/dev/null 2>&1; then
  GO="go"
else
  for cand in \
    "C:/Program Files/Go/bin/go.exe" \
    "/c/Program Files/Go/bin/go.exe" \
    "/c/Program Files/Go/bin/go" \
    "/mnt/c/Program Files/Go/bin/go.exe" \
    "/usr/local/go/bin/go" \
    "/usr/lib/go/bin/go"; do
    [ -x "$cand" ] && GO="$cand" && break
  done
fi
if [ -n "$GO" ]; then
  if "$GO" build ./... >/dev/null 2>&1; then
    check "go build ./... passes" ok
  else
    check "go build ./... passes" fail
  fi
  if "$GO" vet ./internal/db/... ./internal/feature/admin/... >/dev/null 2>&1; then
    check "go vet on db + admin packages passes" ok
  else
    check "go vet on db + admin packages passes" fail
  fi
  if "$GO" test ./internal/db/... ./internal/feature/admin/... -count=1 >/dev/null 2>&1; then
    check "go test on db + admin packages passes" ok
  else
    check "go test on db + admin packages passes" fail
  fi
else
  check "go binary not found (skipping build/vet/test)" fail
fi

# 16. AGENTS.md
if [ -f "AGENTS.md" ] && grep -qE "Phase 3\.4|Force cluster node failover" "AGENTS.md"; then
  check "AGENTS.md mentions Phase 3.4 / Force cluster node failover" ok
else
  check "AGENTS.md mentions Phase 3.4 / Force cluster node failover" fail
fi

# 17. verify_pre_deploy.sh
if [ -f "scripts/verify_pre_deploy.sh" ] && grep -q 'run_check "Phase_3_4"' "scripts/verify_pre_deploy.sh"; then
  check "verify_pre_deploy.sh has Phase_3_4 run_check" ok
else
  check "verify_pre_deploy.sh has Phase_3_4 run_check" fail
fi

echo
echo "=== Phase 3.4: ${PASS} pass, ${FAIL} fail ==="
if [ "$FAIL" -gt "0" ]; then
  echo "FAILURES:"
  for f in "${fails[@]}"; do
    echo "  - $f"
  done
  exit 1
fi
exit 0
