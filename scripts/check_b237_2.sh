#!/bin/bash
# scripts/check_b237_2.sh — B237.2 (v1.5.2+) correct
# Public IP display on /admin/derp.
#
# Verifies:
#   A. Source: resolvePublicDERPIP helper + WhiteIPSource
#      struct field + SKYGATE_DERP_HOSTNAME env var support
#   B. Template: derp.html shows WhiteIPSource annotation
#   C. i18n: 8 new keys (RU + EN) explaining the source
#   D. Tests: 4+ unit tests in derp_b237_2_test.go
#   E. Build + tests
#
# Background: pre-B237.2, the "Публичный IP" field on
# /admin/derp showed the skygate container's egress IP
# (172.18.0.x docker bridge) instead of the actual
# public IP Tailscale clients dial. Operator's 2026-09-04
# report: "на скрине указан неверный ip адрес (он
# относится к контейнеру, а не публичному адресу
# ресурса)". B237.2 closes the gap with a DNS lookup
# of the derper's hostname (the authoritative source
# for "where clients reach us").
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

# A.1 WhiteIPSource field exists on DerpStatus
if grep -qE '^\s*WhiteIPSource\s+string' internal/feature/admin/derp.go 2>/dev/null; then
    ok "A.1 DerpStatus has WhiteIPSource field"
else
    bad "A.1 DerpStatus missing WhiteIPSource field"
fi

# A.2 resolvePublicDERPIP function exists
if grep -qE '^func resolvePublicDERPIP' internal/feature/admin/derp.go 2>/dev/null; then
    ok "A.2 resolvePublicDERPIP function defined"
else
    bad "A.2 resolvePublicDERPIP function missing"
fi

# A.3 resolvePublicDERPIP uses net.LookupHost
# The function is ~50 lines; use awk range instead of grep -A.
if awk '/^func resolvePublicDERPIP/,/^}/' internal/feature/admin/derp.go 2>/dev/null | grep -qE 'net\.LookupHost'; then
    ok "A.3 resolvePublicDERPIP uses net.LookupHost (the DNS source of truth)"
else
    bad "A.3 resolvePublicDERPIP must use net.LookupHost (the only source that returns the real public IP)"
fi

# A.4 SKYGATE_DERP_HOSTNAME env var honored
if awk '/^func resolvePublicDERPIP/,/^}/' internal/feature/admin/derp.go 2>/dev/null | grep -qE 'SKYGATE_DERP_HOSTNAME'; then
    ok "A.4 SKYGATE_DERP_HOSTNAME env var read"
else
    bad "A.4 SKYGATE_DERP_HOSTNAME env var must be read (operator's hostname source)"
fi

# A.5 detectEgressIP() kept as last-resort fallback
if grep -qE '^func detectEgressIP' internal/feature/admin/derp.go 2>/dev/null; then
    ok "A.5 detectEgressIP() kept as last-resort fallback"
else
    bad "A.5 detectEgressIP() must be kept as last-resort fallback"
fi

# A.6 placeholder hostname "derp.example.com" is skipped
if awk '/^func resolvePublicDERPIP/,/^}/' internal/feature/admin/derp.go 2>/dev/null | grep -qE 'derp\.example\.com'; then
    ok "A.6 placeholder hostname 'derp.example.com' is skipped"
else
    bad "A.6 placeholder hostname 'derp.example.com' must be skipped (otherwise the resolver would DNS-resolve example.com and show a wrong IP)"
fi

# A.7 WhiteIPSource marker strings are emitted
if awk '/^func resolvePublicDERPIP/,/^}/' internal/feature/admin/derp.go 2>/dev/null | grep -qE 'dns:env|dns:derper'; then
    ok "A.7 WhiteIPSource markers emitted (dns:env, dns:derper, egress)"
else
    bad "A.7 WhiteIPSource markers must be emitted for the template annotation"
fi

# --- B. Template contracts ---

# B.1 template uses .DerpStatus.WhiteIP (the field, not a hardcoded value)
if grep -qE 'WhiteIP' internal/handlers/templates/admin/derp.html 2>/dev/null; then
    ok "B.1 derp.html renders .DerpStatus.WhiteIP"
else
    bad "B.1 derp.html must render .DerpStatus.WhiteIP"
fi

# B.2 template shows WhiteIPSource annotation when present
if grep -A 3 '\.DerpStatus\.WhiteIP' internal/handlers/templates/admin/derp.html 2>/dev/null | grep -qE 'WhiteIPSource'; then
    ok "B.2 derp.html shows .DerpStatus.WhiteIPSource annotation"
else
    bad "B.2 derp.html must show .DerpStatus.WhiteIPSource annotation"
fi

# --- C. i18n contracts ---

# C.1 RU block has the 4 source-help keys
for key in dns:env dns:derper egress; do
    if grep -qE "\"derp.field_public_ip_source_help_${key}\"" internal/i18n/catalog_derp.go 2>/dev/null; then
        ok "C.1 catalog has derp.field_public_ip_source_help_${key} (RU)"
    else
        bad "C.1 catalog missing derp.field_public_ip_source_help_${key} (RU)"
    fi
done

# C.2 EN block has the same keys
for key in dns:env dns:derper egress; do
    cnt=$(grep -cE "\"derp.field_public_ip_source_help_${key}\"" internal/i18n/catalog_derp.go 2>/dev/null)
    if [ "$cnt" -ge "2" ]; then
        ok "C.2 catalog has derp.field_public_ip_source_help_${key} x $cnt (RU + EN)"
    else
        bad "C.2 catalog has derp.field_public_ip_source_help_${key} x $cnt (need RU + EN, >= 2)"
    fi
done

# --- D. Tests contracts ---

# D.1 test file exists
if [ -f internal/feature/admin/derp_b237_2_test.go ]; then
    ok "D.1 derp_b237_2_test.go exists"
else
    bad "D.1 derp_b237_2_test.go missing"
fi

# D.2 4+ tests
n_tests=$(grep -cE '^func Test' internal/feature/admin/derp_b237_2_test.go 2>/dev/null || echo 0)
if [ "$n_tests" -ge "4" ]; then
    ok "D.2 derp_b237_2_test.go has $n_tests tests (>= 4)"
else
    bad "D.2 derp_b237_2_test.go has $n_tests tests (need >= 4)"
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

# E.2 B237.2 unit tests pass
if command -v go >/dev/null 2>&1; then
    if CGO_ENABLED=0 go test -short -count=1 -timeout 30s -run 'ResolvePublicDERPIP' ./internal/feature/admin/... 2>/dev/null | grep -q '^ok'; then
        ok "E.2 B237.2 unit tests pass"
    else
        bad "E.2 B237.2 unit tests failed"
    fi
else
    echo "  SKIP  E.2 B237.2 unit tests (no go in PATH)"
fi

# E.3 TestTemplateArgsMatchCatalog regression guard
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
echo "=== B237.2 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
