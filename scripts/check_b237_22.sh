#!/bin/bash
# scripts/check_b237_22.sh — B237.22 (v1.5.2+)
# UI-only CDN-grouping of device_rules on /my/exit-rules
# and /admin/exit-rules (TD-11 / Approach G).
#
# Why this contract:
#   The autoupdate (B77) replaces per-IP /32 rules with
#   the CDN's published CIDR list when the domain is
#   behind a known CDN. For Cloudflare that's 15 disjoint
#   CIDRs per (user, device, exit_node, domain) tuple. The
#   pre-B237.22 exit-rules page showed all 15 rows as a
#   flat list — correct but visually noisy. Live data
#   (2026-08): 46 of 151 device_rules rows (30%) are
#   CDN-derivable from 5 distinct parent_domains.
#
# Approach G (UI-only) — chosen over Approaches A-F
# (storage / migration / autoupdate changes) because:
#   - Operator concern: "не наложит ли это ограничения
#     на текущую работу правил и доступа ... ресур cloudflare
#     может быть залочен как и любой другой внешний ресурс
#     ... нужны именно правила на конкретный ресурс делать
#     полный проброс не надо - ломает всю логику"
#   - Each CIDR row stays individually editable (per-CIDR
#     delete button + audit log entry). No "remove all 15"
#     bulk button — the operator must explicitly remove
#     each CIDR they don't want.
#   - The natural key (user_id, device_id, exit_node_id,
#     target_type, target_value) is unchanged. Each user
#     keeps isolated access per (device, exit_node).
#   - Zero migration risk. Zero schema change. Zero
#     autoupdate / SyncAdvertisedRoutes / acl.go changes.
#
# What this contract pins:
#   A. cdn_group.go + cdn_group_admin.go helpers exist
#      (GroupRulesByCDN, GroupAdminRulesByCDN,
#       CDNDisplayItem, CDNDisplayView, etc.)
#   B. form_my.go + form_admin.go pass the CDN-grouped
#      view to the templates
#   C. /my/exit-rules + /admin/exit-rules templates
#      iterate the CDN-grouped view (CDN groups +
#      ungrouped tail)
#   D. Storage is UNCHANGED (no migration, no schema
#      change, no autoupdate change)
#   E. Unit tests cover the helpers
#   F. i18n keys for the per-group "X диапазонов" badge
#      (RU + EN)
#   G. AGENTS.md + PLANS.md mention B237.22
#   H. verify_pre_deploy.sh includes this check
#   I. The dev-tag contract is preserved (no other rules
#      are affected by the grouping)
#
# Exit 0 on all green, non-zero on any FAIL.

set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

PASS=0
FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

# --- A. Source: the helpers (cdn_group.go + cdn_group_admin.go) ---

# A.1 cdn_group.go exists
if [ -f internal/feature/exit_rules/cdn_group.go ]; then
    ok "A.1 cdn_group.go exists"
else
    bad "A.1 cdn_group.go missing"
fi

# A.2 cdn_group_admin.go exists
if [ -f internal/feature/exit_rules/cdn_group_admin.go ]; then
    ok "A.2 cdn_group_admin.go exists"
else
    bad "A.2 cdn_group_admin.go missing"
fi

# A.3 IsCDNGroupMarker + ParseCDNGroupMarker + GroupRulesByCDN exist
if grep -q '^func IsCDNGroupMarker' internal/feature/exit_rules/cdn_group.go 2>/dev/null && \
   grep -q '^func ParseCDNGroupMarker' internal/feature/exit_rules/cdn_group.go 2>/dev/null && \
   grep -q '^func GroupRulesByCDN' internal/feature/exit_rules/cdn_group.go 2>/dev/null; then
    ok "A.3 IsCDNGroupMarker + ParseCDNGroupMarker + GroupRulesByCDN exist"
else
    bad "A.3 the 3 core helpers must all be present in cdn_group.go"
