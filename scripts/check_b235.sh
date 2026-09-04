#!/bin/bash
# scripts/check_b235.sh — B235 (v1.5.2) DERP HostName fix + main-page ping.
#
# Verifies:
#   A. Source: HostName + Name fields + PublicMapURL var + bestHealthyDERP
#   B. Templates: dashboard.html real ping + admin derp_dashboard.html tooltip
#   C. i18n: catalog_my key (region_id sub-text) + catalog_admin col_id_help
#   D. Tests: 5 new B235 tests in map_b235_test.go
#   E. Build + tests
#
# Background: pre-B235, FetchPublicDERPs used `n.Name` ("1f", "22w") as
# the Host field — that's a Tailscale-internal SHORT label, NOT a
# resolvable DNS name. Every public DERP probe failed with "no such
# host" and /admin/derp/dashboard showed 28/28 as degraded. The
# /dashboard hero also showed a hardcoded "waw" placeholder.
# B235 pins `n.HostName` (FQDN like "derp1f.tailscale.com") as the
# network host, preserves `n.Name` for display, and the hero now
# queries the same derp_health source as the admin dashboard.
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

# A.1 DERPInfo has separate Host + Name fields (the B235 split)
if grep -qE '^\s*Host\s+string' internal/derphealth/types.go 2>/dev/null \
   && grep -qE '^\s*Name\s+string' internal/derphealth/types.go 2>/dev/null; then
    ok "A.1 DERPInfo has Host + Name fields"
else
    bad "A.1 DERPInfo missing Host or Name field"
fi

# A.2 map.go uses n.HostName (NOT n.Name) for Host — the actual B235 fix
if grep -qE 'host\s*:=\s*n\.HostName' internal/derphealth/map.go 2>/dev/null; then
    ok "A.2 map.go uses n.HostName (B235 fix)"
else
    bad "A.2 map.go does NOT use n.HostName (regression)"
fi

# A.3 map.go preserves the short label as Name (separate field)
if grep -qE 'Name:\s+n\.Name' internal/derphealth/map.go 2>/dev/null; then
    ok "A.3 map.go preserves n.Name as Name field"
else
    bad "A.3 map.go missing Name: n.Name preservation"
fi

# A.4 PublicMapURL is a var (not const) so unit tests can override
if grep -qE '^var PublicMapURL' internal/derphealth/map.go 2>/dev/null; then
    ok "A.4 PublicMapURL is var (not const)"
else
    bad "A.4 PublicMapURL is const — B235 tests can't override"
fi

# A.5 bestHealthyDERP exists in dashboard.go
if grep -qE 'func \(s \*Service\) bestHealthyDERP\(' internal/feature/my/dashboard.go 2>/dev/null; then
    ok "A.5 dashboard.go has bestHealthyDERP"
else
    bad "A.5 dashboard.go missing bestHealthyDERP"
fi

# A.6 TailnetMetrics has ActiveDERPLatencyMs + ActiveDERPRegionID fields
if grep -qE 'ActiveDERPLatencyMs\s+int' internal/feature/my/dashboard.go 2>/dev/null \
   && grep -qE 'ActiveDERPRegionID\s+int' internal/feature/my/dashboard.go 2>/dev/null; then
    ok "A.6 TailnetMetrics has ActiveDERPLatencyMs + ActiveDERPRegionID"
else
    bad "A.6 TailnetMetrics missing ActiveDERPLatencyMs or ActiveDERPRegionID"
fi

# A.7 bestHealthyDERP query orders by is_own DESC, latency_ms ASC
if awk '/func \(s \*Service\) bestHealthyDERP\(/,/^}/' internal/feature/my/dashboard.go | grep -qE 'is_own DESC'; then
    ok "A.7 bestHealthyDERP orders by is_own DESC, latency ASC"
else
    bad "A.7 bestHealthyDERP missing is_own-priority ordering"
fi

# --- B. Template contracts ---

# B.1 dashboard.html renders ActiveDERPLatencyMs (not hardcoded "waw")
if grep -qE 'ActiveDERPLatencyMs' internal/handlers/templates/dashboard.html 2>/dev/null; then
    ok "B.1 dashboard.html renders ActiveDERPLatencyMs"
else
    bad "B.1 dashboard.html missing ActiveDERPLatencyMs render"
fi

# B.2 dashboard.html renders ActiveDERPRegionID (the B235 region_id sub-text)
if grep -qE 'ActiveDERPRegionID' internal/handlers/templates/dashboard.html 2>/dev/null; then
    ok "B.2 dashboard.html renders ActiveDERPRegionID"
else
    bad "B.2 dashboard.html missing ActiveDERPRegionID render"
fi

# B.3 dashboard.html uses {{tf (not {{t) for the region_id placeholder
if grep -qE '\{\{tf "dashboard\.metric_active_derp_sub_with_id"' internal/handlers/templates/dashboard.html 2>/dev/null; then
    ok "B.3 dashboard.html uses {{tf for the region_id placeholder"
