#!/bin/bash
# scripts/check_b190.sh — B190: pin the absence of B188.3
# test data in the live DB. On 2026-08-25 the B188.3
# integration tests created:
#   - users:    b188_3_legacy, b188_3_nopref
#   - host:     b188_3_desktop (referenced from
#               device_rules + device_exit_node_prefs)
#   - host:     b188_3_phone (referenced from
#               device_rules)
# These were never cleaned up after the test run, so
# they polluted the production policy view (/admin/acls)
# with rules whose src=user. The cleanup on 2026-08-26
# deleted the rows. This B-check pins the absence so a
# future re-run of the B188.3 integration test doesn't
# silently re-accumulate them.
#
# Run after any deploy that might trigger B188.3 tests.
# Exit 0 on clean, non-zero on any test data found.

set -uo pipefail
REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

PASS=0
FAIL=0
ok()  { echo "  PASS  $1"; PASS=$((PASS+1)); }
bad() { echo "  FAIL  $1"; FAIL=$((FAIL+1)); }

DSN=$(grep '^SKYGATE_DB_DSN' .env 2>/dev/null | head -1 | cut -d= -f2-)
if [ -z "$DSN" ]; then
    echo "  SKIP  no DSN in .env (this is fine for a check that"
    echo "         only runs on the live DB — not in unit CI)"
    echo
    echo "=== B190 summary: $PASS passed, $FAIL failed, 0 skipped (env) ==="
    exit 0
fi
export PGPASSWORD=$(echo "$DSN" | sed -n 's|.*://[^:]*:\([^@]*\)@.*|\1|p')

# Helper: count rows matching LIKE pattern in a table.
count_like() {
    local tbl="$1" pat="$2" where="$3"
    local q="SELECT count(*) FROM $tbl WHERE $where LIKE '$pat'"
    psql "$DSN" -A -t -c "$q" 2>/dev/null | tr -d ' '
}

# --- A. portal_users ---
n_users=$(count_like portal_users 'b188%' username)
if [ "$n_users" = "0" ]; then
    ok "A.1 no b188_* users in portal_users"
else
    bad "A.1 found $n_users b188_* users in portal_users (expected 0)"
fi

# --- B. device_rules ---
n_rules=$(count_like device_rules 'b188%' device_hostname)
if [ "$n_rules" = "0" ]; then
    ok "B.1 no b188_* device_hostname in device_rules"
else
    bad "B.1 found $n_rules b188_* rules (expected 0)"
fi

# Also check by user_id (legacy B188.3 IDs were 6002/6003, but
# since we just deleted the users, no user_id-based rules
# survive CASCADE — defensive check)
n_user_rules=$(psql "$DSN" -A -t -c "SELECT count(*) FROM device_rules WHERE user_id IN (6002, 6003);" 2>/dev/null | tr -d ' ')
if [ "$n_user_rules" = "0" ]; then
    ok "B.2 no b188_3 user_id (6002/6003) in device_rules"
else
    bad "B.2 found $n_user_rules b188_3 user_id rules"
fi

# --- C. device_exit_node_prefs ---
n_prefs=$(count_like device_exit_node_prefs 'b188%' device_hostname)
if [ "$n_prefs" = "0" ]; then
    ok "C.1 no b188_* device_hostname in device_exit_node_prefs"
else
    bad "C.1 found $n_prefs b188_* prefs (expected 0)"
fi

# --- D. node_owner_map ---
n_nodes=$(count_like node_owner_map 'b188%' hostname)
if [ "$n_nodes" = "0" ]; then
    ok "D.1 no b188_* hostname in node_owner_map"
else
    bad "D.1 found $n_nodes b188_* nodes (expected 0)"
fi

# --- E. exit_node_health ---
n_health=$(count_like exit_node_health 'b188%' hostname)
if [ "$n_health" = "0" ]; then
    ok "E.1 no b188_* hostname in exit_node_health"
else
    bad "E.1 found $n_health b188_* health rows (expected 0)"
fi

# --- F. cross-table scan (defensive) ---
# Look for any 'b188_3' substring in any user-visible table
# we know about. New tables would need a manual add.
n_total=0
for tbl in portal_users devices device_rules device_exit_node_prefs \
           user_exit_node_prefs node_owner_map exit_node_health \
           preauth_keys personal_api_tokens mesh_members meshes \
           invite_codes; do
    case $tbl in
        preauth_keys)             col=user_id ;;
        personal_api_tokens)       col=user_id ;;
        mesh_members)              col=user_id ;;
        meshes)                    col=owner_id ;;
        invite_codes)             col=created_by ;;
        *)                         col=username ;;
    esac
    # Check for b188 pattern in the relevant string column.
    # We don't fail on individual mismatches here — just log
    # them so the B190 is exhaustive.
    n=$(psql "$DSN" -A -t -c "SELECT count(*) FROM $tbl WHERE '$col' IS NOT NULL AND '$col'::text LIKE 'b188%';" 2>/dev/null | tr -d ' ')
    if [ -n "$n" ] && [ "$n" != "0" ]; then
        echo "  INFO  F.1 $tbl.$col has $n b188_* rows (review)"
        n_total=$((n_total + n))
    fi
done
if [ "$n_total" = "0" ]; then
    ok "F.1 cross-table scan: no b188_* anywhere"
else
    bad "F.1 cross-table scan: $n_total b188_* rows leaked into other tables"
fi

echo
echo "=== B190 summary: $PASS passed, $FAIL failed ==="
exit "$FAIL"
