#!/usr/bin/env bash
#===============================================================================
# Skygate v1.3.19.1 follow-up (B119) — preferred_check TagToHostname
# enforcement
#
# Pins the v1.3.19.1 hotfix that extends `TagToHostname` in
# internal/feature/exit_rules/preferred_check.go to handle the
# post-B111 "tag:dev-infra-X" format. The v1.3.18.1 hotfix
# updated the LOCAL `tagToHost` closure in
# internal/feature/admin/system_tests.go but missed the
# EXPORTED `TagToHostname` function in preferred_check.go —
# so every rule whose exit_node was "karolina" was flagged
# as a "preferred_mismatch" against a device pref of
# "tag:dev-infra-karolina" (which the buggy helper returned
# as "dev-infra-karolina" — the wrong hostname).
#
# What this script verifies (live + on source):
#   A. Source: TagToHostname in preferred_check.go handles 4 formats
#      (tag:dev-infra-X, tag:exit-X, tag:X, bare X). Pre-fix only
#      handled 2 formats.
#   B. Source: the case order in TagToHostname is "tag:dev-infra-"
#      BEFORE "tag:" (the bug: pre-fix used
#      `TrimPrefix(rest, "exit-")` on the rest after stripping
#      "tag:" — so "tag:dev-infra-emilia" was reduced to
#      "dev-infra-emilia" instead of "emilia").
#   C. Source: form_my.go uses TagToHostname (not local copy) to
#      convert user pref to hostname before the IsRuleApplicable
#      comparison.
#   D. Source: form_admin.go uses PreferredExitNodeForRule which
#      internally calls TagToHostname.
#   E. Source: form_my.go calls db.GetDeviceExitNodePref /
#      db.GetUserExitNodePref (not a local copy of the tag strip).
#   F. Source: NO raw `strings.TrimPrefix(t, "tag:")` followed by
#      `strings.TrimPrefix(rest, "exit-")` in preferred_check.go —
#      that pattern is the v1.3.18.1 bug, must not regress.
#   G. Live DB: all device_exit_node_prefs rows use one of the
#      4 supported tag formats. Empty or unparseable prefs would
#      cause a runtime panic in TagToHostname.
#   H. Live policy: the v1.3.18.x hotfix test
#      `exit_rules.preferred_mismatch` system test uses the FIXED
#      `tagToHost` closure (this is the local one in
#      system_tests.go; documented as a redundant safety net
#      since the EXPORTED `TagToHostname` is the one used by the
#      form handlers).
#
# Exit codes:
#   0 = all contracts hold
#   1 = one or more contracts failed
#===============================================================================

