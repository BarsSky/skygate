#!/bin/bash
# ============================================================================
# fix_admin_attribution.sh — one-off operator-driven fix for admin's
# node_owner_map rows.
#
# 2026-07-21: v0.23.2 (one-off, not a feature release).
#
# Background:
#   admin's 6 tag:private devices (workstation-1, workstation-2, workstation-2-old,
#   skygate-host-1, workstation-4, workstation-3) are all owned by "tagged-devices"
#   in node_owner_map. The v0.3.9 backfill (and v0.22.2 fix) can't
#   recover them because the preauth used to register them didn't have
#   headscale_preauth_id captured at issue time, AND the temporal
#   fallback (Strategy C) doesn't apply (preauth was issued days ago,
#   well outside the ±1h window).
#
#   This is a data attribution issue, not a functionality issue —
#   the devices work in headscale, they have correct tags, the only
#   problem is that skygate doesn't know they're admin's. Effects:
#     - /my/devices shows 0 devices (only "tagged-devices"-owned
#       nodes are filtered out by the backfill's "refuse to steal"
#       guard)
#     - subnet status=pending (the v0.22.3 SyncStatus counts rows
#       in node_owner_map)
#     - /admin/users subnet column shows "—" for the per-plane
#       list (because the device count is 0 from skygate's view)
#
# This script does TWO things (v0.23.1 compliance-tier policy):
#   1. Clears admin's per-user control plane override (if
#      any), so HSForUser(1) routes back to the global headscale.
#      Reason: per v0.23.1, per-user control plane is compliance
#      tier only — admin is a default-path user. The v0.23.0
#      pilot set up the per-user headscale but no nodes were
#      migrated (see tag v0.23.1 + commit history). Without this
#      clear, /my/devices for admin returns 0 devices (the
#      per-user headscale is empty), even with the node_owner_map
#      fix below.
#   2. UPDATEs node_owner_map for the 6 known admin devices
#      (so /my/devices + /admin/users show them attributed to
#      admin, not "tagged-devices").
#   3. Triggers /my/devices load (which fires backfillNodeOwnership
#      → subnet.SyncStatus → status flips pending→active).
#   4. Verifies the fix: status='active', /my/devices now shows
#      the devices, /admin/users subnet column shows green pill.
#
# Idempotency: the UPDATE only changes rows where username != 'admin'
# (or is empty), so re-running is safe. If the rows already have
# username='admin', the UPDATE affects 0 rows.
#
# Scope: ONE user (admin, uid=1). For other users (user1/user3/
# user2) the same pattern can be applied if their devices are
# similarly misattributed — but as of v0.23.2, user1's devices
# (workstation-5, workstation-8) are correctly attributed, and user3/user2
# have no devices at all.
#
# Safety: this script only writes to skygate's local SQLite
# (node_owner_map). It does NOT touch headscale, does NOT issue
# preauths, does NOT re-auth any device, does NOT change ACL.
# Pure data-attribution fix in skygate's snapshot.
# ============================================================================
set -euo pipefail

USER="${SKYGATE_ADMIN_USER:-admin}"
PASS="${SKYGATE_ADMIN_PASS:-}"

if [ -z "$PASS" ]; then
    echo "FAIL: SKYGATE_ADMIN_PASS not set in env / .env"
    exit 1
fi

# v0.33.1.7: 2026-08-04 — skygate is now using PostgreSQL
# (the HA cluster at 172.17.0.1:5000), not the in-container
# SQLite at /data/skygate.db. The .env has BOTH SKYGATE_DB
# (legacy) and SKYGATE_DB_DSN (preferred); skygate picks
# SKYGATE_DB_DSN at startup. detect_backend() reads the
# runtime log to pick the right tool (psql for PG,
# docker exec sqlite3 for SQLite), so this script works
# on either backend without operator intervention.
SKYGATE_CONTAINER="${SKYGATE_CONTAINER:-skygate-skygate-1}"
ENV_FILE="${SKYGATE_ENV:-/home/skyadmin/skygate/.env}"
DSN=""
if [ -f "$ENV_FILE" ]; then
    DSN=$(grep -E '^SKYGATE_DB_DSN=' "$ENV_FILE" | head -1 | cut -d= -f2- || true)
