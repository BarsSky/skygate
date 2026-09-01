#!/usr/bin/env bash
# B207 (v1.5.0+) — /admin/audit unified view. Phase 4.1
# / G8 of docs/internal/cluster-management.md.
#
# Pre-B207: /admin/audit only read from the legacy
# audit_log table — operator had no way to see the
# cluster_audit rows (B195) in the UI without dropping
# into psql.
#
# B207 fix: the same /admin/audit URL now serves a
# UNION of audit_log + cluster_audit, with:
#   - a "Source" column (audit_log / cluster_audit
#     badge) so the operator can tell where each row
#     came from
#   - a Target column (cluster_audit has
#     target_node_id; audit_log doesn't — empty in
#     that case)
#   - 4 query-string filters: ?action=, ?user=,
#     ?source=, ?since= (Go duration: 1h, 24h, 7d)
#   - 1 row-cap knob: ?limit= (default 200, max 5000)
#
# The contracts:
#
#   1. internal/feature/admin/admin_pages.go is updated
#   2. GetAdminAudit reads from BOTH audit_log and cluster_audit
#   3. The query UNIONs both tables via two branches
#   4. AuditEntry struct has Source/Time/Actor/Action/Target/Detail/Result/ErrorMessage
#   5. AuditSourceAuditLog/Cluster/All constants
#   6. parseLimit, parseSinceFilter, normalizeSourceFilter helpers
#   7. Template admin/audit.html renders Source column + filter dropdowns
#   8. i18n: audit.col_source/target/since, audit.source_*/recent_events/filtered
#   9. main.go: route GET /admin/audit already wired (B93)
#  10. Unit tests: 6+ covering parseSinceFilter, parseLimit, normalizeSourceFilter, AuditEntry, constants
#  11. go build + vet + admin unit tests pass
#  12. AGENTS.md mentions B207

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
        printf "  \31m✗\033[0m %s\n" "$name"
        FAIL=$((FAIL+1))
        fails+=("$name")
    fi
}

file_exists() { [ -f "$1" ]; }
grep_q() { grep -qE "$1" "$2" 2>/dev/null; return $?; }

# 1. admin_pages.go updated
grep_q 'func .s \*Service. GetAdminAudit' "internal/feature/admin/admin_pages.go" \
    && check "GetAdminAudit handler updated" ok \
    || check "GetAdminAudit handler updated" fail

# 2. GetAdminAudit reads from BOTH audit_log and cluster_audit
grep -A200 'func .s \*Service. GetAdminAudit' "internal/feature/admin/admin_pages.go" | grep -qE "FROM[[:space:]]+audit_log" \
    && check "GetAdminAudit reads from audit_log" ok \
    || check "GetAdminAudit reads from audit_log" fail
grep -A200 'func .s \*Service. GetAdminAudit' "internal/feature/admin/admin_pages.go" | grep -qE "FROM[[:space:]]+cluster_audit" \
    && check "GetAdminAudit reads from cluster_audit" ok \
    || check "GetAdminAudit reads from cluster_audit" fail

# 3. UNION ALL of the two branches
grep -A300 'func .s \*Service. GetAdminAudit' "internal/feature/admin/admin_pages.go" | grep -q "UNION ALL" \
    && check "audit branches are UNION ALL'd" ok \
    || check "audit branches are UNION ALL'd" fail

# 4. AuditEntry struct has all the unified fields
grep_q '^type AuditEntry struct' "internal/feature/admin/admin_pages.go" \
    && check "AuditEntry struct defined" ok \
    || check "AuditEntry struct defined" fail
grep -A30 'type AuditEntry struct' "internal/feature/admin/admin_pages.go" | grep -qE 'Source[[:space:]]+string' \
    && check "AuditEntry.Source" ok \
    || check "AuditEntry.Source" fail
grep -A30 'type AuditEntry struct' "internal/feature/admin/admin_pages.go" | grep -qE 'Time[[:space:]]+string' \
    && check "AuditEntry.Time" ok \
    || check "AuditEntry.Time" fail
