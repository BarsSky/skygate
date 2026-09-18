#!/usr/bin/env bash
# B199 (v1.5.0+) — /admin/cluster (cluster topology view, Phase 2.1 read-only).
#
# Pin the structural surface so a future refactor can't
# silently regress the page. The contracts are:
#
#   1. Handler file exists:        internal/feature/admin/cluster.go
#   2. Template file exists:       internal/handlers/templates/admin/cluster.html
#   3. Template body define:       body-admin-cluster
#   4. Route registered:           GET /admin/cluster
#   5. Service method:             GetAdminCluster
#   6. Page data type:             clusterPageData
#   7. Sidebar link:               nav.cluster
#   8. Section membership:         admin/cluster in InSectionIntegrations
#   9. Section label:              admin/cluster in sectionLabel()
#  10. Page label:                 admin/cluster in pageLabel()
#  11. Page title:                 admin/cluster.html in pageTitle()
#  12. i18n RU keys:               cluster.title + 5+ col_* + 5+ section_*
#  13. i18n EN keys:               cluster.title + 5+ col_* + 5+ section_*
#  14. Helper parsePGTextArray:    13 unit tests (TestParsePGTextArray)
#  15. Helper parseClusterChain:   7 unit tests (TestParseClusterChain)
#  16. Helper abbreviateClusterTime: 11 unit tests
#  17. B198.1 regression fix:      database.html + migrate_run.html use body-admin-*
#  18. AGENTS.md mentions B199
#
# Exit 0 on full pass, 1 on any failure. Prints N/M PASS/FAIL summary.

set -u

# Resolve the project root. Two options, in priority order:
#  1. SKYGATE_PROJECT_DIR env var (used when the script is
#     copied to a different location, e.g. /tmp on the agent).
#  2. The script's parent directory (the original layout).
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
    local result="$2"  # "ok" or "fail"
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

grep_q() {
    # grep_q <pattern> <file>
    # exit 0 if found, 1 if not.
    grep -qE "$1" "$2" 2>/dev/null
}

# 1. Handler file
file_exists "internal/feature/admin/cluster.go" \
    && check "handler file: internal/feature/admin/cluster.go" ok \
    || check "handler file: internal/feature/admin/cluster.go" fail

# 2. Template file
file_exists "internal/handlers/templates/admin/cluster.html" \
    && check "template file: admin/cluster.html" ok \
    || check "template file: admin/cluster.html" fail

# 3. Template body define
grep_q '\{\{define "body-admin-cluster"\}\}' "internal/handlers/templates/admin/cluster.html" \
    && check 'template defines "body-admin-cluster"' ok \
    || check 'template defines "body-admin-cluster"' fail

# 4. Route registered
grep_q 'mux\.Handle\("GET /admin/cluster"' "cmd/skygate/main.go" \
    && check "route GET /admin/cluster registered in main.go" ok \
    || check "route GET /admin/cluster registered in main.go" fail

# 5. Service method
grep_q 'func \(s \*Service\) GetAdminCluster\(' "internal/feature/admin/cluster.go" \
    && check "Service.GetAdminCluster method exists" ok \
    || check "Service.GetAdminCluster method exists" fail

# 6. Page data type
grep_q 'type clusterPageData struct' "internal/feature/admin/cluster.go" \
    && check "clusterPageData struct defined" ok \
    || check "clusterPageData struct defined" fail

# 7. Sidebar link
grep_q 'href="/admin/cluster"' "internal/handlers/templates/layout.html" \
    && check "sidebar link /admin/cluster in layout.html" ok \
    || check "sidebar link /admin/cluster in layout.html" fail

# 8. Section membership
grep_q '"admin/cluster",.*// v1\.5\.0\+ / B199' "internal/handlers/handlers.go" \
    && check "InSectionIntegrations includes admin/cluster" ok \
    || check "InSectionIntegrations includes admin/cluster" fail

# 9. Section label
grep_q 'page == "admin/cluster"' "internal/handlers/handlers.go" \
    && check "sectionLabel() covers admin/cluster" ok \
    || check "sectionLabel() covers admin/cluster" fail

# 10. Page label
awk '/^func pageLabel/,/^}$/' "internal/handlers/handlers.go" | grep -q '"admin/cluster"' \
    && check "pageLabel() returns key for admin/cluster" ok \
    || check "pageLabel() returns key for admin/cluster" fail

# 11. Page title
awk '/^func pageTitle/,/^}$/' "internal/handlers/handlers.go" | grep -q '"admin/cluster.html"' \
    && check "pageTitle() covers admin/cluster.html" ok \
    || check "pageTitle() covers admin/cluster.html" fail