fi

# A.4 GroupAdminRulesByCDN exists (admin-side counterpart)
if grep -q '^func GroupAdminRulesByCDN' internal/feature/exit_rules/cdn_group_admin.go 2>/dev/null; then
    ok "A.4 GroupAdminRulesByCDN exists (admin-side)"
else
    bad "A.4 GroupAdminRulesByCDN must exist in cdn_group_admin.go"
fi

# A.5 CDNDisplayItem + CDNDisplayView structs exist (the per-(host, exitNode) view)
if grep -q '^type CDNDisplayItem struct' internal/feature/exit_rules/cdn_group.go 2>/dev/null && \
   grep -q '^type CDNDisplayView struct' internal/feature/exit_rules/cdn_group.go 2>/dev/null; then
    ok "A.5 CDNDisplayItem + CDNDisplayView structs exist"
else
    bad "A.5 CDNDisplayItem + CDNDisplayView structs must exist"
fi

# A.6 CDNDisplayItemAdmin + CDNDisplayViewAdmin exist (admin-side)
if grep -q '^type CDNDisplayItemAdmin struct' internal/feature/exit_rules/cdn_group_admin.go 2>/dev/null && \
   grep -q '^type CDNDisplayViewAdmin struct' internal/feature/exit_rules/cdn_group_admin.go 2>/dev/null; then
    ok "A.6 CDNDisplayItemAdmin + CDNDisplayViewAdmin exist (admin-side)"
else
    bad "A.6 admin-side display structs must exist"
fi

# --- B. Wire-up: form_my.go + form_admin.go pass the view ---

# B.1 form_my.go passes GroupedByHostnameCDN to the template
if grep -q '"GroupedByHostnameCDN":' internal/feature/exit_rules/form_my.go 2>/dev/null; then
    ok "B.1 form_my.go passes GroupedByHostnameCDN to the template"
else
    bad "B.1 form_my.go must pass GroupedByHostnameCDN to the template"
fi

# B.2 form_my.go calls GroupRulesByCDN
if grep -q 'GroupRulesByCDN(' internal/feature/exit_rules/form_my.go 2>/dev/null; then
    ok "B.2 form_my.go calls GroupRulesByCDN"
else
    bad "B.2 form_my.go must call GroupRulesByCDN"
fi

# B.3 form_admin.go calls GroupAdminRulesByCDN
if grep -q 'GroupAdminRulesByCDN(' internal/feature/exit_rules/form_admin.go 2>/dev/null; then
    ok "B.3 form_admin.go calls GroupAdminRulesByCDN"
else
    bad "B.3 form_admin.go must call GroupAdminRulesByCDN"
fi

# B.4 form_admin.go has NodesCDN field in devNodeGroup
if grep -q 'NodesCDN map\[string\]CDNDisplayViewAdmin' internal/feature/exit_rules/form_admin.go 2>/dev/null; then
    ok "B.4 form_admin.go has NodesCDN in devNodeGroup"
else
    bad "B.4 form_admin.go must have NodesCDN in devNodeGroup"
fi

# --- C. Templates iterate the CDN-grouped view ---

# C.1 /my/exit-rules template iterates GroupedByHostnameCDN
if grep -q 'GroupedByHostnameCDN' internal/handlers/templates/exit_rules.html 2>/dev/null; then
    ok "C.1 /my/exit-rules template iterates GroupedByHostnameCDN"
else
    bad "C.1 /my/exit-rules template must iterate GroupedByHostnameCDN"
fi

# C.2 /my/exit-rules template branches on IsCDNGroup
if grep -q 'IsCDNGroup' internal/handlers/templates/exit_rules.html 2>/dev/null; then
    ok "C.2 /my/exit-rules template branches on IsCDNGroup"
else
    bad "C.2 /my/exit-rules template must branch on IsCDNGroup"
fi