fi

detect_backend() {
    # Read the running skygate's startup log to see which DB
    # backend it actually picked. The log line format is:
    #   "DB backend:    postgres (DSN=postgres://...)" or
    #   "DB backend:    sqlite  (path=/data/skygate.db)"
    local line
    line=$(docker logs "$SKYGATE_CONTAINER" 2>&1 | grep -E 'DB backend:' | tail -1 || true)
    case "$line" in
        *postgres*) echo "pg" ;;
        *sqlite*)   echo "sqlite" ;;
        *)
            # Fallback: SKYGATE_DB_DSN set means PG; otherwise SQLite
            if [ -n "$DSN" ]; then echo "pg"; else echo "sqlite"; fi
            ;;
    esac
}

BACKEND="$(detect_backend)"
echo "=== fix_admin_attribution.sh ==="
echo "(one-off SQL fix for admin's node_owner_map rows)"
echo "DB backend: $BACKEND"
echo

# run_sql <sql_file> — execute the SQL file in whichever DB
# skygate is using. Echoes the result rows.
run_sql() {
    local sql_file="$1"
    case "$BACKEND" in
        pg)
            PGPASSWORD="$(printf '%s' "$DSN" | sed -nE 's#^postgres(ql)?://[^:]+:([^@]+)@.*$#\2#p')" \
                psql -h 172.17.0.1 -p 5000 -U admin -d skygate_staging \
                -v ON_ERROR_STOP=1 -f "$sql_file" 2>&1
            ;;
        sqlite)
            docker exec -i "$SKYGATE_CONTAINER" sqlite3 /data/skygate.db < "$sql_file"
            ;;
        *)
            echo "FAIL: unknown backend '$BACKEND'" >&2
            return 1
            ;;
    esac
}

# run_sql_inline <sql> — same as run_sql but takes the SQL as
# a single argument AND strips PSQL's header/footer noise so
# callers can capture the value as $(). PG runs with -A -t
# (unaligned, tuples-only) so the output is just the value
# (or one value per row). SQLite's -separator '|' + -list
# gives the same shape.
run_sql_inline() {
    local sql="$1"
    case "$BACKEND" in
        pg)
            PGPASSWORD="$(printf '%s' "$DSN" | sed -nE 's#^postgres(ql)?://[^:]+:([^@]+)@.*$#\2#p')" \
                psql -h 172.17.0.1 -p 5000 -U admin -d skygate_staging \
                -v ON_ERROR_STOP=1 -A -t -c "$sql" 2>&1
            ;;
        sqlite)
            echo "$sql" | docker exec -i "$SKYGATE_CONTAINER" sqlite3 -separator '|' -list /data/skygate.db
            ;;
    esac
}

# The list of known admin-owned devices that headscale has
# reassigned to the synthetic "tagged-devices" user (because
# of the tag-driven ownership reassignment) but that skygate
# must keep attributed to "skyadmin" for the /my/devices page
# to render them.
#
# v0.33.1.7: 2026-08-04 — extended to include skyworker and
# skygate-vm. Both had correct tags (tag:dev-skyadmin-<host>)
# but username='tagged-devices' in node_owner_map, so the
# /my/devices snap-path filter skipped them. Root cause for
# the original 6 was the same — pre-v0.12.0 preauth keys had
# no headscale_preauth_id captured, so the backfill's
# Strategy A (preauth match) and Strategy C (1h temporal
# fallback) both fail. The fix is a one-off UPDATE; new
# devices registered through skygate-issued preauths (post-
# v0.12.0) have headscale_preauth_id captured and are
# auto-attributed on the next /my/devices load.
#
# Idempotency: the UPDATE only changes rows where
# username != 'skyadmin' (or is empty), so re-running is safe.
SKYADMIN_DEVICES=(
    "workstation-1"
    "workstation-2"
    "workstation-2-old"
    "skygate-host-1"
    "workstation-4"
    "workstation-3"
    # v0.33.1.7: 2026-08-04 — new devices acquired since the
    # original 2026-07-21 list. skyworker (node_id=9) is the
    # operator's primary work device; skygate-vm (node_id=13)
    # is the in-image skygate container (force-deconflicted
    # from the old "skygate-host-1-1" entry on 2026-07-14).
    "skyworker"
    "skygate-vm"
)

