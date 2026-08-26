#!/bin/bash
# scripts/check_b189.sh — B189 (v1.5.2) DERP Health Dashboard.
#
# Verifies:
#   A. Source: internal/derphealth/ package + handlers + CLI + cron + template
#   B. Migration: migrateV062PG registered + derp_health table exists
#   C. Routes: GET /admin/derp/dashboard + POST /admin/derp/dashboard/refresh
#   D. CLI: case "derp-probe" + runDerpProbe function
#   E. Layout: sidebar link to /admin/derp/dashboard
#   F. Build + tests
#
# Exit 0 on all green, non-zero on any FAIL.

set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

PASS=0
FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

# --- A. Source contracts ---

# A.1 internal/derphealth/types.go exists with DERPInfo + HealthRow
if grep -qE 'type DERPInfo struct' internal/derphealth/types.go 2>/dev/null \
   && grep -qE 'type HealthRow struct' internal/derphealth/types.go 2>/dev/null; then
    ok "A.1 derphealth/types.go has DERPInfo + HealthRow"
else
    bad "A.1 derphealth/types.go missing DERPInfo or HealthRow"
fi

# A.2 internal/derphealth/map.go: FetchPublicDERPs + FetchOwnDERPs + FetchAllDERPs
for fn in FetchPublicDERPs FetchOwnDERPs FetchAllDERPs; do
    if grep -qE "^func $fn\(" internal/derphealth/map.go 2>/dev/null; then
        ok "A.2 derphealth/map.go has $fn"
    else
        bad "A.2 derphealth/map.go missing $fn"
    fi
done

# A.3 internal/derphealth/probe.go: ProbeOne + ProbeAll
for fn in ProbeOne ProbeAll; do
    if grep -qE "^func $fn\(" internal/derphealth/probe.go 2>/dev/null; then
        ok "A.3 derphealth/probe.go has $fn"
    else
        bad "A.3 derphealth/probe.go missing $fn"
    fi
done

# A.4 internal/derphealth/probe.go: ProbeOneTLSConfig override + PersistToDB
if grep -qE 'ProbeOneTLSConfig' internal/derphealth/probe.go 2>/dev/null; then
    ok "A.4 derphealth/probe.go has ProbeOneTLSConfig override"
else
    bad "A.4 derphealth/probe.go missing ProbeOneTLSConfig"
fi
if grep -qE '^func PersistToDB\(' internal/derphealth/probe.go 2>/dev/null; then
    ok "A.4 derphealth/probe.go has PersistToDB"
else
    bad "A.4 derphealth/probe.go missing PersistToDB"
fi

# A.5 internal/derphealth/cron.go: StartCron + RunOnceNow
for fn in StartCron RunOnceNow; do
    if grep -qE "^func $fn\(" internal/derphealth/cron.go 2>/dev/null; then
        ok "A.5 derphealth/cron.go has $fn"
    else
        bad "A.5 derphealth/cron.go missing $fn"
    fi
done

# A.6 internal/feature/admin/derp_dashboard.go: handlers
for fn in GetAdminDerpDashboard PostAdminDerpDashboardRefresh; do
    if grep -qE "^func \(s \*Service\) $fn\(" internal/feature/admin/derp_dashboard.go 2>/dev/null; then
        ok "A.6 derp_dashboard.go has $fn"
    else
        bad "A.6 derp_dashboard.go missing $fn"
    fi
done

# A.7 cmd/skygate/derp_probe.go: runDerpProbe
if grep -qE '^func runDerpProbe\(' cmd/skygate/derp_probe.go 2>/dev/null; then
    ok "A.7 cmd/skygate/derp_probe.go has runDerpProbe"
else
    bad "A.7 cmd/skygate/derp_probe.go missing runDerpProbe"
fi

# A.8 template exists
if [ -f internal/handlers/templates/admin/derp_dashboard.html ]; then
    ok "A.8 derp_dashboard.html template exists"
else
    bad "A.8 derp_dashboard.html template missing"
fi

# A.9 test file exists with 4+ tests
n_tests=$(grep -cE '^func Test' internal/derphealth/derphealth_test.go 2>/dev/null || echo 0)
if [ "$n_tests" -ge "4" ]; then
    ok "A.9 derphealth_test.go has $n_tests tests (>= 4)"
else
    bad "A.9 derphealth_test.go has $n_tests tests (need >= 4)"
fi

# --- B. Migration contracts ---

# B.1 migrateV062PG defined in migrations_pg.go
if grep -qE '^func migrateV062PG\(' internal/db/migrations_pg.go 2>/dev/null; then
    ok "B.1 migrateV062PG function defined"
else
    bad "B.1 migrateV062PG function missing"
fi

# B.2 migrateV062PG creates derp_health table with region_id PK
if grep -qE 'CREATE TABLE IF NOT EXISTS derp_health' internal/db/migrations_pg.go 2>/dev/null; then
    ok "B.2 migrateV062PG creates derp_health table"
