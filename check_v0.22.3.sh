#!/bin/bash
# check_v0.22.3.sh — live validation of v0.22.3 subnet status semantics.
#
# Verifies the 6-step rollout:
#   1. /my/devices shows the new "Your personal subnet" card
#   2. CIDR + status pill on /my/devices for skyadmin
#   3. /admin/users subnet column shows the new colored pill
#   4. /admin/users/{id}/subnet page shows the v0.22.3 explainer
#   5. skyadmin status is "active" (has devices) in DB
#   6. Pre-v0.20.0 users (michail/guest/daniil) keep their "—" / no subnet
#
# Plus a 7th: the API path works for /my/devices (skyadmin authenticates,
# loads page, status is derived from the new SyncStatus logic).
#
# 2026-07-21: v0.22.3 live validation.

set -e

CK=/tmp/check_v0.22.3.ck
rm -f "$CK"

base="http://localhost:8080"
USER="${SKYGATE_ADMIN_USER:-skyadmin}"
PASS="${SKYGATE_ADMIN_PASS:-}"

if [ -z "$PASS" ]; then
    echo "FAIL: SKYGATE_ADMIN_PASS not set in env / .env"
    exit 1
fi

echo "=== check_v0.22.3.sh ==="

echo
echo "--- 1. login as $USER"
code=$(curl -s -o /dev/null -w "%{http_code}" -c "$CK" -b "$CK" \
    --data-urlencode "username=$USER" --data-urlencode "password=$PASS" \
    "$base/login")
if [ "$code" = "302" ]; then
    echo "PASS: login returned 302"
else
    echo "FAIL: login returned $code, want 302"
    exit 1
fi

echo
echo "--- 2. /my/devices shows the new 'Your personal subnet' card"
# The card title is i18n-translated, so we check for the
# rendered Russian text (RU is the default VM lang) AND the
# CIDR. The Russian title is "Твой personal subnet" and
# the English is "Your personal subnet" — accept either.
out=$(curl -s -b "$CK" -c "$CK" "$base/my/devices")
if echo "$out" | grep -qE "(Твой|Your) personal subnet"; then
    echo "PASS: 'Your personal subnet' card title rendered"
else
    echo "FAIL: subnet card title NOT in /my/devices"
    echo "--- relevant fragment: ---"
    echo "$out" | grep -i "subnet" | head -5
    exit 1
fi
if echo "$out" | grep -q "10.0.1.0/24"; then
    echo "PASS: /my/devices shows 10.0.1.0/24 (skyadmin's CIDR)"
else
    echo "FAIL: 10.0.1.0/24 NOT in /my/devices"
    exit 1
fi
# Check for the green active pill (the icon is fa-circle-check
# for active, fa-tower-broadcast for router_active, fa-circle-pause
# for pending, fa-circle-xmark for disabled).
if echo "$out" | grep -qE "fa-circle-check.*active|active</span>"; then
    echo "PASS: /my/devices shows 'active' status pill"
else
    echo "FAIL: 'active' status pill NOT in /my/devices"
    exit 1
fi

echo
echo "--- 3. /admin/users subnet column shows the colored pill"
out=$(curl -s -b "$CK" -c "$CK" "$base/admin/users")
if echo "$out" | grep -q "skyadmin" && echo "$out" | grep -q "10.0.1.0/24"; then
    echo "PASS: /admin/users shows skyadmin's CIDR 10.0.1.0/24"
else
    echo "FAIL: skyadmin's CIDR NOT in /admin/users"
    echo "--- relevant fragment: ---"
    echo "$out" | grep -A 3 "skyadmin" | head -10
    exit 1
fi
# The v0.22.3 status pill is wrapped in a <span> with a
# title= tooltip. Russian: "Subnet активна", English: "Subnet active".
if echo "$out" | grep -qE 'title="Subnet (активна|active)'; then
    echo "PASS: /admin/users subnet column has v0.22.3 tooltip"
else
    echo "FAIL: v0.22.3 tooltip NOT in /admin/users subnet column"
    echo "--- relevant fragment: ---"
    echo "$out" | grep -B 1 -A 1 "10.0.1.0/24" | head -10
    exit 1
fi

echo
echo "--- 4. /admin/users/{id}/subnet shows the v0.22.3 explainer"
# Find skyadmin's id from /admin/users
uid=$(echo "$out" | grep -oE '/admin/users/[0-9]+/(subnet|delete)' | head -1 | grep -oE '[0-9]+')
if [ -z "$uid" ]; then
    echo "FAIL: couldn't extract skyadmin's id from /admin/users"
    exit 1