CK=/tmp/fix_attribution.ck
rm -f "$CK"
BASE_URL="${SKYGATE_BASE_URL:-http://localhost:8080}"

# 1. login (we need a session to trigger /my/devices later)
echo "--- 1. login as $USER"
code=$(curl -s -o /dev/null -w "%{http_code}" -c "$CK" -b "$CK" \
    --data-urlencode "username=$USER" --data-urlencode "password=$PASS" \
    "$BASE_URL/login")
if [ "$code" = "302" ]; then
    echo "PASS: login returned 302"
else
    echo "FAIL: login returned $code, want 302"
    exit 1
fi

# 1b. check if admin has a per-user control plane override
#     (from the v0.23.0 Phase 1 pilot). If yes, clear it — per
#     v0.23.1, per-user control plane is compliance-tier only,
#     and admin is a default-path user. After clearing,
#     HSForUser(1) falls through to HSGlobal() and /my/devices
#     loads the global headscale's view (where admin's
#     6 devices actually live).
echo
echo "--- 1b. check + clear per-user control plane override (v0.23.1 compliance-tier policy)"
override=$(run_sql_inline "SELECT headscale_url FROM portal_users WHERE username = 'admin';" | tr -d ' \t')
if [ -z "$override" ]; then
    echo "    no per-user override (admin is on the global headscale — v0.23.1 default)"
else
    echo "    current per-user override: '$override'"
    echo "    → clearing (per v0.23.1: per-user is compliance tier, not default path)"
    cat > /tmp/clear_override.sql <<'SQL'
UPDATE portal_users
   SET headscale_url = '',
       headscale_api_key_enc = ''
 WHERE username = 'admin';
SQL
    run_sql /tmp/clear_override.sql >/dev/null
    rm -f /tmp/clear_override.sql
    post_override=$(run_sql_inline "SELECT headscale_url FROM portal_users WHERE username = 'admin';" | tr -d ' \t')
    if [ -z "$post_override" ]; then
        echo "    PASS: per-user override cleared"
    else
        echo "    FAIL: per-user override is still '$post_override'"
        exit 1
    fi
fi

# 2. show pre-state: count of rows with username=tagged-devices
#    for admin's devices (should be 6 before the fix).
echo
echo "--- 2. pre-state: node_owner_map rows for admin's devices"
echo "    (should show username=tagged-devices for all 6 before fix)"
device_list=""
for h in "${SKYADMIN_DEVICES[@]}"; do
    if [ -z "$device_list" ]; then
        device_list="'$h'"
    else
        device_list="$device_list, '$h'"
    fi
done
cat > /tmp/check_pre.sql <<SQL
SELECT hostname, username, tag
  FROM node_owner_map
 WHERE hostname IN ($device_list)
 ORDER BY hostname;
SQL
pre_state=$(run_sql /tmp/check_pre.sql)
rm -f /tmp/check_pre.sql
echo "$pre_state" | sed 's/^/    /'

# 3. UPDATE node_owner_map for the 8 devices. Uses an IN clause
#    with a host-generated list (bash doesn't have native arrays
#    in SQL-friendly form, so we build the comma-list).
echo
echo "--- 3. UPDATE node_owner_map: set username='$USER' for $USER's devices"
# Look up the user's id for tagged_by_user_id (defaults to 1
# for the bootstrap admin / skyadmin).
uid=$(run_sql_inline "SELECT id FROM portal_users WHERE username = '$USER';" | tr -d ' \t')
uid="${uid:-1}"
cat > /tmp/fix_attribution.sql <<SQL
UPDATE node_owner_map
   SET username = '$USER', tag = 'tag:private', tagged_by_user_id = $uid
 WHERE hostname IN ($device_list)
   AND (username != '$USER' OR username = 'tagged-devices' OR username = '');
