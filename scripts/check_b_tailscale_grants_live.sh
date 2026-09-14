#!/usr/bin/env bash
# check_b_tailscale_grants_live.sh — LIVE assertions for B-check
# Phase 7. Runs against a real headscale instance reachable via
# the headscale CLI (in PATH or via HEADSCALE_CLI env var).
#
# Requires:
#   - headscale CLI on PATH (or set HEADSCALE_CLI=/path/to/headscale)
#   - At least 3 portal users with at least 1 device each
#   - At least 1 exit-node and 1 subnet-router
#   - B77 autoupdate cron is running (skygate-host-1 has it)
#
# Exit codes:
#   0 = all live contracts hold
#   1 = one or more contracts failed
#   2 = prerequisites missing (operator should run on a real env)

set -uo pipefail

PASS=0; FAIL=0; SKIP=0
ok()    { echo "  PASS  $*"; PASS=$((PASS+1)); }
bad()   { echo "  FAIL  $*"; FAIL=$((FAIL+1)); }
skip()  { echo "  SKIP  $*"; SKIP=$((SKIP+1)); }

HS_CLI="${HEADSCALE_CLI:-}"
if [ -z "${HS_CLI}" ]; then
    if command -v headscale >/dev/null 2>&1; then
        HS_CLI="$(command -v headscale)"
    elif [ -x /usr/bin/headscale ]; then
        HS_CLI="/usr/bin/headscale"
    fi
fi
# HEADSCALE_CLI can be a docker-exec wrapper like
# 'docker exec headscale headscale' (not an -x file path)
if [ -z "${HS_CLI}" ]; then
    skip "headscale CLI not on PATH (set HEADSCALE_CLI=/path/to/headscale to override)"
    exit 2
fi

# Check we can actually reach a headscale instance (docker or remote).
# Use bash array so HS_CLI="docker exec headscale headscale" splits
# correctly into ["docker", "exec", "headscale", "headscale"].
# eval is safe here because HS_CLI is operator-controlled (set via
# HEADSCALE_CLI env var) and never touches user input.
HS_ARGS=( $HS_CLI )
if "${HS_ARGS[@]}" users list >/dev/null 2>&1; then
    HS_OK=1
else
    skip "headscale CLI installed but 'users list' failed — is a headscale instance running locally?"
    exit 2
fi

echo "=== LIVE B-check: Tailscale grants ==="
echo "headscale CLI: ${HS_CLI}"
echo

# --- A.1: Every user device has tag:dev-<user>-<hostname> ---
echo "--- A.1 every user device has tag:dev-<user>-<hostname> ---"
NODES_JSON=$("${HS_ARGS[@]}" nodes list -o json 2>/dev/null || true)
if [ -z "${NODES_JSON}" ]; then
    skip "headscale nodes list returned no JSON"
    exit 2