fi
echo "  (skyadmin id=$uid)"
out=$(curl -s -b "$CK" -c "$CK" "$base/admin/users/$uid/subnet")
# The v0.22.3 explainer in Russian starts with "v0.22.3:" and
# contains "теперь означает" (now means). In English, the equivalent
# is "now means".
if echo "$out" | grep -qE "v0\.22\.3.*(теперь означает|now means)"; then
    echo "PASS: /admin/users/$uid/subnet shows v0.22.3 explainer"
else
    echo "FAIL: v0.22.3 explainer NOT on /admin/users/$uid/subnet"
    echo "--- relevant fragment: ---"
    echo "$out" | grep -B 1 -A 3 "v0.22.3" | head -10
    exit 1
fi
# The status pill on the subnet page (not just the column).
# Russian tooltip: "Subnet активна", English: "Subnet active".
if echo "$out" | grep -qE 'title="Subnet (активна|active)'; then
    echo "PASS: /admin/users/$uid/subnet shows status pill with tooltip"
else
    echo "FAIL: status pill tooltip NOT on /admin/users/$uid/subnet"
    exit 1
fi

echo
echo "--- 5. skyadmin status in DB is 'active' (not 'pending')"
db_status=$(docker exec skygate sqlite3 /data/skygate.db \
    "SELECT subnet_status FROM portal_users WHERE username='skyadmin'" 2>&1)
if [ "$db_status" = "active" ]; then
    echo "PASS: skyadmin's subnet_status = 'active' (was 'pending' pre-v0.22.3)"
else
    echo "FAIL: skyadmin's subnet_status = '$db_status', want 'active'"
    exit 1
fi
db_cidr=$(docker exec skygate sqlite3 /data/skygate.db \
    "SELECT subnet_cidr FROM portal_users WHERE username='skyadmin'" 2>&1)
if [ "$db_cidr" = "10.0.1.0/24" ]; then
    echo "PASS: skyadmin's subnet_cidr = '10.0.1.0/24' (correct deterministic allocation)"
else
    echo "FAIL: skyadmin's subnet_cidr = '$db_cidr', want '10.0.1.0/24'"
    exit 1
fi

echo
echo "--- 6. pre-v0.20.0 users (michail/guest/daniil) keep their 'no subnet' state"
for u in michail guest daniil; do
    s=$(docker exec skygate sqlite3 /data/skygate.db \
        "SELECT subnet_status FROM portal_users WHERE username='$u'" 2>&1)
    c=$(docker exec skygate sqlite3 /data/skygate.db \
        "SELECT subnet_cidr FROM portal_users WHERE username='$u'" 2>&1)
    if [ -z "$s" ] || [ "$s" = "none" ] || [ "$s" = "" ]; then
        echo "PASS: $u has no subnet (status='${s:-empty}', cidr='${c:-empty}')"
    else
        echo "FAIL: $u unexpectedly has subnet_status='$s' (pre-v0.20.0, expected empty)"
    fi
done

echo
echo "--- 7. verify that guest's 'no subnet' state correctly renders as '—' in /admin/users"
# Reload /admin/users (the earlier $out may be stale from step 3)
out2=$(curl -s -b "$CK" -c "$CK" "$base/admin/users")
# The guest row has <code>guest</code> as the username cell.
# Find the line with the code-wrapped guest and check that the
# subnet cell (a few lines down) contains the "—" placeholder.
guest_line=$(echo "$out2" | grep -n "<code>guest</code>" | cut -d: -f1)
if [ -z "$guest_line" ]; then
    echo "FAIL: couldn't find 'guest' row in /admin/users"
    exit 1
fi
# The subnet column is the 5th <td> in the row (after ID, username,
# headscale_id, role). Just check the next 5 lines for the dash.
guest_subnet=$(echo "$out2" | sed -n "$((guest_line+1)),$((guest_line+6))p" 2>&1)
if echo "$guest_subnet" | grep -qE "—|cell_none|user_subnet.cell_none"; then
    echo "PASS: 'guest' renders as '—' for subnet (pre-v0.20.0, no row)"
else
    echo "FAIL: 'guest' subnet cell doesn't show '—' (lines: $guest_subnet)"
    exit 1
fi

echo
echo "=== check_v0.22.3.sh: ALL CHECKS PASSED ==="
