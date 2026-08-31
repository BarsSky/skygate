#!/usr/bin/env bash
# check_td18.sh — TD-18 contract check.
#
# TD-18 (2026-08-31): close 31 pre-existing i18n gaps + add hint blocks to
# 3 admin pages (headscale_acl, services, derp_dashboard) that had no
# help text or were entirely untranslated. Also turn TD-16 contract B
# from advisory to hard fail + add a B2 sub-check for padded catalog
# keys.
#
# This script pins:
#   A. 0 missing i18n keys in any admin template (re-pin of TD-16
#      contract B, since TD-18 made it a hard fail).
#   B. 0 padded catalog keys (the B148 bug that left 50 cert.* keys
#      unreachable — must NOT regress).
#   C. admin/headscale_acl.html has the "What is an ACL?" hint block
#      + i18n-wrapped table headers + i18n-wrapped button text.
#   D. admin/services.html has the "What do the statuses mean?" +
#      "What are these integrations?" hint blocks.
#   E. admin/derp_dashboard.html has full i18n wrap (was 100% English
#      pre-TD-18) + the "About the probes" hint block.
#   F. All 3 hint pages reference the expected i18n keys (e.g.
#      "acl.what_is_body", "services.help_body", "derp_dashboard.about_body").
#   G. AGENTS.md mentions TD-18.
#   H. verify_pre_deploy.sh references check_td18.sh.
#   I. This script is executable.
#   J. go test -short ./internal/i18n/... passes (catalog parity).

set -u
PASS=0
FAIL=0
ok() { PASS=$((PASS+1)); printf '  PASS  %s\n' "$1"; }
bad() { FAIL=$((FAIL+1)); printf '  FAIL  %s\n' "$1"; }