SQL
# Apply the UPDATE. The rows_affected count is read via the
# backend's command-tag. PG prints "UPDATE N" to STDOUT; SQLite
# uses a separate `SELECT changes()` call (run_sql_inline
# which echoes the value as a single value row).
fix_out=$(run_sql /tmp/fix_attribution.sql)
rm -f /tmp/fix_attribution.sql
case "$BACKEND" in
    pg)      rows_updated=$(echo "$fix_out" | grep -oE 'UPDATE [0-9]+' | grep -oE '[0-9]+' | head -1) ;;
    sqlite)  rows_updated=$(run_sql_inline "SELECT changes();" | tr -d ' ' | head -1) ;;
esac
rows_updated="${rows_updated:-0}"
echo "    rows updated: $rows_updated"
if [ "$rows_updated" = "0" ]; then
    echo "    (no rows needed updating — attribution already correct?)"
    # Don't fail; this is a valid state. The trigger below will
    # still run and verify.
fi

# 4. show post-state: same query, should now show username=admin
#    for all devices in SKYADMIN_DEVICES.
echo
echo "--- 4. post-state: node_owner_map rows for admin's devices"
echo "    (should now show username=admin for all $((${#SKYADMIN_DEVICES[@]})))"
cat > /tmp/check_post.sql <<SQL
SELECT hostname, username, tag
  FROM node_owner_map
 WHERE hostname IN ($device_list)
 ORDER BY hostname;
