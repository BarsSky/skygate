#!/bin/bash
# scripts/check_b236.sh — B236 (v1.5.2+) /admin/tailscale
# subnet-routes management.
#
# Verifies:
#   A. Source: tailscaleAdvertisedRoutes + detectHostLAN +
#      cidrOverlaps + dockerBridgeRanges + handleTailscaleSetAdvertiseRoutes
#   B. Template: admin/tailscale.html has the new form +
#      advertised-routes list section
#   C. i18n: catalog_tailscale.go has the 9 new keys in
#      RU + EN (advertise_routes_heading/empty/approved_note/
#      help/label/placeholder/hint/save/clear/confirm)
#   D. Tests: tailscale_b236_test.go has 7+ tests
#   E. Build + tests
#
# Background: pre-B236, skygate-host-1 had
# --advertise-routes=172.17.0.0/16,192.168.13.0/24,172.18.0.0/16
# set manually (the 192.168.13.0/24 shadowed skyworker's
# direct LAN route to 192.168.13.67 on 2026-09-04). The
# /admin/tailscale page had no UI to view/manage these
# routes — the operator had to SSH to fix it.
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

# A.1 tailscaleAdvertisedRoutes function exists
if grep -qE '^func tailscaleAdvertisedRoutes' internal/feature/admin/tailscale.go 2>/dev/null; then
    ok "A.1 tailscaleAdvertisedRoutes function defined"
else
    bad "A.1 tailscaleAdvertisedRoutes function missing"
fi

# A.2 TailscaleState has AdvertisedRoutes + Approved + Source fields
for fld in AdvertisedRoutes AdvertisedRoutesApproved AdvertisedRoutesSource; do
    if grep -qE "^\s*$fld " internal/feature/admin/tailscale.go 2>/dev/null; then
        ok "A.2 TailscaleState has $fld field"
    else
        bad "A.2 TailscaleState missing $fld field"
    fi
done

# A.3 detectHostLAN function exists
if grep -qE '^func detectHostLAN' internal/feature/admin/tailscale.go 2>/dev/null; then
    ok "A.3 detectHostLAN function defined"
else
    bad "A.3 detectHostLAN function missing"
fi

# A.4 cidrOverlaps function exists
if grep -qE '^func cidrOverlaps' internal/feature/admin/tailscale.go 2>/dev/null; then
    ok "A.4 cidrOverlaps function defined"
else
    bad "A.4 cidrOverlaps function missing"
fi

# A.5 dockerBridgeRanges has the B236 deny list (must include 172.17 + 172.18)
if grep -qE '172\.17\.0\.0/16' internal/feature/admin/tailscale.go 2>/dev/null \
   && grep -qE '172\.18\.0\.0/16' internal/feature/admin/tailscale.go 2>/dev/null; then
    ok "A.5 dockerBridgeRanges includes 172.17.0.0/16 + 172.18.0.0/16"
else
    bad "A.5 dockerBridgeRanges missing 172.17 or 172.18 (would re-introduce the skyworker bug)"
fi

# A.6 handleTailscaleSetAdvertiseRoutes function exists
if grep -qE 'func \(s \*Service\) handleTailscaleSetAdvertiseRoutes' internal/feature/admin/tailscale.go 2>/dev/null; then
    ok "A.6 handleTailscaleSetAdvertiseRoutes function defined"
else
    bad "A.6 handleTailscaleSetAdvertiseRoutes function missing"
fi

# A.7 set_advertise_routes action is wired in the POST switch
if grep -qE 'case "set_advertise_routes":' internal/feature/admin/tailscale.go 2>/dev/null; then
    ok "A.7 set_advertise_routes action in PostAdminTailscale switch"
else
    bad "A.7 set_advertise_routes action not in PostAdminTailscale switch"
fi

# A.8 handler validates against host LAN (the B236 main rule)
# The check looks for the "пересекается с LAN" Russian error message
# in the handler (the validator's user-facing flash on reject).
if grep -qE 'пересекается с LAN' internal/feature/admin/tailscale.go 2>/dev/null; then
    ok "A.8 handler refuses self-LAN advertise (the B236 main rule)"
else
    bad "A.8 handler missing the self-LAN check (regression — skyworker bug class)"
fi

# A.9 handler refuses docker bridge (the B236 secondary rule)
# Look for the "docker bridge" Russian error message.
if grep -qE 'docker bridge' internal/feature/admin/tailscale.go 2>/dev/null; then
    n=$(grep -cE 'docker bridge' internal/feature/admin/tailscale.go 2>/dev/null)
    if [ "$n" -ge "2" ]; then
        ok "A.9 handler refuses docker-bridge advertise (the B236 secondary rule) — $n occurrences"
    else
        bad "A.9 handler has the variable but not the rejection message"
    fi
else
    bad "A.9 handler missing the docker-bridge check (regression — skyworker bug class)"
fi

# A.10 handler uses tailscale set --advertise-routes (idempotent replace, not add)
# The actual code uses []string{"set", "--advertise-routes=" ...
if grep -qE 'advertise-routes=' internal/feature/admin/tailscale.go 2>/dev/null; then
    n_set=$(grep -cE '"set", "--advertise-routes=' internal/feature/admin/tailscale.go 2>/dev/null)
    if [ "$n_set" -ge "1" ]; then
        ok "A.10 handler uses 'tailscale set --advertise-routes=' (idempotent replace)"
    else
        bad "A.10 handler uses tailscale up form — B185 'all-or-nothing' trap"
    fi
