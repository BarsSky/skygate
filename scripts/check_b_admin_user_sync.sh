#!/usr/bin/env bash
# check_b_admin_user_sync.sh — verifies skygate admin user is in sync with headscale.
#
# 2026-09-12 (Issue follow-up): the operator reported confusion about
# SKYGATE_ADMIN_USER vs headscale user naming. The current bootstrap flow
# creates the admin user in both skygate DB and headscale at first start,
# but is idempotent — if the operator changes SKYGATE_ADMIN_USER in .env
# after first install, NOTHING updates. This check detects the drift.
#
# Pins 5 contracts:
#
#   A. portal_users has exactly ONE user with is_admin=1
#      (the canonical admin). FAIL if 0 or >1.
#
#   B. The admin's username == SKYGATE_ADMIN_USER (from skygate
#      container env, not host .env — the env may be patched).
#      FAIL if they differ.
#
#   C. The admin's headscale_user_id is NOT NULL.
#      FAIL if NULL (admin exists in skygate but missing in headscale).
#
#   D. headscale has a user with the same id as portal_users.headscale_user_id
#      (i.e., the admin actually exists on headscale side).
#      FAIL if headscale doesn't have it.
#
#   E. headscale has a user with the same name as the admin (in case
#      the admin was renamed externally — drift detector).
#      WARN if names mismatch.
#
# Usage:
#   bash scripts/check_b_admin_user_sync.sh                  # active backend
#   bash scripts/check_b_admin_user_sync.sh --strict         # FAIL on WARN
#
# Exit codes:
#   0 = all 5 contracts pass
#   1 = one or more contracts FAIL (admin drift detected)
#   2 = backend unknown

set -uo pipefail

STRICT=0
for arg in "$@"; do
    case "$arg" in
        --strict) STRICT=1 ;;
    esac
done

CONTAINER="${SKYGATE_CONTAINER:-skygate-skygate-1}"
PG_CONTAINER="${SKYGATE_PG_CONTAINER:-skygate-pg-local}"
VOLUME="${SKYGATE_DATA_VOLUME:-skygate-data}"

ok()  { echo "  PASS  $1"; }
warn(){ echo "  WARN  $1"; }
bad() { echo "  FAIL  $1"; exit 1; }

# ── pre-flight (2026-09-18, P1) ──
# Contract A drives the LIVE docker stack, so on a workstation without a
# reachable daemon this used to FAIL with "skygate-skygate-1 container not
# running" — an environment answer to a live-state question, which painted the
# whole gate red and buried real findings. Skip only when docker itself is
# unreachable; if docker works but the container is missing, that IS a real
# finding and the check fails below as before.
if ! sudo docker info >/dev/null 2>&1; then
    echo "SKIP: docker daemon not reachable (B-mod-admin-user-sync inspects the live stack) — run it on the skygate VM"
    exit 0
fi

# ── container reachable ──
if ! sudo docker inspect "$CONTAINER" >/dev/null 2>&1; then
    bad "$CONTAINER container not running"
fi
state=$(sudo docker inspect "$CONTAINER" --format '{{.State.Status}}')
[ "$state" = "running" ] || bad "$CONTAINER state=$state"

# ── detect backend ──
LIVE_DB=$(sudo docker exec "$CONTAINER" sh -c 'env | grep "^SKYGATE_DB=" | head -1 | cut -d= -f2-')
EXPECTED_ADMIN=$(sudo docker exec "$CONTAINER" sh -c 'env | grep "^SKYGATE_ADMIN_USER=" | head -1 | cut -d= -f2-')
[ -n "$EXPECTED_ADMIN" ] || bad "SKYGATE_ADMIN_USER empty in container env"

case "$LIVE_DB" in
    postgres://*|postgresql://*) BACKEND="pg" ;;
    sqlite:*|file:*|"")         BACKEND="sqlite" ;;
    *)                          BACKEND="unknown" ;;
esac
[ "$BACKEND" != "unknown" ] || bad "backend unknown (SKYGATE_DB=$LIVE_DB)"
echo "backend=$BACKEND  expected_admin=$EXPECTED_ADMIN"

# ── query admin row ──
case "$BACKEND" in
    pg)
        if ! sudo docker inspect "$PG_CONTAINER" >/dev/null 2>&1; then
            bad "$PG_CONTAINER container not running"
        fi
        # Contract A: exactly one admin
        ADMIN_ROW=$(sudo docker exec "$PG_CONTAINER" psql -U admin -d skygate_staging -At -c \
            "SELECT id || '|' || username || '|' || COALESCE(headscale_user_id::text, 'NULL') FROM portal_users WHERE is_admin=1 ORDER BY id")
        ADMIN_COUNT=$(echo "$ADMIN_ROW" | grep -c '|' || true)
        if [ "$ADMIN_COUNT" -eq 1 ]; then
            ok "A: exactly one admin in portal_users"
        else
            bad "A: $ADMIN_COUNT admins in portal_users (expected 1)"
        fi
        ADMIN_ID=$(echo "$ADMIN_ROW" | cut -d'|' -f1)
        ADMIN_NAME=$(echo "$ADMIN_ROW" | cut -d'|' -f2)
        ADMIN_HSID=$(echo "$ADMIN_ROW" | cut -d'|' -f3)
        # Contract B: admin username == SKYGATE_ADMIN_USER
        if [ "$ADMIN_NAME" = "$EXPECTED_ADMIN" ]; then
            ok "B: portal admin name ($ADMIN_NAME) == SKYGATE_ADMIN_USER"
        else
            bad "B: portal admin name=$ADMIN_NAME != SKYGATE_ADMIN_USER=$EXPECTED_ADMIN (drift)"
        fi
        # Contract C: headscale_user_id IS NOT NULL
        if [ "$ADMIN_HSID" != "NULL" ] && [ -n "$ADMIN_HSID" ]; then
            ok "C: admin has headscale_user_id=$ADMIN_HSID"
        else
            bad "C: admin's headscale_user_id is NULL (not linked)"
        fi
        # Contract D: headscale has user with this id (query headscale CLI directly)
        # headscale stores users in its own SQLite (headscale_headscale_data volume),
        # not in our PG. The headscale container has no shell, so we exec the
        # CLI directly + parse JSON with python3 on the host side.
        HS_JSON=$(sudo docker exec headscale headscale users list -i "$ADMIN_HSID" -o json 2>&1)
        HS_NAMES=$(echo "$HS_JSON" | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin)
    if isinstance(d, list):
        for u in d:
            if str(u.get('id',''))=='$ADMIN_HSID':
                print(u.get('name',''))
                break
    elif isinstance(d, dict):
        print(d.get('name',''))