# ---------------------------------------------------------------------------
# A. 0 missing i18n keys in admin templates (delegates to TD-16 B check)
# ---------------------------------------------------------------------------
missing_a=$(awk '
/\{\{[[:space:]]*t[[:space:]]+"[a-zA-Z][a-zA-Z0-9_.]*"/ {
    match($0, /\{\{[[:space:]]*t[[:space:]]+"([a-zA-Z][a-zA-Z0-9_.]*)"/, arr)
    if (arr[1] != "") {
        print arr[1]
    }
}' $(find internal/handlers/templates -name '*.html' -type f 2>/dev/null) \
    | sort -u | while read -r key; do
    if ! grep -q "\"$key\"" internal/i18n/catalog*.go 2>/dev/null; then
        echo "$key"
    fi
done | sort -u)
if [ -z "$missing_a" ]; then
    ok "contract A: 0 i18n keys missing from the catalog"
else
    bad "contract A: missing i18n keys:"
    echo "$missing_a" | sed 's/^/        /'
fi

# ---------------------------------------------------------------------------
# B. 0 padded catalog keys (the B148 bug that left 50 cert.* keys
#    unreachable from the t() funcmap).
# ---------------------------------------------------------------------------
padded_b=$(grep -hnE '^\s*"[a-zA-Z][a-zA-Z0-9_.]*\s+"\s*:' internal/i18n/catalog*.go 2>/dev/null \
    | sed -E 's/.*"([^"]+)"\s*:.*/\1/' \
    | sort -u)
if [ -z "$padded_b" ]; then
    ok "contract B: 0 catalog keys with trailing whitespace"
else
    bad "contract B: padded catalog keys (unreachable):"
    echo "$padded_b" | sed 's/^/        /'
fi

# ---------------------------------------------------------------------------
# C. admin/headscale_acl.html has hint block + i18n table headers
# ---------------------------------------------------------------------------
if grep -q 't "acl.what_is_body"' internal/handlers/templates/admin/headscale_acl.html; then
    ok "contract C1: headscale_acl.html references acl.what_is_body hint"
else
    bad "contract C1: headscale_acl.html missing acl.what_is_body hint"
fi
if grep -q 't "acl.skygate_managed_label"' internal/handlers/templates/admin/headscale_acl.html \
    && grep -q 't "acl.external_label"' internal/handlers/templates/admin/headscale_acl.html; then
    ok "contract C2: headscale_acl.html i18n-wraps the Skygate/External section labels"
else
    bad "contract C2: headscale_acl.html missing i18n section labels"
fi
if grep -q 't "acl.src_help"' internal/handlers/templates/admin/headscale_acl.html \
    && grep -q 't "acl.dst_help"' internal/handlers/templates/admin/headscale_acl.html \
    && grep -q 't "acl.label_help"' internal/handlers/templates/admin/headscale_acl.html; then
    ok "contract C3: headscale_acl.html has per-field hints (src/dst/label_help)"
else
    bad "contract C3: headscale_acl.html missing per-field hints"
fi

# ---------------------------------------------------------------------------
# D. admin/services.html has the 2 hint blocks
# ---------------------------------------------------------------------------
if grep -qE '\{\{t "services.help_title"\}|\{\{tf "services.help_title"' internal/handlers/templates/admin/services.html \
    && grep -qE '\{\{t "services.help_body"|\{\{tf "services.help_body"' internal/handlers/templates/admin/services.html; then
    ok "contract D1: services.html has 'What do the statuses mean?' hint block"
else
    bad "contract D1: services.html missing 'What do the statuses mean?' hint"
fi
if grep -q 't "services.integrations_help_title"' internal/handlers/templates/admin/services.html \
    && grep -q 't "services.integrations_help_body"' internal/handlers/templates/admin/services.html; then
    ok "contract D2: services.html has 'What are these integrations?' hint block"
else
    bad "contract D2: services.html missing 'What are these integrations?' hint"
fi

# ---------------------------------------------------------------------------
# E. admin/derp_dashboard.html has full i18n wrap + About hint
# ---------------------------------------------------------------------------
if grep -q 't "derp_dashboard.title"' internal/handlers/templates/admin/derp_dashboard.html \
    && grep -q 't "derp_dashboard.subtitle"' internal/handlers/templates/admin/derp_dashboard.html; then
    ok "contract E1: derp_dashboard.html i18n-wraps title + subtitle (was 100% English pre-TD-18)"
else
    bad "contract E1: derp_dashboard.html missing i18n title/subtitle"
fi
if grep -q 't "derp_dashboard.about_body"' internal/handlers/templates/admin/derp_dashboard.html; then
    ok "contract E2: derp_dashboard.html has 'About the probes' hint block"
else
    bad "contract E2: derp_dashboard.html missing 'About the probes' hint"
fi
if grep -q 't "derp_dashboard.latency_good_help"' internal/handlers/templates/admin/derp_dashboard.html \
    && grep -q 't "derp_dashboard.latency_ok_help"' internal/handlers/templates/admin/derp_dashboard.html \
    && grep -q 't "derp_dashboard.latency_bad_help"' internal/handlers/templates/admin/derp_dashboard.html; then
    ok "contract E3: derp_dashboard.html has latency color threshold tooltips (≤50 / ≤150 / >150 ms)"
else
    bad "contract E3: derp_dashboard.html missing latency threshold tooltips"
fi
if grep -q 't "derp_dashboard.own_vs_public_help"' internal/handlers/templates/admin/derp_dashboard.html \
    && grep -q 't "derp_dashboard.probes_help"' internal/handlers/templates/admin/derp_dashboard.html; then
    ok "contract E4: derp_dashboard.html has 'own vs public' + 'Probes counter' hints"
else
    bad "contract E4: derp_dashboard.html missing own-vs-public or probes-counter hints"
fi

# ---------------------------------------------------------------------------
# F. AGENTS.md mentions TD-18
# ---------------------------------------------------------------------------
if grep -qF "TD-18" AGENTS.md; then
    ok "contract F: AGENTS.md mentions TD-18"
else
    bad "contract F: AGENTS.md does NOT mention TD-18"
fi

# ---------------------------------------------------------------------------
# G. verify_pre_deploy.sh references check_td18.sh
# ---------------------------------------------------------------------------
if grep -qF "check_td18.sh" scripts/verify_pre_deploy.sh; then
    ok "contract G: verify_pre_deploy.sh references check_td18.sh"
else
    bad "contract G: verify_pre_deploy.sh does NOT reference check_td18.sh"
fi

# ---------------------------------------------------------------------------
# H. This script is executable
# ---------------------------------------------------------------------------
if [ -x scripts/check_td18.sh ]; then
    ok "contract H: scripts/check_td18.sh is executable"
else
    bad "contract H: scripts/check_td18.sh is NOT executable"
fi

# ---------------------------------------------------------------------------
# I. go test -short ./internal/i18n/... passes
# ---------------------------------------------------------------------------
if command -v go >/dev/null 2>&1; then
    if go test -count=1 -short ./internal/i18n/... >/dev/null 2>&1; then
        ok "contract I: go test -short ./internal/i18n/... passes"
    else
        bad "contract I: go test -short ./internal/i18n/... failed"
    fi
else
    bad "contract I: go not on PATH"
fi

# ---------------------------------------------------------------------------
echo
echo "=== TD-18 summary: $PASS pass, $FAIL fail ==="
if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
echo "all contracts satisfied"