else
    bad "A.10 handler does NOT call tailscale set --advertise-routes at all"
fi

# A.11 audit row writes before/after for grep-ability
if grep -qE 'tailscale_advertise_routes' internal/feature/admin/tailscale.go 2>/dev/null; then
    ok "A.11 handler writes 'tailscale_advertise_routes' audit row with before/after"
else
    bad "A.11 handler must write audit row (operator must be able to grep the change)"
fi

# --- B. Template contracts ---

# B.1 advertised-routes form exists
if grep -qE 'action.*set_advertise_routes' internal/handlers/templates/admin/tailscale.html 2>/dev/null; then
    ok "B.1 /admin/tailscale form has action=set_advertise_routes"
else
    bad "B.1 /admin/tailscale form missing set_advertise_routes action"
fi

# B.2 form has the CIDR input field
if grep -qE 'name="advertise_routes"' internal/handlers/templates/admin/tailscale.html 2>/dev/null; then
    ok "B.2 form has name=advertise_routes input"
else
    bad "B.2 form missing the advertise_routes input"
fi

# B.3 form has a Clear button
if grep -qE 'tailscale\.advertise_routes_clear' internal/handlers/templates/admin/tailscale.html 2>/dev/null; then
    ok "B.3 form has the Clear button (i18n key)"
else
    bad "B.3 form missing the Clear button"
fi

# B.4 form has the onsubmit confirm() (per B185 'all-or-nothing' style)
# Look in the 3 lines BEFORE the action=set_advertise_routes
# (the onsubmit attribute is on the <form ...> tag, which
# appears 3 lines above the hidden input).
if grep -B 3 'action.*set_advertise_routes' internal/handlers/templates/admin/tailscale.html 2>/dev/null | grep -q 'onsubmit'; then
    ok "B.4 form has onsubmit=confirm() guard"
else
    bad "B.4 form missing onsubmit=confirm() guard"
fi

# --- C. i18n contracts ---

# C.1 catalog_tailscale.go has the 10 new keys (RU block)
for key in advertise_routes_heading advertise_routes_empty advertise_routes_approved_note \
           advertise_routes_help advertise_routes_label advertise_routes_placeholder \
           advertise_routes_hint advertise_routes_save advertise_routes_clear \
           advertise_routes_confirm; do
    # The "advertise_routes_help" / "advertise_routes_hint" keys are
    # typically followed by a value with <code> tags so we just
    # count occurrences of the key string.
    if grep -qE "\"tailscale\.$key\"" internal/i18n/catalog_tailscale.go 2>/dev/null; then
        ok "C.1 catalog has tailscale.$key (RU)"
    else
        bad "C.1 catalog missing tailscale.$key (RU)"
    fi
done

# C.2 en block has the same 10 keys
for key in advertise_routes_heading advertise_routes_empty advertise_routes_approved_note \
           advertise_routes_help advertise_routes_label advertise_routes_placeholder \
           advertise_routes_hint advertise_routes_save advertise_routes_clear \
           advertise_routes_confirm; do
    if grep -qE "\"tailscale\.$key\"" internal/i18n/catalog_tailscale.go 2>/dev/null; then
        # We need to count: the key should appear at least 2x (once in
        # the RU map, once in the enTailscale map).
        cnt=$(grep -cE "\"tailscale\.$key\"" internal/i18n/catalog_tailscale.go 2>/dev/null)
        if [ "$cnt" -ge "2" ]; then
            ok "C.2 catalog has tailscale.$key x $cnt (RU + EN)"
        else
            bad "C.2 catalog has tailscale.$key x $cnt — need RU + EN (>= 2)"
        fi
    else
        bad "C.2 catalog missing tailscale.$key"
    fi
done

# --- D. Tests contracts ---

# D.1 tailscale_b236_test.go exists
if [ -f internal/feature/admin/tailscale_b236_test.go ]; then
    ok "D.1 tailscale_b236_test.go exists"
else
    bad "D.1 tailscale_b236_test.go missing"
fi

# D.2 7+ tests present
n_tests=$(grep -cE '^func Test' internal/feature/admin/tailscale_b236_test.go 2>/dev/null || echo 0)
if [ "$n_tests" -ge "7" ]; then
    ok "D.2 tailscale_b236_test.go has $n_tests tests (>= 7)"
else
    bad "D.2 tailscale_b236_test.go has $n_tests tests (need >= 7)"
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

# E.2 admin unit tests pass
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 60s -run 'CidrOverlaps|DetectHostLAN|DockerBridgeRanges|TailscaleAdvertisedRoutes|ValidateAdvertiseRoutes' ./internal/feature/admin/... 2>/dev/null | grep -q '^ok'; then
        ok "E.2 B236 unit tests pass"
    else
        bad "E.2 B236 unit tests failed"
    fi
else
    echo "  SKIP  E.2 B236 unit tests (no go in PATH)"
fi

# E.3 TestTemplateArgsMatchCatalog regression guard (the {{t}} vs {{tf}} bug from v0.16.6)
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 60s -run TestTemplateArgsMatchCatalog ./internal/handlers/... 2>/dev/null | grep -q '^ok'; then
        ok "E.3 TestTemplateArgsMatchCatalog passes (regression guard for the new {{tf}} with %d)"
    else
        bad "E.3 TestTemplateArgsMatchCatalog failed (a {{t}} vs {{tf}} is wrong somewhere)"
    fi
else
    echo "  SKIP  E.3 TestTemplateArgsMatchCatalog (no go in PATH)"
fi

# --- Summary ---

echo
echo "=== B236 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