else
    bad "B.2 migrateV062PG doesn't create derp_health"
fi

# B.3 migrateV062PG registered in driver_postgres chain
if grep -qE 'migrateV062PG,' internal/db/driver_postgres.go 2>/dev/null; then
    ok "B.3 migrateV062PG registered in driver_postgres chain"
else
    bad "B.3 migrateV062PG NOT in driver_postgres chain"
fi

# B.4 derp_health table exists in live DB (if reachable)
DSN=$(grep '^SKYGATE_DB_DSN' .env 2>/dev/null | head -1 | cut -d= -f2-)
if [ -n "$DSN" ]; then
    export PGPASSWORD=$(echo "$DSN" | sed -n 's|.*://[^:]*:\([^@]*\)@.*|\1|p')
    if psql "$DSN" -A -t -c "SELECT 1 FROM information_schema.tables WHERE table_schema='public' AND table_name='derp_health';" 2>/dev/null | grep -q '^1$'; then
        ok "B.4 derp_health table exists in live DB"
    else
        bad "B.4 derp_health table NOT in live DB"
    fi
else
    echo "  SKIP  B.4 derp_health live check (no DSN)"
fi

# B.5 V062 marked applied in applied_migrations
if [ -n "$DSN" ]; then
    if psql "$DSN" -A -t -c "SELECT 1 FROM applied_migrations WHERE version=62;" 2>/dev/null | grep -q '^1$'; then
        ok "B.5 V062 marked applied in applied_migrations"
    else
        bad "B.5 V062 NOT in applied_migrations"
    fi
else
    echo "  SKIP  B.5 V062 applied check (no DSN)"
fi

# --- C. Route contracts ---

# C.1 GET /admin/derp/dashboard registered
if grep -qE 'GET /admin/derp/dashboard[^/]' cmd/skygate/main.go 2>/dev/null; then
    ok "C.1 GET /admin/derp/dashboard route"
else
    bad "C.1 GET /admin/derp/dashboard route missing"
fi

# C.2 POST /admin/derp/dashboard/refresh registered
if grep -qE 'POST /admin/derp/dashboard/refresh' cmd/skygate/main.go 2>/dev/null; then
    ok "C.2 POST /admin/derp/dashboard/refresh route"
else
    bad "C.2 POST /admin/derp/dashboard/refresh route missing"
fi

# C.3 routes are behind authMW
if grep -qE 'GET /admin/derp/dashboard[^/].*authMW' cmd/skygate/main.go 2>/dev/null \
   || grep -qB 1 'GET /admin/derp/dashboard' cmd/skygate/main.go 2>/dev/null | grep -q authMW; then
    ok "C.3 dashboard route is behind authMW"
else
    bad "C.3 dashboard route may not be behind authMW"
fi

# --- D. CLI subcommand contracts ---

# D.1 case "derp-probe" present in main.go switch
if grep -qE 'case "derp-probe":' cmd/skygate/main.go 2>/dev/null; then
    ok "D.1 case \"derp-probe\" present in main.go"
else
    bad "D.1 case \"derp-probe\" missing from main.go"
fi

# D.2 derp-probe subcommand calls runDerpProbe (use awk because
# the case label and the call are 6 lines apart due to comments;
# grep -A may fail depending on the shell's quoting of the
# embedded quotes).
if awk '/case "derp-probe":/{flag=1; next} flag && /runDerpProbe/{print; exit} flag && /^\s*case /{flag=0}' cmd/skygate/main.go | grep -q runDerpProbe; then
    ok "D.2 derp-probe calls runDerpProbe"
else
    bad "D.2 derp-probe doesn't call runDerpProbe"
fi

# --- E. Layout / navigation contracts ---

# E.1 nav link to /admin/derp/dashboard (sidebar or layout)
if grep -rqE 'admin/derp/dashboard' internal/handlers/templates/ 2>/dev/null; then
    ok "E.1 sidebar / layout has link to /admin/derp/dashboard"
else
    bad "E.1 no template link to /admin/derp/dashboard (informational; operator can navigate via URL)"
fi

# --- F. Build + tests ---

# F.1 go build clean
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go build ./... 2>/dev/null; then
        ok "F.1 go build ./... clean"
    else
        bad "F.1 go build ./... failed"
    fi
else
    echo "  SKIP  F.1 go build (no go in PATH)"
fi

# F.2 derphealth unit tests pass
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 30s ./internal/derphealth/... 2>/dev/null | grep -q '^ok'; then
        ok "F.2 derphealth unit tests pass"
    else
        bad "F.2 derphealth unit tests failed"
    fi
else
    echo "  SKIP  F.2 derphealth tests (no go in PATH)"
fi

# --- Summary ---

echo
echo "=== B189 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