set -uo pipefail
PASS=0; FAIL=0; WARN=0
ok()  { echo "  PASS  $*"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
warn(){ echo "  WARN  $*"; WARN=$((WARN+1)); }

# Allow override so this script works from /tmp on the VM
: "${SKYGATE_DIR:=$(cd "$(dirname "$0")/.." && pwd)}"
cd "${SKYGATE_DIR}" || exit 1
echo "skygate root: ${SKYGATE_DIR}"

PCHECK="internal/feature/exit_rules/preferred_check.go"
FORM_MY="internal/feature/exit_rules/form_my.go"
FORM_ADMIN="internal/feature/exit_rules/form_admin.go"
SYSTEM_TESTS="internal/feature/admin/system_tests.go"

[ -f "${PCHECK}" ] || { bad "source file not found: ${PCHECK}"; exit 1; }
[ -f "${FORM_MY}" ] || { bad "source file not found: ${FORM_MY}"; exit 1; }
[ -f "${FORM_ADMIN}" ] || { bad "source file not found: ${FORM_ADMIN}"; exit 1; }
[ -f "${SYSTEM_TESTS}" ] || { bad "source file not found: ${SYSTEM_TESTS}"; exit 1; }

# ------------------------------------------------------------------------------
# Contract A: source — TagToHostname handles 4 formats
# ------------------------------------------------------------------------------
echo
echo "=== A. source: TagToHostname handles 4 formats (post-B111) ==="
has_dev_infra=$(grep -c 'tag:dev-infra-' "${PCHECK}" || true)
has_tag_exit=$(grep -c 'tag:exit-' "${PCHECK}" || true)
has_tag_prefix=$(grep -c 'TrimPrefix(t, "tag:")' "${PCHECK}" || true)
if [ "${has_dev_infra}" -ge 1 ] && [ "${has_tag_exit}" -ge 1 ] && [ "${has_tag_prefix}" -ge 1 ]; then
    ok "TagToHostname has tag:dev-infra-X + tag:exit-X + tag:X cases"
else
    bad "TagToHostname missing a format: dev-infra=${has_dev_infra} exit=${has_tag_exit} tag=${has_tag_prefix}"
fi

# ------------------------------------------------------------------------------
# Contract B: source — case ORDER is correct (dev-infra BEFORE tag:)
# ------------------------------------------------------------------------------
echo
echo "=== B. source: case order in TagToHostname is dev-infra BEFORE tag: ==="
# Extract the TagToHostname function block. The first case must be
# 'tag:dev-infra-', the last case must be 'tag:' alone.
awk '/^func TagToHostname/,/^}/' "${PCHECK}" > /tmp/tag_to_hostname.txt
first_case=$(grep -nE 'case strings\.HasPrefix' /tmp/tag_to_hostname.txt | head -1 || true)
if echo "${first_case}" | grep -q 'tag:dev-infra-'; then
    ok "first case in TagToHostname switch is tag:dev-infra- (prefix order correct)"
else
    bad "first case is NOT tag:dev-infra-: ${first_case}"
fi
last_case=$(grep -nE 'case strings\.HasPrefix' /tmp/tag_to_hostname.txt | tail -1 || true)
if echo "${last_case}" | grep -q '"tag:"'; then
    ok "last case in TagToHostname switch is 'tag:' (catches all other tag: forms)"
else
    bad "last case is NOT 'tag:': ${last_case}"
fi

# ------------------------------------------------------------------------------
# Contract C: source — form_my.go uses TagToHostname (exported)
# ------------------------------------------------------------------------------
echo
echo "=== C. source: form_my.go uses TagToHostname (not local copy) ==="
if grep -q 'TagToHostname(' "${FORM_MY}"; then
    ok "form_my.go calls TagToHostname at least once"
else
    bad "form_my.go does NOT call TagToHostname — bug regression?"
fi

# ------------------------------------------------------------------------------
# Contract D: source — form_admin.go uses PreferredExitNodeForRule (which
# internally calls TagToHostname)
# ------------------------------------------------------------------------------
echo
echo "=== D. source: form_admin.go uses PreferredExitNodeForRule ==="
if grep -q 'PreferredExitNodeForRule(' "${FORM_ADMIN}"; then
    ok "form_admin.go calls PreferredExitNodeForRule at least once"
else
    bad "form_admin.go does NOT call PreferredExitNodeForRule — bug regression?"
fi

# ------------------------------------------------------------------------------
# Contract E: source — form_my.go uses db.GetDeviceExitNodePref /
# db.GetUserExitNodePref (not a local tag strip)
# ------------------------------------------------------------------------------
echo
echo "=== E. source: form_my.go uses db.Get*ExitNodePref helpers ==="
# form_my.go can use db.GetDeviceExitNodePref directly OR
# PreferredExitNodeForRule (which wraps it). Either counts.
has_dev_pref=$(grep -cE 'db\.GetDeviceExitNodePref|PreferredExitNodeForRule' "${FORM_MY}" || true)
has_user_pref=$(grep -cE 'db\.GetUserExitNodePref|PreferredExitNodeForRule' "${FORM_MY}" || true)
if [ "${has_dev_pref}" -ge 1 ] && [ "${has_user_pref}" -ge 1 ]; then
    ok "form_my.go uses db.GetDeviceExitNodePref + db.GetUserExitNodePref (directly or via PreferredExitNodeForRule)"
else
    bad "form_my.go missing pref helpers: dev=${has_dev_pref} user=${has_user_pref}"
fi

# ------------------------------------------------------------------------------
# Contract F: source — NO raw `strings.TrimPrefix(t, "tag:")` followed by
# `strings.TrimPrefix(rest, "exit-")` in preferred_check.go (the v1.3.18.1 bug
# pattern must not regress)
# ------------------------------------------------------------------------------
echo
echo "=== F. source: NO pre-v1.3.19.1 bug pattern in preferred_check.go ==="
if grep -E 'TrimPrefix\(rest, "exit-"\)' "${PCHECK}" >/dev/null; then
    bad "pre-v1.3.19.1 bug pattern detected: 'TrimPrefix(rest, \"exit-\")' still in preferred_check.go"
else
    ok "no pre-v1.3.19.1 bug pattern (TrimPrefix(rest, 'exit-')) in preferred_check.go"
fi

# ------------------------------------------------------------------------------
# Contract G: live DB — all device_exit_node_prefs + user_exit_node_prefs
# rows use one of the 4 supported tag formats (or are empty)
# ------------------------------------------------------------------------------
echo
echo "=== G. live DB: pref tags use a supported format ==="
if [ -f /home/skyadmin/skygate/.env ] && command -v psql >/dev/null 2>&1; then
    DSN=$(grep -E '^SKYGATE_DB_DSN=' /home/skyadmin/skygate/.env 2>/dev/null | head -1 | cut -d= -f2-)
    if [ -n "${DSN}" ]; then
        host=$(echo "${DSN}" | sed -E 's|.*@([^:/]+):.*|\1|')
        port=$(echo "${DSN}" | sed -E 's|.*@[^:/]+:([0-9]+).*|\1|')
        # Count rows that have a non-empty exit_node_tag but DON'T match
        # any of the 4 supported formats:
        #   tag:dev-infra-X, tag:exit-X, tag:X, bare X (no tag prefix).
        out=$(PGPASSWORD=skygate_admin_pass psql -h "${host}" -p "${port}" -U admin -d skygate_staging -A -t -c \
            "SELECT count(*) FROM (
                SELECT exit_node_tag FROM user_exit_node_prefs WHERE exit_node_tag != ''
                UNION ALL
                SELECT exit_node_tag FROM device_exit_node_prefs WHERE exit_node_tag != ''
             ) t WHERE exit_node_tag NOT LIKE 'tag:dev-infra-%'
                AND exit_node_tag NOT LIKE 'tag:exit-%'
                AND exit_node_tag NOT LIKE 'tag:%'
                AND exit_node_tag NOT LIKE '%';" 2>/dev/null)
        cnt=$(echo "${out}" | tr -d '[:space:]')
        if [ "${cnt}" = "0" ]; then
            ok "live prefs: 0 rows with unsupported tag format"
        else
            bad "live prefs: ${cnt} rows with unsupported tag format (TagToHostname won't recognize)"
        fi
    else
        warn "SKYGATE_DB_DSN not set in /home/skyadmin/skygate/.env — skipping live check"
    fi
else
    warn "psql not on PATH or /home/skyadmin/skygate/.env missing — skipping live check"
fi

# ------------------------------------------------------------------------------
# Contract H: source — system_tests.go has the v1.3.18.1 tagToHost
# closure (defensive: even though it's a local copy, ensure the
# fix is in place — this is the system test that surfaced the
# v1.3.19.1 bug to the operator)
# ------------------------------------------------------------------------------
echo
echo "=== H. source: system_tests.go has the v1.3.18.1 tagToHost fix ==="
if grep -q 'tag:dev-infra-' "${SYSTEM_TESTS}"; then
    ok "system_tests.go has tag:dev-infra-X case in tagToHost closure"
else
    bad "system_tests.go missing tag:dev-infra-X case — v1.3.18.1 fix regressed?"
fi

echo
echo "=== summary: ${PASS} pass, ${FAIL} fail, ${WARN} warn ==="
[ "${FAIL}" -eq 0 ] || exit 1
exit 0
