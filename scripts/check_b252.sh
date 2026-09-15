#!/bin/bash
# check_b252.sh — B252 grouped ACL view contract check.
#
# B252 (2026-09-15): operator reported the ACL policy view was
# still too dense after B250 (one big multi-line JSON). The fix:
# classify grants into 8 categories and render each as a
# collapsible <details> with a small table inside. Raw JSON
# stays under a "Show raw JSON" toggle.
#
# This script pins:
#   - Source contract (types + classifier + parser)
#   - Template contract (uses PolicyView, has summary chips,
#     categories rendered via <details>)
#   - i18n keys (RU + EN, both languages)
#   - Test contract (TestParseACLPolicy_LiveVerify_NoOtherBucket
#     pins the "all prod-shape grants must classify" invariant)
#
# Usage: bash scripts/check_b252.sh

set -u
PASS=0
FAIL=0

if [ -t 1 ]; then
  C_GREEN="\033[32m"; C_RED="\033[31m"; C_RESET="\033[0m"
else
  C_GREEN=""; C_RED=""; C_RESET=""
fi

ok() { echo -e "  ${C_GREEN}PASS${C_RESET}  $1"; PASS=$((PASS + 1)); }
bad() { echo -e "  ${C_RED}FAIL${C_RESET}  $1"; FAIL=$((FAIL + 1)); }
check() { local desc="$1"; shift; if "$@" >/dev/null 2>&1; then ok "$desc"; else bad "$desc"; fi; }

echo "=== A. Source contracts (admin_pages.go) ==="

# A1. Types are declared.
check "ACLGrant struct exists" \
  grep -q 'type ACLGrant struct' internal/feature/admin/admin_pages.go
check "ACLCategory struct exists" \
  grep -q 'type ACLCategory struct' internal/feature/admin/admin_pages.go
check "ACLPolicyView struct exists" \
  grep -q 'type ACLPolicyView struct' internal/feature/admin/admin_pages.go

# A2. Classifier + parser + helpers are present.
check "classifyGrantID function" \
  grep -q 'func classifyGrantID' internal/feature/admin/admin_pages.go
check "parseACLPolicy function" \
  grep -q 'func parseACLPolicy' internal/feature/admin/admin_pages.go
check "unescapeHTML function" \
  grep -q 'func unescapeHTML' internal/feature/admin/admin_pages.go
check "categoryOrder var has 8 categories" \
  grep -A10 'var categoryOrder = \[\]string{' internal/feature/admin/admin_pages.go \
    | grep -c '"' | awk '{exit ($1 >= 16 ? 0 : 1)}'

# A3. Handler passes PolicyView (not Policy).
check "GetAdminACLs sets PolicyView in template data" \
  grep -q '"PolicyView"' internal/feature/admin/admin_pages.go

# A4. The 8 category IDs are referenced in render order.
for cat in per_user_main per_user_self per_device_inet cidr_outbound infra_mesh tag_mesh wildcard other; do
  check "category ID '$cat' in categoryOrder" \
    grep -q "\"$cat\"" internal/feature/admin/admin_pages.go
done

echo ""
echo "=== B. Template contract (admin/acls.html) ==="

# B1. Template uses PolicyView.
check "Template uses .PolicyView" \
  grep -q '\.PolicyView' internal/handlers/templates/admin/acls.html

# B2. Summary chips on top of the categorised view.
check "Template renders TotalGrants" \
  grep -q '\.PolicyView\.TotalGrants' internal/handlers/templates/admin/acls.html

# B3. Each category rendered as <details> with table inside.
check "Category rendered with <details>" \
  grep -q '<details class="acl-category"' internal/handlers/templates/admin/acls.html
check "Grant table has src/dst/via/users columns" \
  grep -q 'acls.col.src' internal/handlers/templates/admin/acls.html

# B4. SSH + tagOwners + raw JSON sections.
check "SSH section uses separate <details>" \
  grep -q 'acls.ssh_title' internal/handlers/templates/admin/acls.html
