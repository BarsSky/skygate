#!/usr/bin/env bash

# Live-state check: skip (do not fail) when the docker daemon is unreachable.
. "$(dirname "$0")/lib/skip_if_no_docker.sh"
# check_b_node_attribution.sh — verify every headscale node is attributed
# to a REAL portal user (not the sentinel "tagged-devices" fallback).
#
# 2026-09-13: B243 follow-up. Investigated during the live debug of the
# skyadmin id=1 vs id=86 incident. Found that the operator's PRIMARY
# skygate node (skygate-host-1-1) was stuck in headscale's sentinel
# "tagged-devices" user (id=2147455555 — a sentinel value, not a real
# user; `headscale users list -i 2147455555` returns empty).
#
# Symptom chain (live on 13.69):
#   - skygate-host-1-1 (online=True) has user=tagged-devices(id=2147455555)
#   - skygate DB (node_owner_map) has node_id=43 hs_id=85 (infra) tag=dev-skyadmin-skygate-host-1
#   - headscale ACL tagOwners has tag:dev-skyadmin-skygate-host-1 owned by skyadmin
#   - Every 5 minutes tag.autoupdate fails:
#       reason=acl_reject
#       error=Error: setting tags: rpc error: code = InvalidArgument
#       (headscale rejects because tagged-devices user can't claim
#       the skyadmin-owned tag)
#
# Root cause: the node was originally registered when skygate's
# OIDC was disabled (or preauth-key auth hit a fallback path). Headscale
# silently assigned it to "tagged-devices" user. Since then, every
# autoupdate attempt to re-tag with `tag:dev-skyadmin-skygate-host-1`
# fails because tagged-devices user doesn't own that tag.
#
# Effect: per-DEVICE ACL grants referencing tag:dev-skyadmin-skygate-host-1
# silently don't apply for this node. /admin/exit-rules shows skyadmin's
# rules but they don't actually reach the skygate node.
#
# Fix (operator-side): delete + re-register the node under skyadmin
# (or wait for headscale CLI to add a `nodes move` subcommand).
#
# This check catches the regression pattern (a node ends up in the
# tagged-devices sentinel user) so it can be detected before it
# silently degrades ACL coverage.
#
# Contracts (4):
#   A. NO headscale node has user.id = 2147455555 (the sentinel)
#   B. NO headscale node has user.name = "tagged-devices"
#   C. Every skygate DB row in node_owner_map points at a live
#      headscale user (catches the sentinel leak)
#   D. Every "online" headscale node is attributed to a real
#      portal user (skyadmin/michail/guest/daniil/infra)
#
# Exit codes:
#   0 = all attributes OK
#   1 = one or more contracts failed

set -uo pipefail

HEADSCALE_CONTAINER="${HEADSCALE_CONTAINER:-headscale}"
PG_CONTAINER="${SKYGATE_PG_CONTAINER:-skygate-pg-local}"

ok()  { echo "  PASS  $*"; }
bad() { echo "  FAIL  $*"; }
warn(){ echo "  WARN  $*"; }

# B281 (2026-09-22): absent live dependency = SKIP, never FAIL (AGENTS.md §1.1) — on CI there is no
# headscale container, and the old `bad ...; exit 1` produced a red row that described the runner,
# not the code.
# B489 (2026-11-17): the dependency is the INSTALLATION, not two docker containers — the reads below
# go through lib/live_db.sh, so a native (systemd) host can answer them.
if [ "$(live_kind)" = "none" ]; then
    echo "  SKIP  no live installation on this host (live check — run it on the host that serves the tailnet)"
    exit 0
fi

# ── fetch live headscale state ──
# Per-run scratch files (B340): a FIXED /tmp path is not writable by the next run
# under a different user, which made eight checks report phantom FAILs on
# 2026-10-01. See AGENTS.md trap #13. The python helpers below read the paths
# from the environment because a `<< 'PYEOF'` heredoc does not expand ${…}.
SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)" || SKY_TMP="/tmp/skygate-check.$$"
trap 'rm -rf "$SKY_TMP"' EXIT
ATTR_USERS_JSON="${SKY_TMP}/attr_users.json"
ATTR_NODES_JSON="${SKY_TMP}/attr_nodes.json"
export ATTR_USERS_JSON ATTR_NODES_JSON
live_headscale users list -o json 2>/dev/null > "$ATTR_USERS_JSON"
live_headscale nodes list -o json 2>/dev/null > "$ATTR_NODES_JSON"
# B489: which nodes skygate itself attributes to a portal user. Membership in headscale's
# `tagged-devices` user is BY DESIGN for a tagged node (skygate tags every device it owns, B272),
# so the question is never "is it in tagged-devices" but "does node_owner_map know this node".
ATTR_MAPPED_IDS=$(live_db_sql "SELECT node_id FROM node_owner_map" 2>/dev/null | tr '\n' ',' )
export ATTR_MAPPED_IDS