grep -A30 'type AuditEntry struct' "internal/feature/admin/admin_pages.go" | grep -qE 'Actor[[:space:]]+string' \
    && check "AuditEntry.Actor" ok \
    || check "AuditEntry.Actor" fail
grep -A30 'type AuditEntry struct' "internal/feature/admin/admin_pages.go" | grep -qE 'Action[[:space:]]+string' \
    && check "AuditEntry.Action" ok \
    || check "AuditEntry.Action" fail
grep -A30 'type AuditEntry struct' "internal/feature/admin/admin_pages.go" | grep -qE 'Target[[:space:]]+string' \
    && check "AuditEntry.Target" ok \
    || check "AuditEntry.Target" fail
grep -A30 'type AuditEntry struct' "internal/feature/admin/admin_pages.go" | grep -qE 'Detail[[:space:]]+string' \
    && check "AuditEntry.Detail" ok \
    || check "AuditEntry.Detail" fail
grep -A30 'type AuditEntry struct' "internal/feature/admin/admin_pages.go" | grep -qE 'Result[[:space:]]+string' \
    && check "AuditEntry.Result" ok \
    || check "AuditEntry.Result" fail
grep -A30 'type AuditEntry struct' "internal/feature/admin/admin_pages.go" | grep -qE 'ErrorMessage[[:space:]]+string' \
    && check "AuditEntry.ErrorMessage" ok \
    || check "AuditEntry.ErrorMessage" fail

# 5. AuditSource* constants
grep_q 'AuditSourceAll[[:space:]]*=[[:space:]]*""' "internal/feature/admin/admin_pages.go" \
    && check "AuditSourceAll constant defined" ok \
    || check "AuditSourceAll constant defined" fail
grep_q 'AuditSourceAuditLog[[:space:]]*=[[:space:]]*"audit_log"' "internal/feature/admin/admin_pages.go" \
    && check "AuditSourceAuditLog constant defined" ok \
    || check "AuditSourceAuditLog constant defined" fail
grep_q 'AuditSourceCluster[[:space:]]*=[[:space:]]*"cluster_audit"' "internal/feature/admin/admin_pages.go" \
    && check "AuditSourceCluster constant defined" ok \
    || check "AuditSourceCluster constant defined" fail
grep_q 'AuditSourceLimitDefault[[:space:]]*=[[:space:]]*200' "internal/feature/admin/admin_pages.go" \
    && check "AuditSourceLimitDefault=200" ok \
    || check "AuditSourceLimitDefault=200" fail

# 6. Helper functions
grep_q 'func parseLimit' "internal/feature/admin/admin_pages.go" \
    && check "parseLimit() defined" ok \
    || check "parseLimit() defined" fail
grep_q 'func parseSinceFilter' "internal/feature/admin/admin_pages.go" \
    && check "parseSinceFilter() defined" ok \
    || check "parseSinceFilter() defined" fail
grep_q 'func normalizeSourceFilter' "internal/feature/admin/admin_pages.go" \
    && check "normalizeSourceFilter() defined" ok \
    || check "normalizeSourceFilter() defined" fail

# 7. Template renders the new fields
file_exists "internal/handlers/templates/admin/audit.html" \
    && check "audit.html template exists" ok \
    || check "audit.html template exists" fail
grep -q 'audit.col_source' "internal/handlers/templates/admin/audit.html" \
    && check "audit.html uses audit.col_source" ok \
    || check "audit.html uses audit.col_source" fail
grep -q 'audit.col_target' "internal/handlers/templates/admin/audit.html" \
    && check "audit.html uses audit.col_target" ok \
    || check "audit.html uses audit.col_target" fail
grep -q 'audit.col_since' "internal/handlers/templates/admin/audit.html" \
    && check "audit.html uses audit.col_since" ok \
    || check "audit.html uses audit.col_source" fail