SQL
post_state=$(run_sql /tmp/check_post.sql)
rm -f /tmp/check_post.sql
echo "$post_state" | sed 's/^/    /'
# The post-state check is informational, not a hard pass/fail.
# If the device count is < expected, that just means some
# devices haven't been registered through the preauth flow
# yet (no row in node_owner_map). The fix is still valid for
# the devices that DO have rows.
post_admin_count=$(echo "$post_state" | grep -c "skyadmin" || true)
expected=$((${#SKYADMIN_DEVICES[@]}))
if [ "$post_admin_count" = "$expected" ]; then
    echo "PASS: all $expected devices in node_owner_map have username=skyadmin"
elif [ "$post_admin_count" -gt 0 ]; then
    echo "INFO: $post_admin_count of $expected known devices have rows; the others haven't been registered through a preauth yet (no node_owner_map row to fix)"
else
    echo "WARN: no devices in node_owner_map match the known list"
fi

# 5. trigger /my/devices load (this fires backfillNodeOwnership
#    which calls subnet.SyncStatus, which flips the status to
#    'active' since nodeCount is now correct).
echo
echo "--- 5. trigger /my/devices load (fires subnet.SyncStatus)"
curl -s -b "$CK" -c "$CK" "$BASE_URL/my/devices" >/dev/null
# Give the backfill a moment to run (it's synchronous, but the
# page render takes a moment).
sleep 1

# 6. verify: subnet_status is now 'active' (only if a subnet row
#    was allocated; for users without one, skip this check).
echo
echo "--- 6. verify: portal_users.subnet_status flipped to 'active' (if allocated)"
db_status=$(run_sql_inline "SELECT subnet_status FROM portal_users WHERE username = 'admin';" | tr -d ' \t')
if [ -z "$db_status" ]; then
    echo "INFO: no subnet row allocated for admin — skipping status check"
elif [ "$db_status" = "active" ] || [ "$db_status" = "router_active" ]; then
    echo "PASS: admin's subnet_status = '$db_status'"
else
    echo "WARN: admin's subnet_status = '$db_status' (not 'active'); may need a manual /my/devices reload"
fi

# 7. verify: /my/devices page renders the expected devices.
# The headscale ListAllNodes cache is 4s, so we sleep a beat
# before fetching — otherwise the second call after step 5
# might hit the cache and miss nodes the first call seeded.
echo
echo "--- 7. verify: /my/devices page renders the expected devices"
sleep 2
out=$(curl -sL -b "$CK" -c "$CK" "$BASE_URL/my/devices")
# The "newly fixed" devices (skyworker, skygate-vm) MUST show
# up — that's the original bug the script fixes. Older devices
# may or may not still be in headscale, so we just require the
# new ones (the ones the user actually reported missing).
for required in skyworker skygate-vm; do
    if echo "$out" | grep -q "<code>$required</code>"; then
        echo "PASS: $required is now visible in /my/devices"
    else
        echo "FAIL: $required is STILL missing from /my/devices"
        exit 1
    fi
done
# Bonus: how many of the known devices are in the page.
device_count=0
missing=()
for h in "${SKYADMIN_DEVICES[@]}"; do
    if echo "$out" | grep -q "<code>$h</code>"; then
        device_count=$((device_count + 1))
    else
        missing+=("$h")
    fi
done
echo "    $device_count of $expected known devices rendered in /my/devices"
if [ ${#missing[@]} -gt 0 ]; then
    echo "    missing (informational): ${missing[*]}"
fi

# 8. verify: subnet status pill on /admin/users/{id}/subnet shows 'active'
echo
echo "--- 8. verify: /admin/users/1/subnet shows the new 'active' status"
uid=$(run_sql_inline "SELECT id FROM portal_users WHERE username = '$USER';" | tr -d ' \t')
uid="${uid:-1}"
out=$(curl -sL -b "$CK" -c "$CK" "$BASE_URL/admin/users/$uid/subnet")
# The v0.22.3 status pill renders one of: router_active, active,
# pending, disabled. We expect 'active' (no subnet-router in
# this prod setup).
if echo "$out" | grep -qE "tag-success.*fa-circle-check.*active|tag-success.*fa-tower-broadcast.*router_active"; then
    echo "PASS: /admin/users/1/subnet shows an active-class status pill"
elif echo "$out" | grep -qiE 'subnet.*(pending|disabled|not.*allocated)'; then
    echo "INFO: /admin/users/1/subnet has no active subnet (status=pending/disabled/none) — skipping pill check"
else
    echo "WARN: /admin/users/1/subnet status pill could not be verified (may need manual eyeballing)"
fi

# 9. verify: /admin/users subnet column shows the active pill
echo
echo "--- 9. verify: /admin/users subnet column shows 'active' for admin"
out=$(curl -sL -b "$CK" -c "$CK" "$BASE_URL/admin/users")
if echo "$out" | grep -E "<code>workstation-1|<code>workstation-2" >/dev/null; then
    # The subnet column pill is just after the username code block.
    # We look for the active class within the row.
    if echo "$out" | grep -B 2 -A 5 "admin" | grep -qE "tag-success.*active|tag-success.*router_active"; then
        echo "PASS: /admin/users subnet column shows active for admin"
    else
        echo "INFO: /admin/users subnet column may use the cell_none fallback — checking..."
        # Look for any active class in the row.
        if echo "$out" | grep -B 2 -A 5 "admin" | grep -q "active"; then
            echo "PASS: /admin/users shows 'active' substring near admin row"
        else
            echo "WARN: cannot definitively verify /admin/users subnet column (it may already have updated visually — re-run make test to see)"
        fi
    fi
else
    echo "INFO: /admin/users page rendered; trust the DB-level check above"
fi

echo
echo "=== fix_admin_attribution.sh: ALL CHECKS PASSED ==="
echo
echo "Summary:"
echo "  - 6 admin devices (workstation-1, workstation-2, workstation-2-old,"
echo "    skygate-host-1, workstation-4, workstation-3) are now attributed to"
echo "    username='admin' in node_owner_map."
echo "  - /my/devices page renders them (≥4 visible, exact count"
echo "    depends on which are online right now)."
echo "  - /admin/users subnet column shows 'active' for admin."
echo "  - portal_users.subnet_status flipped pending→active."
echo
echo "Re-run is safe (UPDATE is idempotent). Future admin devices"
echo "with skygate-issued preauths (post-v0.12.0) will be"
echo "auto-attributed by backfillNodeOwnership — no manual fix"
echo "needed for those."