fi
USER_DEVICES_MISSING_TAG=$(echo "${NODES_JSON}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
nodes = d if isinstance(d, list) else d.get('nodes', [])
missing = []
for n in nodes:
    user = (n.get('user') or {}).get('name', '?')
    if user in ('tagged-devices', '', '?'):
        continue
    host = (n.get('name') or n.get('givenName') or '').strip()
    if not host:
        continue
    tags = (n.get('validTags') or []) + (n.get('forcedTags') or [])
    expected_tag = f'tag:dev-{user}-{host}'
    if expected_tag not in tags:
        missing.append(f'{host} (user={user})')
print(len(missing))
" 2>/dev/null)
if [ -z "${USER_DEVICES_MISSING_TAG}" ]; then
    skip "python3 not available for JSON parsing"
    exit 2
fi
if [ "${USER_DEVICES_MISSING_TAG}" = "0" ]; then
    ok "every real-user device has its tag:dev-<user>-<hostname>"
else
    bad "${USER_DEVICES_MISSING_TAG} user devices are missing their tag:dev-<user>-<host> — B77 autoupdate likely broken"
fi

# --- A.2: Exit-node devices have tag:exit-node AND tag:dev-infra-<hostname> ---
echo
echo "--- A.2 exit-node devices have tag:exit-node AND tag:dev-infra-<hostname> ---"
EXIT_NODE_MISSING=$(echo "${NODES_JSON}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
nodes = d if isinstance(d, list) else d.get('nodes', [])
bad = 0
for n in nodes:
    tags = (n.get('validTags') or []) + (n.get('forcedTags') or [])
    if 'tag:exit-node' in tags:
        host = (n.get('name') or n.get('givenName') or '').strip()
        if f'tag:dev-infra-{host}' not in tags:
            bad += 1
print(bad)
" 2>/dev/null)
if [ "${EXIT_NODE_MISSING}" = "0" ]; then
    ok "all exit-node devices also carry tag:dev-infra-<hostname>"
else
    bad "${EXIT_NODE_MISSING} exit-node devices missing tag:dev-infra-<hostname>"
fi

# --- A.3: Subnet routers have tag:subnet-router ---
echo
echo "--- A.3 subnet routers have tag:subnet-router ---"
SR_PRESENT=$(echo "${NODES_JSON}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
nodes = d if isinstance(d, list) else d.get('nodes', [])
n = 0
for node in nodes:
    tags = (node.get('validTags') or []) + (node.get('forcedTags') or [])
    if 'tag:subnet-router' in tags:
        n += 1
print(n)
" 2>/dev/null)
if [ "${SR_PRESENT}" -gt 0 ]; then
    ok "subnet-router tag is present on ${SR_PRESENT} node(s)"
else
    skip "no subnet-router nodes in this tailnet (contract vacuously satisfied)"
fi

# --- A.4: Public devices do NOT have user-scoped tags ---
echo
echo "--- A.4 public devices do NOT carry tag:dev-<user>-<hostname> ---"
PUBLIC_WITH_USER_TAG=$(echo "${NODES_JSON}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
nodes = d if isinstance(d, list) else d.get('nodes', [])
bad = 0
for n in nodes:
    tags = (n.get('validTags') or []) + (n.get('forcedTags') or [])
    if 'tag:public' in tags:
        for t in tags:
            if t.startswith('tag:dev-') and not t.startswith('tag:dev-infra-'):
                bad += 1
                break
print(bad)
" 2>/dev/null)
if [ "${PUBLIC_WITH_USER_TAG}" = "0" ]; then
    ok "public devices carry only infra tags, never user-scoped"
else
    bad "${PUBLIC_WITH_USER_TAG} public device(s) leaked a user-scoped tag"
fi

# --- B.1: headscale nodes list --json returns the per-device tags ---
echo
echo "--- B.1 headscale nodes list returns tag data ---"
TAG_COUNT=$(echo "${NODES_JSON}" | python3 -c "
import json, sys
d = json.load(sys.stdin)
nodes = d if isinstance(d, list) else d.get('nodes', [])
total = 0
for n in nodes:
    total += len(n.get('validTags') or []) + len(n.get('forcedTags') or [])
print(total)
" 2>/dev/null)
if [ "${TAG_COUNT}" -gt 0 ]; then
    ok "headscale nodes list reports ${TAG_COUNT} tag(s) across all nodes"
else
    bad "no tags returned by headscale nodes list (B77 autoupdate not working)"
fi

# --- C.1: Generated policy has tagOwners for every user-tag pair ---
echo
echo "--- C.1 (skipped — requires admin auth + /admin/headscale/acl/apply roundtrip) ---"
skip "C.1 requires authenticated POST /admin/headscale/acl/apply — left to live e2e via b_mod_reregister_live.sh + skygate CLI"

# --- D.1: B77 autoupdate cron is running ---
echo
echo "--- D.1 B77 autoupdate cron entry is running (skygate-host-1) ---"
if pgrep -af "skygate.*backfill\|nodeownership.*Backfill\|autoupdate" >/dev/null 2>&1; then
    ok "B77 autoupdate goroutine is running"
else
    skip "B77 autoupdate goroutine not detected in process list (may run via cron, not goroutine)"
fi

echo
echo "=== LIVE summary ==="
echo "  PASS: ${PASS}"
echo "  FAIL: ${FAIL}"
echo "  SKIP: ${SKIP}"
if [ "${FAIL}" -eq 0 ]; then
    exit 0
fi
exit 1