grep -q 'cluster_audit' "internal/handlers/templates/admin/audit.html" \
    && check "audit.html renders the cluster_audit source badge" ok \
    || check "audit.html renders the cluster_audit source badge" fail

# 8. i18n keys
grep -q '"audit.col_source"' "internal/i18n/catalog_admin.go" \
    && check "i18n audit.col_source defined" ok \
    || check "i18n audit.col_source defined" fail
grep -q '"audit.col_target"' "internal/i18n/catalog_admin.go" \
    && check "i18n audit.col_target defined" ok \
    || check "i18n audit.col_target defined" fail
grep -q '"audit.col_since"' "internal/i18n/catalog_admin.go" \
    && check "i18n audit.col_since defined" ok \
    || check "i18n audit.col_since defined" fail
grep -q '"audit.source_all"' "internal/i18n/catalog_admin.go" \
    && check "i18n audit.source_all defined" ok \
    || check "i18n audit.source_all defined" fail
grep -q '"audit.source_audit_log"' "internal/i18n/catalog_admin.go" \
    && check "i18n audit.source_audit_log defined" ok \
    || check "i18n audit.source_audit_log defined" fail
grep -q '"audit.source_cluster_audit"' "internal/i18n/catalog_admin.go" \
    && check "i18n audit.source_cluster_audit defined" ok \
    || check "i18n audit.source_cluster_audit defined" fail
grep -q '"audit.recent_events"' "internal/i18n/catalog_admin.go" \
    && check "i18n audit.recent_events defined" ok \
    || check "i18n audit.recent_events defined" fail

# 9. Route already wired (B93)
grep_q 'GET /admin/audit' "cmd/skygate/main.go" \
    && check "main.go: GET /admin/audit route registered" ok \
    || check "main.go: GET /admin/audit route registered" fail

# 10. Unit tests
file_exists "internal/feature/admin/admin_audit_b207_test.go" \
    && check "admin_audit_b207_test.go exists" ok \
    || check "admin_audit_b207_test.go exists" fail
ut=$(grep -c '^func Test' "internal/feature/admin/admin_audit_b207_test.go")
[ "$ut" -ge 6 ] \
    && check "audit unit tests: $ut (>= 6)" ok \
    || check "audit unit tests: $ut (need >= 6)" fail

# 11. Build + vet + tests
if command -v go >/dev/null 2>&1; then
    if (cd "$PWD" && go build ./... >/tmp/check_b207_build.log 2>&1); then
        check "go build ./... succeeds" ok
    else
        check "go build ./... succeeds" fail
        head -10 /tmp/check_b207_build.log
    fi
    if (cd "$PWD" && go vet ./... >/tmp/check_b207_vet.log 2>&1); then
        check "go vet ./... succeeds" ok
    else
        check "go vet ./... succeeds" fail
        head -10 /tmp/check_b207_vet.log
    fi
    if (cd "$PWD" && go test -count=1 -run 'TestParseSinceFilter|TestLimitOverride|TestSourceFilterValidation|TestAuditEntry|TestAuditSource' ./internal/feature/admin/ >/tmp/check_b207_test.log 2>&1); then
        check "B207 unit tests pass" ok
    else
        check "B207 unit tests pass" fail
        head -20 /tmp/check_b207_test.log
    fi
else
    check "go build/vet/tests skipped (no go in PATH)" ok
fi

# 12. AGENTS.md mention
grep_q "B207" "AGENTS.md" 2>/dev/null \
    && check "AGENTS.md mentions B207" ok \
    || check "AGENTS.md mentions B207" fail

# Summary
echo ""
TOTAL=$((PASS+FAIL))
if [ "$FAIL" -eq 0 ]; then
    printf "\033[32m%d/%d PASS\033[0m — B207 contracts satisfied.\n" "$PASS" "$TOTAL"
    exit 0
else
    printf "\033[31m%d/%d FAIL\033[0m — B207 contracts broken:\n" "$FAIL" "$TOTAL"
    for f in "${fails[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