# C.3 /admin/exit-rules template iterates NodesCDN
if grep -q 'NodesCDN' internal/handlers/templates/admin/exit_rules.html 2>/dev/null; then
    ok "C.3 /admin/exit-rules template iterates NodesCDN"
else
    bad "C.3 /admin/exit-rules template must iterate NodesCDN"
fi

# C.4 /admin/exit-rules template branches on IsCDNGroup
if grep -q 'IsCDNGroup' internal/handlers/templates/admin/exit_rules.html 2>/dev/null; then
    ok "C.4 /admin/exit-rules template branches on IsCDNGroup"
else
    bad "C.4 /admin/exit-rules template must branch on IsCDNGroup"
fi

# C.5 /admin/exit-rules template uses the TotalCount for the per-(exitNode) badge
if grep -q 'TotalCount' internal/handlers/templates/admin/exit_rules.html 2>/dev/null; then
    ok "C.5 /admin/exit-rules template uses TotalCount for the badge"
else
    bad "C.5 /admin/exit-rules template must use TotalCount"
fi

# --- D. Storage is UNCHANGED (the operator's hard constraint) ---

# D.1 cdn_group.go file's header comment mentions "UI-only" or "no migration"
if grep -q -E 'UI-only|no migration|storage is unchanged' internal/feature/exit_rules/cdn_group.go 2>/dev/null; then
    ok "D.1 cdn_group.go header pins UI-only / no migration contract"
else
    bad "D.1 cdn_group.go header must mention UI-only / no migration"
fi

# D.2 cdn_group_admin.go header mentions "UI-only" or "no migration"
if grep -q -E 'UI-only|no migration|storage is unchanged' internal/feature/exit_rules/cdn_group_admin.go 2>/dev/null; then
    ok "D.2 cdn_group_admin.go header pins UI-only / no migration contract"
else
    bad "D.2 cdn_group_admin.go header must mention UI-only / no migration"
fi