else
    bad "B.3 dashboard.html uses {{t (not {{tf) — TestTemplateArgsMatchCatalog will fail"
fi

# B.4 admin derp_dashboard.html has the col_id_help tooltip
if grep -qE 'col_id_help' internal/handlers/templates/admin/derp_dashboard.html 2>/dev/null; then
    ok "B.4 admin derp_dashboard.html has col_id_help tooltip"
else
    bad "B.4 admin derp_dashboard.html missing col_id_help tooltip"
fi

# B.5 admin derp_dashboard.html renders .Name pill when present
if grep -qE '\.Name' internal/handlers/templates/admin/derp_dashboard.html 2>/dev/null; then
    ok "B.5 admin derp_dashboard.html renders .Name pill"
else
    bad "B.5 admin derp_dashboard.html missing .Name display"
fi

# --- C. i18n contracts ---

# C.1 catalog_my has metric_active_derp_sub_with_id (RU + EN)
if grep -qE '"dashboard\.metric_active_derp_sub_with_id"' internal/i18n/catalog_my.go 2>/dev/null; then
    ok "C.1 catalog_my has metric_active_derp_sub_with_id"
else
    bad "C.1 catalog_my missing metric_active_derp_sub_with_id"
fi

# C.2 catalog_admin has col_id_help (RU + EN)
if grep -qE '"derp_dashboard\.col_id_help"' internal/i18n/catalog_admin.go 2>/dev/null; then
    ok "C.2 catalog_admin has col_id_help"
else
    bad "C.2 catalog_admin missing col_id_help"
fi

# C.3 metric_active_derp_sub_with_id value contains %d (the region_id placeholder)
val=$(grep -A 1 '"dashboard\.metric_active_derp_sub_with_id"' internal/i18n/catalog_my.go | head -2 | grep -oE 'Tailscale region_id %d' || true)
if [ -n "$val" ]; then
    ok "C.3 metric_active_derp_sub_with_id value has %d placeholder"
else
    bad "C.3 metric_active_derp_sub_with_id missing %d placeholder"
fi

# C.4 col_id_help value mentions region_id (operator's "что это за id" question)
val=$(grep -A 1 '"derp_dashboard\.col_id_help"' internal/i18n/catalog_admin.go | head -2 | grep -oiE 'region_id' || true)
if [ -n "$val" ]; then
    ok "C.4 col_id_help value mentions region_id"
else
    bad "C.4 col_id_help value missing region_id"
fi

# --- D. Tests contracts ---

# D.1 map_b235_test.go exists
if [ -f internal/derphealth/map_b235_test.go ]; then
    ok "D.1 map_b235_test.go exists"
else
    bad "D.1 map_b235_test.go missing"
fi

# D.2 map_b235_test.go has the HostIsFQDN_NotShortLabel regression test
if grep -qE 'TestFetchPublicDERPs_HostIsFQDN_NotShortLabel' internal/derphealth/map_b235_test.go 2>/dev/null; then
    ok "D.2 TestFetchPublicDERPs_HostIsFQDN_NotShortLabel exists"
else
    bad "D.2 TestFetchPublicDERPs_HostIsFQDN_NotShortLabel missing"
fi

# D.3 map_b235_test.go has 5+ tests
n_tests=$(grep -cE '^func Test' internal/derphealth/map_b235_test.go 2>/dev/null || echo 0)
if [ "$n_tests" -ge "5" ]; then
    ok "D.3 map_b235_test.go has $n_tests tests (>= 5)"
else
    bad "D.3 map_b235_test.go has $n_tests tests (need >= 5)"
fi

# --- E. Build + tests ---

# E.1 go build clean
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go build ./... 2>/dev/null; then
        ok "E.1 go build ./... clean"
    else
        bad "E.1 go build ./... failed"
    fi
else
    echo "  SKIP  E.1 go build (no go in PATH)"
fi

# E.2 derphealth unit tests pass
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 30s ./internal/derphealth/... 2>/dev/null | grep -q '^ok'; then
        ok "E.2 derphealth unit tests pass"
    else
        bad "E.2 derphealth unit tests failed"
    fi
else
    echo "  SKIP  E.2 derphealth tests (no go in PATH)"
fi

# E.3 handlers templates test passes (regression guard for the {{t}} vs {{tf}} fix)
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 60s -run TestTemplateArgsMatchCatalog ./internal/handlers/... 2>/dev/null | grep -q '^ok'; then
        ok "E.3 TestTemplateArgsMatchCatalog passes"
    else
        bad "E.3 TestTemplateArgsMatchCatalog failed"
    fi
else
    echo "  SKIP  E.3 TestTemplateArgsMatchCatalog (no go in PATH)"
fi

# --- Summary ---

echo
echo "=== B235 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
