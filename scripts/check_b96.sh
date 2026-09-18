#!/bin/bash
# scripts/check_b96.sh — invoked by verify_pre_deploy.sh B96 check.
#
# Why a separate file: the B96 check pins the v1.1.0 (TD-1)
# sidebar-section contract via Go unit tests, and the
# verify_pre_deploy.sh run_check helper runs a single inline
# command. The 2 Go tests that B96 depends on (TestB96_Admin
# LayoutGroupsAll22Pages + TestB96_AllAdminPagesInASection)
# are too verbose to inline as a bash regex. A dedicated
# shell script avoids all of that.
#
# Pinned contracts:
#   - internal/handlers/templates/layout.html groups all 22
#     admin pages into exactly 6 <details class="sidebar-section">
#     blocks (Devices & Nodes / Access Control / System Health
#     & Logs / Integrations / Data / Settings & Users)
#   - Each section has a {{if .InSectionX}}open{{end}}
#     conditional that auto-opens when the current page is
#     in the section
#   - The 6 section title i18n keys exist in catalog_common.go
#     (B4 parity: ru + en)
#   - 2 Go unit tests pass: TestB96_AdminLayoutGroupsAll22Pages
#     and TestB96_AllAdminPagesInASection
#   - Bonus: the hamburger input/label are present in
#     layout.html (B97's contract, but we pin them here too
#     so a layout-only refactor doesn't silently break TD-3)

set -e

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# Resolve go robustly (same pattern as check_b92.sh).
GO=""
if command -v go >/dev/null 2>&1; then
    GO="go"
elif [ -x "/mnt/c/Program Files/Go/bin/go.exe" ]; then
    GO="/mnt/c/Program Files/Go/bin/go.exe"
else
    echo "SKY-FAIL: go not found" >&2
    exit 1
fi

# 1. layout.html exists and has all the B96 markers.
test -f internal/handlers/templates/layout.html || { echo "SKY-FAIL: layout.html missing" >&2; exit 1; }
grep -qF "v1.1.0 (TD-1 + TD-3)" internal/handlers/templates/layout.html || { echo "SKY-FAIL: layout.html missing v1.1.0 (TD-1 + TD-3) marker" >&2; exit 1; }

# 2. Exactly 6 <details class="sidebar-section"> blocks.
SECTION_COUNT=$(grep -cF '<details class="sidebar-section"' internal/handlers/templates/layout.html || true)
if [ "$SECTION_COUNT" != "6" ]; then
    echo "SKY-FAIL: expected 6 sidebar sections, found $SECTION_COUNT" >&2
    exit 1
fi

# 3. All 6 InSectionX booleans appear.
for s in InSectionDevices InSectionAccess InSectionHealth InSectionIntegrations InSectionData InSectionSettings; do
    grep -qF "{{if .$s}}open{{end}}" internal/handlers/templates/layout.html || { echo "SKY-FAIL: layout.html missing {{if .$s}}open{{end}} conditional" >&2; exit 1; }
done

# 4. All 8 i18n keys exist in catalog_common.go (B4 parity
#    test already enforces ru == en for the key set; here we
#    just check both files have them in the right place).
for k in nav.section_devices nav.section_access nav.section_health nav.section_integrations nav.section_data nav.section_settings nav.toggle_sidebar nav.toggle_section; do
    grep -qF "\"$k\"" internal/i18n/catalog_common.go || { echo "SKY-FAIL: i18n key $k missing from catalog_common.go" >&2; exit 1; }
done

# 5. The 22 admin pages are referenced in layout.html (links).
#    A few of them are inside the data: we check the literal
#    href so subpages (e.g. /admin/exit-nodes/cleanup) don't
#    accidentally match.
for p in /admin/devices /admin/exit-nodes /admin/meshes /admin/subnets /admin/acls /admin/exit-rules /admin/headscale/acl /admin/system_tests /admin/services /admin/audit /admin/integrations /admin/headscale /admin/headplane /admin/telegram /admin/tailscale /admin/derp /admin/backup /admin/invites /admin/control-planes /admin/settings /admin/users /admin/update; do
    grep -qF "href=\"$p\"" internal/handlers/templates/layout.html || { echo "SKY-FAIL: layout.html missing link to $p" >&2; exit 1; }
done

# 6. Hamburger input + label are present.
grep -qF 'id="sidebar-toggle"' internal/handlers/templates/layout.html || { echo "SKY-FAIL: layout.html missing sidebar-toggle input" >&2; exit 1; }
grep -qF 'class="sidebar-toggle"' internal/handlers/templates/layout.html || { echo "SKY-FAIL: layout.html missing sidebar-toggle label" >&2; exit 1; }

# 7. Go unit tests pass.
"$GO" test -count=1 -run "TestB96_" ./internal/handlers/ 2>&1 || { echo "SKY-FAIL: B96 unit tests failed" >&2; exit 1; }

echo "B96 check passed: layout.html groups 22 admin pages into 6 collapsible sidebar sections + 2 Go unit tests pass"