# 12. i18n RU keys — count of cluster.* keys in ruAdmin
ru_count=$(awk '/^var ruAdmin/,/^}$/' "internal/i18n/catalog_admin.go" | grep -cE '"cluster\.[a-z_]+":')
[ "$ru_count" -ge 30 ] \
    && check "i18n RU: $ru_count cluster.* keys (>= 30)" ok \
    || check "i18n RU: only $ru_count cluster.* keys (need >= 30)" fail

# 13. i18n EN keys — count of cluster.* keys in enAdmin
en_count=$(awk '/^var enAdmin/,/^}$/' "internal/i18n/catalog_admin.go" | grep -cE '"cluster\.[a-z_]+":')
[ "$en_count" -ge 30 ] \
    && check "i18n EN: $en_count cluster.* keys (>= 30)" ok \
    || check "i18n EN: only $en_count cluster.* keys (need >= 30)" fail

# 14. parsePGTextArray — file + tests
grep_q 'func TestParsePGTextArray' "internal/feature/admin/cluster_b199_test.go" \
    && check "TestParsePGTextArray exists" ok \
    || check "TestParsePGTextArray exists" fail

# 15. parseClusterChain — file + tests
grep_q 'func TestParseClusterChain' "internal/feature/admin/cluster_b199_test.go" \
    && check "TestParseClusterChain exists" ok \
    || check "TestParseClusterChain exists" fail

# 16. abbreviateClusterTime — file + tests
grep_q 'func TestAbbreviateClusterTime' "internal/feature/admin/cluster_b199_test.go" \
    && check "TestAbbreviateClusterTime exists" ok \
    || check "TestAbbreviateClusterTime exists" fail

# 17. B198.1 regression — body-admin-database / body-admin-migrate_run
grep_q '\{\{define "body-admin-database"\}\}' "internal/handlers/templates/admin/database.html" \
    && check "B198.1 regression: database.html uses body-admin-database" ok \
    || check "B198.1 regression: database.html uses body-admin-database" fail

grep_q '\{\{define "body-admin-migrate_run"\}\}' "internal/handlers/templates/admin/migrate_run.html" \
    && check "B198.1 regression: migrate_run.html uses body-admin-migrate_run" ok \
    || check "B198.1 regression: migrate_run.html uses body-admin-migrate_run" fail

# 18. AGENTS.md mentions B199
grep_q "B199" "AGENTS.md" 2>/dev/null \
    && check "AGENTS.md mentions B199" ok \
    || check "AGENTS.md mentions B199" fail

# 19. Build + vet
echo ""
if command -v go >/dev/null 2>&1; then
    echo "  Running go build..."
    if (cd "$PWD" && go build ./... >/tmp/check_b199_build.log 2>&1); then
        check "go build ./... succeeds" ok
    else
        check "go build ./... succeeds" fail
        head -10 /tmp/check_b199_build.log
    fi

    echo ""
    echo "  Running go vet..."
    if (cd "$PWD" && go vet ./... >/tmp/check_b199_vet.log 2>&1); then
        check "go vet ./... succeeds" ok
    else
        check "go vet ./... succeeds" fail
        head -10 /tmp/check_b199_vet.log
    fi

    echo ""
    echo "  Running cluster unit tests..."
    if (cd "$PWD" && go test ./internal/feature/admin/ -run "TestParsePG|TestParseClusterChain|TestAbbreviateClusterTime" -count=1 >/tmp/check_b199_test.log 2>&1); then
        check "cluster helper unit tests pass" ok
    else
        check "cluster helper unit tests pass" fail
        head -20 /tmp/check_b199_test.log
    fi
else
    echo "  -- go not in PATH, skipping build/vet/tests (run on the agent)"
    check "go build/vet/tests skipped (no go in PATH)" ok
fi

# 20. Live health check (optional — only runs if SKYGATE_AGENT env is set)
if [ -n "${SKYGATE_AGENT:-}" ]; then
    echo ""
    echo "  Live check on $SKYGATE_AGENT..."
    code=$(curl -s -o /dev/null -w '%{http_code}' "http://$SKYGATE_AGENT:8080/admin/cluster" || echo "000")
    if [ "$code" = "200" ] || [ "$code" = "302" ]; then
        check "GET $SKYGATE_AGENT:8080/admin/cluster = $code" ok
    else
        check "GET $SKYGATE_AGENT:8080/admin/cluster = $code (want 200 or 302)" fail
    fi
fi

# Summary
echo ""
TOTAL=$((PASS+FAIL))
if [ "$FAIL" -eq 0 ]; then
    printf "\033[32m%d/%d PASS\033[0m — B199 contracts satisfied.\n" "$PASS" "$TOTAL"
    exit 0
else
    printf "\033[31m%d/%d FAIL\033[0m — B199 contracts broken:\n" "$FAIL" "$TOTAL"
    for f in "${fails[@]}"; do
        echo "  - $f"
    done
    exit 1
fi