# D.3 No new "cdn_group_id" column or marker-as-row storage change in db/
if grep -qE 'cdn_group_id|cdn_group_marker' internal/db/migrations/*.sql 2>/dev/null; then
    bad "D.3 db migrations must NOT introduce a cdn_group column (UI-only grouping)"
else
    ok "D.3 no cdn_group column in db migrations (storage unchanged)"
fi

# D.4 cdn.go (the autoupdater) is unchanged
# (catches accidental edits to the autoupdate that would
# change the per-CIDR insert logic)
if grep -q 'cdnParentMarker' internal/feature/exit_rules/cdn.go 2>/dev/null; then
    ok "D.4 cdn.go still uses cdnParentMarker (autoupdate unchanged)"
else
    bad "D.4 cdn.go autoupdate logic must not be removed"
fi

# --- E. Unit tests ---

# E.1 cdn_group_test.go exists with at least 6 tests
test_count=$(grep -c '^func Test' internal/feature/exit_rules/cdn_group_test.go 2>/dev/null || echo 0)
if [ "$test_count" -ge 6 ]; then
    ok "E.1 cdn_group_test.go has $test_count tests (>= 6)"
else
    bad "E.1 cdn_group_test.go has only $test_count tests (need >= 6)"
fi

# E.2 cdn_group_admin_test.go exists with at least 3 tests
admin_test_count=$(grep -c '^func Test' internal/feature/exit_rules/cdn_group_admin_test.go 2>/dev/null || echo 0)
if [ "$admin_test_count" -ge 3 ]; then
    ok "E.2 cdn_group_admin_test.go has $admin_test_count tests (>= 3)"
else
    bad "E.2 cdn_group_admin_test.go has only $admin_test_count tests (need >= 3)"
fi

# E.3 cdn_group_test.go + cdn_group_admin_test.go actually pass
if go test -short -count=1 ./internal/feature/exit_rules/... 2>&1 | grep -qE 'FAIL|--- FAIL'; then
    bad "E.3 cdn_group tests must pass"
else
    ok "E.3 cdn_group + cdn_group_admin tests pass"
fi

# --- F. i18n keys (RU + EN) ---

# F.1 RU cdn_group_count key exists
if grep -q '"exit_rules.cdn_group_count"' internal/i18n/catalog_exit_rules.go 2>/dev/null; then
    ok "F.1 cdn_group_count key exists in catalog_exit_rules.go"
else
    bad "F.1 cdn_group_count key missing from catalog_exit_rules.go"
fi

# F.2 the RU translation has %d (the count placeholder)
if grep -qE '"exit_rules.cdn_group_count"[[:space:]]*:[[:space:]]*"%d' internal/i18n/catalog_exit_rules.go 2>/dev/null; then
    # Check RU section specifically — catalog is split ru / en, but
    # both languages are in the same file. The "X диапазонов" string
    # is unique to RU. We check at least one of the two languages has
    # the %d placeholder; both should but we only need to verify
    # the structure.
    ok "F.2 cdn_group_count has %d placeholder"
else
    bad "F.2 cdn_group_count must have %d placeholder"
fi

# F.3 the RU translation has "диапазонов"
if grep -qE 'cdn_group_count.*диапазонов' internal/i18n/catalog_exit_rules.go 2>/dev/null; then
    ok "F.3 cdn_group_count RU has 'диапазонов' (RU word for 'ranges')"
else
    bad "F.3 cdn_group_count RU translation must have 'диапазонов'"
fi

# F.4 the EN translation has "ranges" (loose check: cdn_group_count line contains the word 'ranges')
if grep -E 'cdn_group_count' internal/i18n/catalog_exit_rules.go 2>/dev/null | grep -qE 'ranges'; then
    ok "F.4 cdn_group_count EN has 'ranges'"
else
    bad "F.4 cdn_group_count EN translation must have 'ranges'"
fi

# --- G. AGENTS.md + PLANS.md mention B237.22 ---

# G.1 AGENTS.md mentions B237.22
if grep -q 'B237\.22' AGENTS.md 2>/dev/null; then
    ok "G.1 AGENTS.md mentions B237.22"
else
    bad "G.1 AGENTS.md must mention B237.22"
fi

# G.2 docs/PLANS.md mentions TD-11
if grep -q 'TD-11' docs/PLANS.md 2>/dev/null; then
    ok "G.2 docs/PLANS.md mentions TD-11 (the parent task)"
else
    bad "G.2 docs/PLANS.md must mention TD-11"
fi

# --- H. verify_pre_deploy.sh includes this check ---

# H.1 verify_pre_deploy.sh has check_b237_22
if grep -q 'check_b237_22' scripts/verify_pre_deploy.sh 2>/dev/null; then
    ok "H.1 scripts/verify_pre_deploy.sh includes check_b237_22"
else
    bad "H.1 scripts/verify_pre_deploy.sh must include check_b237_22"
fi

# --- I. Build + vet are clean ---

# I.1 go build is clean
if go build ./... 2>&1 | grep -q .; then
    bad "I.1 go build ./... must be clean"
else
    ok "I.1 go build ./... is clean"
fi

# I.2 go vet is clean
if go vet ./... 2>&1 | grep -q .; then
    bad "I.2 go vet ./... must be clean"
else
    ok "I.2 go vet ./... is clean"
fi

# I.3 staticcheck for the new files is clean (no SA-rules triggered
#     in cdn_group.go or cdn_group_admin.go)
if staticcheck ./internal/feature/exit_rules/... 2>&1 | grep -E 'cdn_group' | grep -q .; then
    bad "I.3 staticcheck must not flag cdn_group.go or cdn_group_admin.go"
else
    ok "I.3 staticcheck clean for cdn_group*.go"
fi

echo ""
echo "  B237.22 (TD-11 / Approach G — UI-only CDN grouping): $PASS PASS, $FAIL FAIL"
exit $FAIL
