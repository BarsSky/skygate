#!/bin/bash
# scripts/check_b93.sh — invoked by verify_pre_deploy.sh B93 check.
#
# Why a separate file: same as check_b91.sh / check_b92.sh.
# The B93 check has 7+ grep-pins + 2 unit-test runs + 3
# InfraAuditIdentity tests. Inline printf in run_check
# triggers PowerShell backtick-quote issues. A dedicated
# shell script avoids all of that.
#
# Pinned contracts (v0.33.1.41, Issue 4 infra user):
#   - internal/db/migrations_v0.54.go exists + uses
#     reserved id=99 for the 'infra' portal user
#   - internal/db/migrations_pg.go: V054PG uses the same
#     reserved id=99
#   - cmd/skygate/main.go: ensureInfraUser(d, hs) wired
#     at startup
#   - internal/nodeownership/auto.go: BackfillInfra
#     function defined + called from runOneTick
#   - internal/db/queries.go: ACL username query
#     filters headscale_user_id IS NOT NULL
#   - internal/feature/admin/telegram.go: SetEgress
#     uses InfraAuditIdentity(c.UserID, c.Username)
#   - internal/feature/admin/service.go: Backend
#     interface declares InfraAuditIdentity
#   - internal/handlers/handlers_export.go: *App wrapper
#     exists
#   - Unit tests pass:
#     * 8x TestBackfillInfra_* + TestIsInfraNode
#     * 3x TestInfraAuditIdentity_*

set -e

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# Resolve go robustly (same pattern as check_b91.sh / check_b92.sh).
GO=""
if command -v go >/dev/null 2>&1; then
    GO="go"
elif [ -x "/mnt/c/Program Files/Go/bin/go.exe" ]; then
    GO="/mnt/c/Program Files/Go/bin/go.exe"
else
    echo "SKY-FAIL: go not found" >&2
    exit 1
fi

# 1. V054 SQLite migration file exists.
test -f internal/db/migrations_v0.54.go || { echo "SKY-FAIL: V054 SQLite migration missing" >&2; exit 1; }
grep -qF "migrateV054" internal/db/migrations_v0.54.go || { echo "SKY-FAIL: migrateV054 not defined" >&2; exit 1; }

# 2. V054 SQLite uses reserved id=99 (system user) to avoid
#    collision with low-id auto-assigned test rows.
grep -qF "VALUES (99, 'infra'" internal/db/migrations_v0.54.go || { echo "SKY-FAIL: V054 SQLite does NOT use reserved id=99" >&2; exit 1; }

# 3. V054 PG version exists and uses the same reserved id.
grep -qF "migrateV054PG" internal/db/migrations_pg.go || { echo "SKY-FAIL: V054 PG migration missing" >&2; exit 1; }
grep -qF "VALUES (99, \$1, \$2, 0)" internal/db/migrations_pg.go || { echo "SKY-FAIL: V054 PG does NOT use reserved id=99" >&2; exit 1; }

# 4. ensureInfraUser helper is called at startup.
grep -qF "ensureInfraUser(d, hs)" cmd/skygate/main.go || { echo "SKY-FAIL: ensureInfraUser NOT wired at startup" >&2; exit 1; }
grep -qF "func ensureInfraUser" cmd/skygate/main.go || { echo "SKY-FAIL: ensureInfraUser function not defined" >&2; exit 1; }

# 5. BackfillInfra helper exists in the autoupdater.
grep -qF "func BackfillInfra" internal/nodeownership/auto.go || { echo "SKY-FAIL: BackfillInfra function missing" >&2; exit 1; }

# 6. BackfillInfra is called from runOneTick (B77 autoupdater
#    loop body). Pins the contract that the autoupdater
#    actually attributes infra nodes — not just that the
#    helper exists.
grep -qF "BackfillInfra(dbConn, nodes)" internal/nodeownership/auto.go || { echo "SKY-FAIL: BackfillInfra NOT called from runOneTick" >&2; exit 1; }

# 7. ACL query filters out portal users without a headscale
#    link — necessary so the V054 infra row (linked at startup)
#    doesn't appear in the policy until ensureInfraUser wires it.
grep -qF "headscale_user_id IS NOT NULL" internal/db/queries.go || { echo "SKY-FAIL: ACL username query does NOT filter headscale_user_id" >&2; exit 1; }

# 8. /admin/telegram SetEgress uses InfraAuditIdentity.
grep -qF "InfraAuditIdentity(c.UserID, c.Username)" internal/feature/admin/telegram.go || { echo "SKY-FAIL: SetEgress does NOT use InfraAuditIdentity" >&2; exit 1; }

# 9. Backend interface has InfraAuditIdentity.
grep -qF "InfraAuditIdentity(fallbackUID int64" internal/feature/admin/service.go || { echo "SKY-FAIL: Backend interface does NOT declare InfraAuditIdentity" >&2; exit 1; }

# 10. *App.InfraAuditIdentity wrapper exists.
grep -qF "func (a *App) InfraAuditIdentity" internal/handlers/handlers_export.go || { echo "SKY-FAIL: *App.InfraAuditIdentity wrapper missing" >&2; exit 1; }

# 11. BackfillInfra unit tests pass.
test -f internal/nodeownership/infra_test.go || { echo "SKY-FAIL: infra_test.go missing" >&2; exit 1; }
for t in TestBackfillInfra_NoInfraUser_NoInsert TestBackfillInfra_UnlinkedInfraUser_NoInsert TestBackfillInfra_SkygateHostPrefix_InsertsRow TestBackfillInfra_InfraDevTag_InsertsRow TestBackfillInfra_RegularNode_NoInsert TestBackfillInfra_Idempotent TestBackfillInfra_PreservesExistingOwner TestIsInfraNode; do
    grep -qF "$t" internal/nodeownership/infra_test.go || { echo "SKY-FAIL: $t missing from infra_test.go" >&2; exit 1; }
done
"$GO" test -count=1 -short -run 'TestBackfillInfra|TestIsInfraNode' ./internal/nodeownership/ 2>&1 || { echo "SKY-FAIL: BackfillInfra unit tests failed" >&2; exit 1; }

# 12. InfraAuditIdentity unit tests pass.
test -f internal/feature/admin/B93_infra_audit_test.go || { echo "SKY-FAIL: B93_infra_audit_test.go missing" >&2; exit 1; }
for t in TestInfraAuditIdentity_FallsBackToCaller_WhenNoInfra TestInfraAuditIdentity_ReturnsInfra_WhenLinked TestInfraAuditIdentity_FallsBackWhenUnlinked; do
    grep -qF "$t" internal/feature/admin/B93_infra_audit_test.go || { echo "SKY-FAIL: $t missing from B93_infra_audit_test.go" >&2; exit 1; }
done
"$GO" test -count=1 -short -run 'TestInfraAuditIdentity' ./internal/feature/admin/ 2>&1 || { echo "SKY-FAIL: InfraAuditIdentity unit tests failed" >&2; exit 1; }

echo "B93 check passed: Issue 4 infra user (V054 + ensureInfraUser + BackfillInfra + InfraAuditIdentity) wired + 11 unit tests pass"