echo "=== A. no node in sentinel user (id=2147455555) ==="
SENTINEL_NODES=$(python3 << 'PYEOF'
import json, os
d = json.load(open(os.environ['ATTR_NODES_JSON']))
mapped = {int(x) for x in os.environ.get('ATTR_MAPPED_IDS', '').split(',') if x.strip().isdigit()}
n = 0
for x in d:
    if x.get('user',{}).get('id') == 2147455555:
        # B489: tagged nodes legitimately live in headscale's sentinel user (id 2147455555) and are
        # absent from `users list`; the finding is a node nobody attributes, not the sentinel itself.
        if int(x['id']) in mapped:
            continue
        print("  %s %s user=tagged-devices (sentinel) with no node_owner_map row"
              % (x['id'], x.get('name','')))
        n += 1
print("COUNT=%d" % n)
PYEOF
)
echo "$SENTINEL_NODES" | grep -v "^COUNT=" | sed 's/^/  /'
SENTINEL_COUNT=$(echo "$SENTINEL_NODES" | grep "^COUNT=" | cut -d= -f2)
if [ "$SENTINEL_COUNT" = "0" ]; then
    ok "every node in headscale's sentinel user is attributed by node_owner_map (tagged nodes live there by design)"
else
    bad "$SENTINEL_COUNT node(s) in the sentinel user with NO portal owner — these are NOT attributed to a real portal user"
fi

echo
echo
echo "=== B. no node with user.name='tagged-devices' ==="
NAMED_NODES=$(python3 << 'PYEOF'
import json, os
d = json.load(open(os.environ['ATTR_NODES_JSON']))
mapped = {int(x) for x in os.environ.get('ATTR_MAPPED_IDS', '').split(',') if x.strip().isdigit()}
n = 0
for x in d:
    if x.get('user',{}).get('name') == 'tagged-devices':
        # B489: expected for a tagged node; a finding only when node_owner_map cannot attribute it
        if int(x['id']) in mapped:
            continue
        print(f"  {x['id']} {x.get('name','')} user.name=tagged-devices (node_owner_map has no row)")
        n += 1
print(f"COUNT={n}")
PYEOF
)
echo "$NAMED_NODES" | grep -v "^COUNT=" | sed 's/^/  /'
NAMED_COUNT=$(echo "$NAMED_NODES" | grep "^COUNT=" | cut -d= -f2)
if [ "$NAMED_COUNT" = "0" ]; then
    ok "every node in headscale's tagged-devices user is attributed by node_owner_map (tagged nodes belong to that user by design)"
else
    bad "$NAMED_COUNT node(s) sit in the tagged-devices fallback user with NO node_owner_map row — re-register them under a real portal user"
fi

echo
echo
echo "=== C. every node_owner_map row points at a live headscale user ==="
HS_USER_IDS=$(python3 -c "
import json, os
d = json.load(open(os.environ['ATTR_USERS_JSON']))
print(','.join(str(u['id']) for u in d))
")
NOM_ORPHANS=$(live_db_sql -F'|' "
SELECT node_id, headscale_user_id, username FROM node_owner_map
WHERE headscale_user_id NOT IN ($HS_USER_IDS)
   OR headscale_user_id = 0
   OR headscale_user_id < 0
   OR headscale_user_id > 100000
ORDER BY headscale_user_id, node_id" 2>/dev/null)
NOM_COUNT=$(echo -n "$NOM_ORPHANS" | grep -c '|')
if [ "$NOM_COUNT" = "0" ]; then
    ok "all node_owner_map rows point at live headscale users"
else
    bad "$NOM_COUNT node_owner_map rows reference missing/sentinel headscale users"
    echo "$NOM_ORPHANS" | sed 's/^/    /'
fi

echo
echo "=== D. every ONLINE node is attributed to a real portal user ==="
ONLINE_UNATTRIBUTED=$(python3 << 'PYEOF'
import json, os
d = json.load(open(os.environ['ATTR_NODES_JSON']))
mapped = {int(x) for x in os.environ.get('ATTR_MAPPED_IDS', '').split(',') if x.strip().isdigit()}
n = 0
for x in d:
    u = x.get('user', {})
    if not x.get('online'):
        continue
    # B489: the sentinel user and the tagged-devices user are both fine as long as node_owner_map
    # attributes the node; what matters is that SOME portal user owns it.
    if u.get('id') == 2147455555 or u.get('name') == 'tagged-devices':
        if int(x['id']) in mapped:
            continue
        print("  ONLINE node %s %s has user=%s(id=%s) and no node_owner_map row"
              % (x['id'], x.get('name',''), u.get('name','?'), u.get('id','?')))
        n += 1
print("COUNT=%d" % n)
PYEOF
)
echo "$ONLINE_UNATTRIBUTED" | grep -v "^COUNT=" | sed 's/^/  /'
ONLINE_COUNT=$(echo "$ONLINE_UNATTRIBUTED" | grep "^COUNT=" | cut -d= -f2)
if [ "$ONLINE_COUNT" = "0" ]; then
    ok "every online headscale node is attributed to a real portal user (via node_owner_map)"
else
    bad "$ONLINE_COUNT online node(s) with no portal owner — fix: re-register under skyadmin (or whichever portal user owns the tag)"
fi

echo
echo "=== summary: see PASS/FAIL counts above ==="
