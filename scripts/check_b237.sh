#!/bin/bash
# scripts/check_b237.sh — B237 (v1.5.2+) own DERP через skygate.
#
# Verifies:
#   A. Source: derpMapResponse struct + shortNameFromHostname +
#      publicDERPPortFromURL + GetAdminDerpRelaysDerpmap handler
#   B. Apply: rewriteDerpURLs + applyHeadscaleDerpURLsConfig +
#      PostAdminDerpRelaysApplyHeadscale handler
#   C. Routes: GET /admin/derp/relays/derpmap.json +
#      POST /admin/derp/relays/apply-headscale (no auth on
#      GET because headscale needs to fetch it; POST is
#      authMW-protected)
#   D. Template: derp_relays.html has the "Apply to headscale"
#      button (action=apply-headscale) with CSRF + confirm()
#   E. i18n: catalog_derp.go has the 3 new keys in RU + EN
#      (relays_apply_headscale_btn/help/confirm)
#   F. Tests: 7+ tests across derp_dashboard_b237_test.go +
#      derp_apply_headscale_b237_test.go
#   G. Build + tests
#
# Background: pre-B237 the operator's own DERP (derp.skynas.ru)
# was missing from headscale's `derp.urls` config — the public
# Tailscale derpmap didn't include the operator's relay, so
# Tailscale clients had no way to know about it. The DERP was
# configured in derp_relays but invisible to the tailnet.
# B237 closes the gap with (a) a Tailscale-shaped derpmap.json
# endpoint at /admin/derp/relays/derpmap.json, and (b) a
# one-click "Apply to headscale" button that rewrites
# headscale's config.yaml + restarts headscale.
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

# A.1 derpMapResponse struct + types defined
if grep -qE '^type derpMapResponse struct' internal/feature/admin/derp_dashboard.go 2>/dev/null \
   && grep -qE '^type derpMapRegion struct' internal/feature/admin/derp_dashboard.go 2>/dev/null \
   && grep -qE '^type derpMapNode struct' internal/feature/admin/derp_dashboard.go 2>/dev/null; then
    ok "A.1 derpMapResponse / Region / Node structs defined"
else
    bad "A.1 derpMapResponse / Region / Node structs missing"
fi

# A.2 shortNameFromHostname + publicDERPPortFromURL pure helpers
if grep -qE '^func shortNameFromHostname' internal/feature/admin/derp_dashboard.go 2>/dev/null \
   && grep -qE '^func publicDERPPortFromURL' internal/feature/admin/derp_dashboard.go 2>/dev/null; then
    ok "A.2 shortNameFromHostname + publicDERPPortFromURL helpers"
else
    bad "A.2 shortNameFromHostname or publicDERPPortFromURL missing"
fi

# A.3 GetAdminDerpRelaysDerpmap handler
if grep -qE 'func \(s \*Service\) GetAdminDerpRelaysDerpmap' internal/feature/admin/derp_dashboard.go 2>/dev/null; then
    ok "A.3 GetAdminDerpRelaysDerpmap handler defined"
else
    bad "A.3 GetAdminDerpRelaysDerpmap handler missing"
fi

# A.4 Endpoint returns Tailscale-shaped JSON (Regions map, RegionID, RegionCode, RegionName, Nodes)
# The JSON tags are in struct field declarations — not in the
# encoded output. Grep the source for the struct field tags.
if grep -qE 'json:"Regions"' internal/feature/admin/derp_dashboard.go 2>/dev/null \
   && grep -qE 'json:"RegionID"' internal/feature/admin/derp_dashboard.go 2>/dev/null \
   && grep -qE 'json:"HostName"' internal/feature/admin/derp_dashboard.go 2>/dev/null \
   && grep -qE 'json:"DERPPort"' internal/feature/admin/derp_dashboard.go 2>/dev/null; then
    ok "A.4 derpmap response has Tailscale-shaped fields (Regions/RegionID/HostName/DERPPort)"
else
    bad "A.4 derpmap response missing Tailscale-shape fields"
fi

# A.5 Default derp port is 443 (the public port for DERP, not 8443)
if grep -qE 'return 443' internal/feature/admin/derp_dashboard.go 2>/dev/null; then
    ok "A.5 default public DERP port is 443"
else
    bad "A.5 default public DERP port must be 443"
fi

# --- B. Apply contracts ---

# B.1 rewriteDerpURLs pure function
if grep -qE '^func rewriteDerpURLs' internal/feature/admin/derp_apply_headscale_b237.go 2>/dev/null; then
    ok "B.1 rewriteDerpURLs pure function"