except Exception as e:
    pass
" 2>&1)
        if [ -n "$HS_NAMES" ]; then
            ok "D: headscale has user with id=$ADMIN_HSID (name=$HS_NAMES)"
        else
            bad "D: headscale does NOT have user with id=$ADMIN_HSID"
        fi
        # Contract E: headscale name == portal name (drift warning)
        if [ "$HS_NAMES" = "$ADMIN_NAME" ]; then
            ok "E: headscale name ($HS_NAMES) == portal name ($ADMIN_NAME)"
        else
            if [ "$STRICT" = "1" ]; then
                bad "E: headscale name=$HS_NAMES != portal name=$ADMIN_NAME (drift; --strict)"
            else
                warn "E: headscale name=$HS_NAMES != portal name=$ADMIN_NAME (drift; rename recommended)"
            fi
        fi
        ;;
    sqlite)
        if ! sudo docker run --rm -v "$VOLUME":/data alpine sh -c \
            'apk add --no-cache sqlite >/dev/null 2>&1 && \
             sqlite3 /data/skygate.db "SELECT id, username, COALESCE(headscale_user_id, -1) FROM portal_users WHERE is_admin=1 ORDER BY id"' \
            > /tmp/_admin_sqlite_$$ 2>&1; then
            bad "sqlite query failed (see /tmp/_admin_sqlite_$$)"
        fi
        ADMIN_ROW=$(cat /tmp/_admin_sqlite_$$)
        ADMIN_COUNT=$(echo "$ADMIN_ROW" | grep -c '.' || true)
        if [ "$ADMIN_COUNT" -eq 1 ]; then
            ok "A: exactly one admin in portal_users"
        else
            bad "A: $ADMIN_COUNT admins in portal_users (expected 1)"
        fi
        ADMIN_ID=$(echo "$ADMIN_ROW" | awk 'NR==1 {print $1}')
        ADMIN_NAME=$(echo "$ADMIN_ROW" | awk 'NR==1 {print $2}')
        ADMIN_HSID=$(echo "$ADMIN_ROW" | awk 'NR==1 {print $3}')
        [ "$ADMIN_HSID" -gt 0 ] && ok "C: admin has headscale_user_id=$ADMIN_HSID" || bad "C: admin headscale_user_id missing"
        # B + D + E: same as pg
        if [ "$ADMIN_NAME" = "$EXPECTED_ADMIN" ]; then
            ok "B: portal admin name ($ADMIN_NAME) == SKYGATE_ADMIN_USER"
        else
            bad "B: portal admin name=$ADMIN_NAME != SKYGATE_ADMIN_USER=$EXPECTED_ADMIN (drift)"
        fi
        HS_JSON=$(sudo docker exec headscale headscale users list -i "$ADMIN_HSID" -o json 2>&1)
        HS_NAMES=$(echo "$HS_JSON" | python3 -c "
import json,sys
try:
    d=json.load(sys.stdin)
    if isinstance(d, list):
        for u in d:
            if str(u.get('id',''))=='$ADMIN_HSID':
                print(u.get('name',''))
                break
    elif isinstance(d, dict):
        print(d.get('name',''))
except Exception as e:
    pass
" 2>&1)
        if [ -n "$HS_NAMES" ]; then
            ok "D: headscale has user with id=$ADMIN_HSID (name=$HS_NAMES)"
        else
            bad "D: headscale does NOT have user with id=$ADMIN_HSID"
        fi
        if [ "$HS_NAMES" = "$ADMIN_NAME" ]; then
            ok "E: headscale name ($HS_NAMES) == portal name ($ADMIN_NAME)"
        else
            if [ "$STRICT" = "1" ]; then
                bad "E: headscale name=$HS_NAMES != portal name=$ADMIN_NAME (drift; --strict)"
            else
                warn "E: headscale name=$HS_NAMES != portal name=$ADMIN_NAME (drift)"
            fi
        fi
        rm -f /tmp/_admin_sqlite_$$
        ;;
esac

echo ""
echo "All 5 contracts checked. Backend=$BACKEND. Admin=$ADMIN_NAME (id=$ADMIN_ID, hs_id=$ADMIN_HSID)."
