#!/usr/bin/env bash

# Live-state check: skip (do not fail) when the docker daemon is unreachable.
. "$(dirname "$0")/lib/skip_if_no_docker.sh"
# check_b_duplicate_users.sh — verify headscale has exactly one user per name.
#
# 2026-09-12 (B243 follow-up): direct regression test for the live
# skyadmin id=1 vs id=86 incident. The pre-B243 reconcile cron
# silently picked the LAST occurrence in the byName map when
# headscale had duplicates, relinking portal.skyadmin from id=1
# (bootstrap admin, 7 devices) to id=86 (OIDC duplicate, 0 devices).
# B243 fixed the cron to refuse silent relink + write audit, but the
# HEADSCALE-SIDE state (duplicates) can still trigger the bug path.
# This B-check catches the headscale state directly.
#
# Catches:
#   - headscale OIDC auto-create (v0.29.1 by design): when an OIDC
#     login arrives with a name that already exists in headscale
#     (e.g. "skyadmin"), headscale creates a NEW user with
#     provider=oidc. Bootstrap admin (provider=<empty>) + OIDC user
#     (provider=oidc) coexist.
#   - Operator manually created duplicate via CLI/API.
#
# Does NOT catch:
#   - The portal-side relink (covered by check_b_admin_user_sync.sh
#     contracts C/D/E + B243 cron refusal).
#   - The audit_log write failure (covered by
#     check_b_reconcile_audit_writes.sh for B243 SQL fix).
#
# Usage:
#   bash scripts/check_b_duplicate_users.sh                # exit 1 if duplicates
#   bash scripts/check_b_duplicate_users.sh --strict       # same; reserved for parity
#
# Exit codes:
#   0 = no duplicates
#   1 = duplicates found
#   2 = backend unknown / CLI error

set -uo pipefail

STRICT=0
for arg in "$@"; do
    case "$arg" in
        --strict) STRICT=1 ;;
    esac
done

CONTAINER="${SKYGATE_CONTAINER:-skygate-skygate-1}"
HEADSCALE_CONTAINER="${HEADSCALE_CONTAINER:-headscale}"

# ── containers reachable ──
if ! sudo docker inspect "$CONTAINER" >/dev/null 2>&1; then
    echo "  FAIL  $CONTAINER container not running"
    exit 1
fi
if ! sudo docker inspect "$HEADSCALE_CONTAINER" >/dev/null 2>&1; then
    echo "  FAIL  $HEADSCALE_CONTAINER container not running"
    exit 1
fi

# ── fetch + parse headscale users ──
sudo docker exec "$HEADSCALE_CONTAINER" headscale users list -o json > /tmp/check_b_dup_users.json 2>/dev/null
if [ ! -s /tmp/check_b_dup_users.json ]; then
    echo "  FAIL  headscale users list returned empty (headscale unreachable or CLI errored)"
    exit 2
fi

# Detect duplicates with a small Python helper (json + Counter).
python3 > /tmp/check_b_dup_result.txt << 'PYEOF'
import json, collections
users = json.load(open('/tmp/check_b_dup_users.json'))
counts = collections.Counter(u['name'] for u in users)
total = len(users)
dups = {n: c for n, c in counts.items() if c > 1}
if not dups:
    print(f"OK total={total}")
else:
    print(f"DUP total={total}")
    for name in sorted(dups):
        matches = [u for u in users if u['name'] == name]
        ids = ', '.join(f"id={u['id']}/provider={u.get('provider','') or '<empty>'}" for u in matches)
        print(f"NAME {name} count={dups[name]} {ids}")
PYEOF

FIRST_LINE=$(head -1 /tmp/check_b_dup_result.txt)
if [[ "$FIRST_LINE" == OK* ]]; then
    TOTAL=$(echo "$FIRST_LINE" | sed 's/^OK total=//')
    echo "  PASS  A: headscale has no duplicate names ($TOTAL users total)"
    exit 0
fi

# Duplicates found — print detail + remediation hint.
echo "  FAIL  A: headscale has duplicate user names (operator must delete the extras)"
echo
sed -n '2,$p' /tmp/check_b_dup_result.txt | while IFS= read -r line; do
    case "$line" in
        NAME*)
            name=$(echo "$line" | awk '{print $2}')
            count=$(echo "$line" | awk '{print $3}' | sed 's/count=//')
            echo "    $count x user named \"$name\":"
            echo "$line" | sed 's/.* //' | tr ',' '\n' | sed 's/^/      /'
            ;;
    esac
done
echo
echo "  Remediation:"
echo "    sudo docker exec $HEADSCALE_CONTAINER headscale users delete -i <duplicate-id> --force"
echo "    (verify with: sudo docker exec $HEADSCALE_CONTAINER headscale users list)"
echo
echo "  After deleting, the B243 reconcile cron will auto-relink portal_users"
echo "  on the next cycle (every 1h, or trigger manually via /admin/headscale/reconcile)."
exit 1