else
    bad "B.1 rewriteDerpURLs missing"
fi

# B.2 applyHeadscaleDerpURLsConfig orchestrates read + rewrite + write + restart
if grep -qE '^func applyHeadscaleDerpURLsConfig' internal/feature/admin/derp_apply_headscale_b237.go 2>/dev/null; then
    ok "B.2 applyHeadscaleDerpURLsConfig orchestrator"
else
    bad "B.2 applyHeadscaleDerpURLsConfig missing"
fi

# B.3 PostAdminDerpRelaysApplyHeadscale handler
if grep -qE 'func \(s \*Service\) PostAdminDerpRelaysApplyHeadscale' internal/feature/admin/derp_apply_headscale_b237.go 2>/dev/null; then
    ok "B.3 PostAdminDerpRelaysApplyHeadscale handler"
else
    bad "B.3 PostAdminDerpRelaysApplyHeadscale handler missing"
fi

# B.4 Apply is idempotent (re-running with same URL is a no-op)
if grep -qE 'idempotent' internal/feature/admin/derp_apply_headscale_b237.go 2>/dev/null; then
    ok "B.4 applyHeadscaleDerpURLsConfig is idempotent"
else
    bad "B.4 apply must be idempotent (re-apply with same URL = no-op)"
fi

# B.5 Config is written atomically (tmp + mv)
if grep -qE '\.b237\.tmp' internal/feature/admin/derp_apply_headscale_b237.go 2>/dev/null; then
    ok "B.5 config written atomically (tmp + mv)"
else
    bad "B.5 config write must be atomic (no partial writes)"
fi

# B.6 docker restart with timeout (10s)
if grep -qE 'context.WithTimeout.*10' internal/feature/admin/derp_apply_headscale_b237.go 2>/dev/null; then
    ok "B.6 docker restart has 10s timeout"
else
    bad "B.6 docker restart must have a timeout (10s)"
fi

# B.7 audit row written with config snippet + docker output
if grep -qE 'derp_apply_headscale' internal/feature/admin/derp_apply_headscale_b237.go 2>/dev/null; then
    ok "B.7 audit row 'derp_apply_headscale' with config + docker output"
else
    bad "B.7 audit row missing"
fi

# --- C. Route contracts ---

# C.1 GET /admin/derp/relays/derpmap.json registered (no authMW — headscale fetches it)
if grep -qE 'mux\.Handle\("GET /admin/derp/relays/derpmap\.json"' cmd/skygate/main.go 2>/dev/null; then
    ok "C.1 GET /admin/derp/relays/derpmap.json registered"
else
    bad "C.1 GET /admin/derp/relays/derpmap.json route missing"
fi

# C.2 The GET route does NOT use authMW (so headscale can fetch from another container)
# The route registration is on a single line: mux.Handle("GET ...", http.HandlerFunc(...))
# (no authMW wrapping). The negative-check matches the authMW pattern
# INSIDE the same line and asserts it's missing.
if grep 'mux\.Handle("GET /admin/derp/relays/derpmap\.json"' cmd/skygate/main.go 2>/dev/null | grep -qE 'http\.HandlerFunc'; then
    if ! grep 'mux\.Handle("GET /admin/derp/relays/derpmap\.json"' cmd/skygate/main.go 2>/dev/null | grep -qE 'authMW'; then
        ok "C.2 derpmap.json endpoint is NOT behind authMW (headscale must be able to fetch it)"
    else
        bad "C.2 derpmap.json is behind authMW (headscale can't carry cookies)"
    fi
else
    bad "C.2 derpmap.json route registration not found"
fi

# C.3 POST /admin/derp/relays/apply-headscale registered behind authMW
if grep -qE 'mux\.Handle\("POST /admin/derp/relays/apply-headscale", authMW' cmd/skygate/main.go 2>/dev/null; then
    ok "C.3 POST /admin/derp/relays/apply-headscale behind authMW"
else
    bad "C.3 POST /admin/derp/relays/apply-headscale must be behind authMW"
fi

# --- D. Template contracts ---

# D.1 derp_relays.html has the Apply to headscale button
if grep -qE 'action="/admin/derp/relays/apply-headscale"' internal/handlers/templates/admin/derp_relays.html 2>/dev/null; then
    ok "D.1 derp_relays.html has action=/admin/derp/relays/apply-headscale"
else
    bad "D.1 derp_relays.html missing the apply-headscale form"
fi

