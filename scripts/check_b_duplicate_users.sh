#!/usr/bin/env bash

# Live-state check: skip (do not fail) when the docker daemon is unreachable.
. "$(dirname "$0")/lib/skip_if_no_docker.sh"
# B479: docker being reachable does not mean the STACK is running (a retired host
# keeps the daemon and the container names). Skip when there is no live pair.
. "$(dirname "$0")/lib/skip_if_no_live_stack.sh"
. "$(dirname "$0")/lib/live_db.sh"
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

# ── the installation reachable ──
# B281 (2026-09-22): no live dependency = SKIP, never FAIL (AGENTS.md §1.1).
# B488 (TD-37): the question is now the one the check actually needs — "can THIS host answer?" — asked
# through lib/live_db.sh, which knows the docker pair AND the native (systemd) install. The old form
# inspected `docker inspect` twice, so a native host skipped this contract forever.
LIVE_KIND="$(live_kind)"
if [ "$LIVE_KIND" = "none" ]; then
    echo "  SKIP  no running skygate+headscale docker pair and no /etc/skygate/skygate.env (live check — run it on the host that serves the tailnet)"
    exit 0
fi

# ── fetch + parse headscale users ──
# Per-run scratch files (B340): a FIXED /tmp path is not writable by the next
# run under a different user, which made eight checks report phantom FAILs on
# 2026-10-01. See AGENTS.md trap #13. The python helper below reads the path
# from the environment because a `<< 'PYEOF'` heredoc does not expand ${…}.
SKY_TMP="$(mktemp -d /tmp/skygate-check.XXXXXX)" || SKY_TMP="/tmp/skygate-check.$$"
trap 'rm -rf "$SKY_TMP"' EXIT
DUP_USERS_JSON="${SKY_TMP}/check_b_dup_users.json"
DUP_RESULT_TXT="${SKY_TMP}/check_b_dup_result.txt"
export DUP_USERS_JSON

live_headscale users list -o json > "$DUP_USERS_JSON" 2>/dev/null
if [ ! -s "$DUP_USERS_JSON" ]; then
    echo "  FAIL  headscale users list returned empty (headscale unreachable or CLI errored)"
    exit 2
fi

# Detect duplicates with a small Python helper (json + Counter).
python3 > "$DUP_RESULT_TXT" << 'PYEOF'
import json, collections, os
users = json.load(open(os.environ['DUP_USERS_JSON']))
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

FIRST_LINE=$(head -1 "$DUP_RESULT_TXT")
if [[ "$FIRST_LINE" == OK* ]]; then
    TOTAL=$(echo "$FIRST_LINE" | sed 's/^OK total=//')
    echo "  PASS  A: headscale has no duplicate names ($TOTAL users total)"
    exit 0
fi

# Duplicates found — print detail + remediation hint.
echo "  FAIL  A: headscale has duplicate user names (operator must delete the extras)"
echo
sed -n '2,$p' "$DUP_RESULT_TXT" | while IFS= read -r line; do
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
echo "  Remediation: delete the extras with the headscale CLI of THIS install:"
case "$LIVE_KIND" in
    docker)
        echo "    sudo docker exec $HEADSCALE_CONTAINER headscale users delete -i <duplicate-id> --force"
        ;;
    *)
        echo "    sudo headscale users delete -i <duplicate-id> --force"
        ;;
esac
echo "    (verify with: the same CLI, users list -o json)"
echo
echo "  After deleting, the B243 reconcile cron will auto-relink portal_users"
echo "  on the next cycle (every 1h, or trigger manually via /admin/headscale/reconcile)."
exit 1