check "tagOwners section" \
  grep -q 'acls.tagOwners_title' internal/handlers/templates/admin/acls.html
check "Raw JSON toggle" \
  grep -q 'acls.raw_json_title' internal/handlers/templates/admin/acls.html

echo ""
echo "=== C. i18n keys (RU + EN) ==="

# All required keys.
keys=(
  acls.summary_grants
  acls.summary_ssh
  acls.summary_hosts
  acls.summary_tagOwners
  acls.summary_groups
  acls.parse_warning_title
  acls.parse_warning_body
  acls.ssh_title
  acls.tagOwners_title
  acls.raw_json_title
  acls.col.src
  acls.col.dst
  acls.col.via
  acls.col.users
  acls.col.tag
  acls.col.owners
  acls.category.per_user_main.name
  acls.category.per_user_self.name
  acls.category.per_device_inet.name
  acls.category.cidr_outbound.name
  acls.category.infra_mesh.name
  acls.category.tag_mesh.name
  acls.category.wildcard.name
  acls.category.other.name
)
for k in "${keys[@]}"; do
  # Each key must appear at least twice (RU + EN).
  count=$(grep -c "\"$k\"" internal/i18n/catalog_admin.go)
  if [ "$count" -ge 2 ]; then
    ok "i18n key '$k' (RU + EN)"
  else
    bad "i18n key '$k' — found $count occurrence(s), need >= 2"
  fi
done

echo ""
echo "=== D. Test contract (admin_acls_b252_test.go) ==="

# D1. Test file exists with each test.
check "B252 test file exists" \
  test -f internal/feature/admin/admin_acls_b252_test.go
check "TestClassifyGrantID_PerUserMain exists" \
  grep -q 'func TestClassifyGrantID_PerUserMain' internal/feature/admin/admin_acls_b252_test.go
check "TestClassifyGrantID_PerUserSelf exists" \
  grep -q 'func TestClassifyGrantID_PerUserSelf' internal/feature/admin/admin_acls_b252_test.go
check "TestClassifyGrantID_CIDROutbound exists" \
  grep -q 'func TestClassifyGrantID_CIDROutbound' internal/feature/admin/admin_acls_b252_test.go
check "TestClassifyGrantID_PerDeviceInternet exists" \
  grep -q 'func TestClassifyGrantID_PerDeviceInternet' internal/feature/admin/admin_acls_b252_test.go
check "TestClassifyGrantID_InfraMesh exists" \
  grep -q 'func TestClassifyGrantID_InfraMesh' internal/feature/admin/admin_acls_b252_test.go
check "TestClassifyGrantID_TagMesh exists" \
  grep -q 'func TestClassifyGrantID_TagMesh' internal/feature/admin/admin_acls_b252_test.go
check "TestClassifyGrantID_Wildcard exists" \
  grep -q 'func TestClassifyGrantID_Wildcard' internal/feature/admin/admin_acls_b252_test.go
check "TestClassifyGrantID_OtherFallback exists" \
  grep -q 'func TestClassifyGrantID_OtherFallback' internal/feature/admin/admin_acls_b252_test.go

# D2. The LiveVerify test pins the prod-shape invariant: NO grants
# should fall into the "other" bucket on the prod dataset.
check "TestParseACLPolicy_LiveVerify_NoOtherBucket pins 'no other' invariant" \
  grep -q 'NoOtherBucket' internal/feature/admin/admin_acls_b252_test.go

# D3. The HTML-escaped JSON parsing test (B252 critical path).
check "TestParseACLPolicy_HTMLEscaped exists" \
  grep -q 'func TestParseACLPolicy_HTMLEscaped' internal/feature/admin/admin_acls_b252_test.go

echo ""
echo "=== B252 summary: $PASS pass / $FAIL fail ==="
[ "$FAIL" -gt 0 ] && exit 1
exit 0