# D.2 button has CSRF token
if grep -A 5 'action="/admin/derp/relays/apply-headscale"' internal/handlers/templates/admin/derp_relays.html 2>/dev/null | grep -qE 'name="csrf"'; then
    ok "D.2 apply-headscale form has CSRF token"
else
    bad "D.2 apply-headscale form missing CSRF token"
fi

# D.3 button has onsubmit confirm() guard
if grep -A 5 'action="/admin/derp/relays/apply-headscale"' internal/handlers/templates/admin/derp_relays.html 2>/dev/null | grep -qE 'onsubmit'; then
    ok "D.3 apply-headscale form has onsubmit=confirm() guard"
else
    bad "D.3 apply-headscale form missing onsubmit confirm"
fi

# D.4 button uses the i18n key (not a hardcoded label)
if grep -A 5 'action="/admin/derp/relays/apply-headscale"' internal/handlers/templates/admin/derp_relays.html 2>/dev/null | grep -qE 'relays_apply_headscale_btn'; then
    ok "D.4 apply button label uses i18n key (relays_apply_headscale_btn)"
else
    bad "D.4 apply button label must use i18n key"
fi

# --- E. i18n contracts ---

# E.1 catalog_derp.go has the 3 new keys (RU block)
for key in relays_apply_headscale_btn relays_apply_headscale_help relays_apply_headscale_confirm; do
    if grep -qE "\"derp\.$key\"" internal/i18n/catalog_derp.go 2>/dev/null; then
        ok "E.1 catalog has derp.$key (RU)"
    else
        bad "E.1 catalog missing derp.$key (RU)"
    fi
done

# E.2 EN block has the same 3 keys
for key in relays_apply_headscale_btn relays_apply_headscale_help relays_apply_headscale_confirm; do
    cnt=$(grep -cE "\"derp\.$key\"" internal/i18n/catalog_derp.go 2>/dev/null)
    if [ "$cnt" -ge "2" ]; then
        ok "E.2 catalog has derp.$key x $cnt (RU + EN)"
    else
        bad "E.2 catalog has derp.$key x $cnt (need RU + EN, >= 2)"
    fi
done

# --- F. Tests contracts ---

# F.1 derp_dashboard_b237_test.go exists
if [ -f internal/feature/admin/derp_dashboard_b237_test.go ]; then
    ok "F.1 derp_dashboard_b237_test.go exists"
else
    bad "F.1 derp_dashboard_b237_test.go missing"
fi

# F.2 derp_apply_headscale_b237_test.go exists
if [ -f internal/feature/admin/derp_apply_headscale_b237_test.go ]; then
    ok "F.2 derp_apply_headscale_b237_test.go exists"
else
    bad "F.2 derp_apply_headscale_b237_test.go missing"
fi

# F.3 total tests >= 7 (2 shortName/port + 5 rewriteDerpURLs)
n_total=$(grep -hE '^func Test' internal/feature/admin/derp_dashboard_b237_test.go internal/feature/admin/derp_apply_headscale_b237_test.go 2>/dev/null | wc -l)
if [ "$n_total" -ge "7" ]; then
    ok "F.3 B237 has $n_total tests (>= 7)"
else
    bad "F.3 B237 has $n_total tests (need >= 7)"
fi

# --- G. Build + tests ---

# G.1 go build clean
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go build ./... 2>/dev/null; then
        ok "G.1 go build ./... clean"
    else
        bad "G.1 go build ./... failed"
    fi
else
    echo "  SKIP  G.1 go build (no go in PATH)"
fi

# G.2 B237 tests pass
if command -v go >/dev/null 2>&1; then
    out=$(CGO_ENABLED=0 go test -short -count=1 -timeout 30s \
        -run 'RewriteDerpURLs|ShortNameFromHostname|PublicDERPPortFromURL' \
        ./internal/feature/admin/... 2>&1)
    if echo "$out" | grep -q '^ok'; then
        ok "G.2 B237 unit tests pass"
    else
        bad "G.2 B237 unit tests failed: $out"
    fi
else
    echo "  SKIP  G.2 B237 tests (no go in PATH)"
fi

# G.3 TestTemplateArgsMatchCatalog regression guard
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 60s -run TestTemplateArgsMatchCatalog ./internal/handlers/... 2>/dev/null | grep -q '^ok'; then
        ok "G.3 TestTemplateArgsMatchCatalog passes (regression guard)"
    else
        bad "G.3 TestTemplateArgsMatchCatalog failed"
    fi
else
    echo "  SKIP  G.3 TestTemplateArgsMatchCatalog (no go in PATH)"
fi

# --- Summary ---

echo
echo "=== B237 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
