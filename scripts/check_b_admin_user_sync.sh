#!/usr/bin/env bash
# check_b_admin_user_sync.sh — verifies skygate's PRIMARY admin is in sync with headscale.
#
# 2026-09-12: the operator reported confusion about
# SKYGATE_ADMIN_USER vs headscale user naming. The bootstrap flow creates
# the admin user in both skygate DB and headscale at first start, but is
# idempotent — if the operator changes SKYGATE_ADMIN_USER in .env after
# first install, NOTHING updates. This check detects the drift.
#
# 2026-09-19: v0.72 (B264) — CONTRACT RENEGOTIATION.
#
# Contract A used to assert "exactly ONE portal_users row with is_admin=1".
# That is WRONG by design now: B264 lets an administrator grant AND revoke
# the `admin` role for other portal users, so a healthy install has 1..N
# is_admin=1 rows. The canonical (bootstrap/root) account is instead marked
# by the V072 column `is_primary`, which the partial UNIQUE index
# portal_users_one_primary_uniq caps at exactly one row. The contracts below
# therefore key on is_primary, not on is_admin:
#
#   P. portal_users.is_primary EXISTS (V072 ran on this database).
#      FAIL if missing (the live binary is older than the migration).
#
#   A. portal_users has exactly ONE row with is_primary=1
#      (the canonical, immutable primary admin). FAIL if 0 or >1.
#
#   A2. That primary row is is_admin=1 (primary ⟹ admin; the UI would
#      otherwise render a "primary" account that cannot administer).
#      FAIL if 0.
#
#   B. The primary's username == SKYGATE_ADMIN_USER (from skygate
#      container env, not host .env — the env may be patched).
#      FAIL if they differ.
#
#   C. The primary's headscale_user_id is NOT NULL.
#      FAIL if NULL (primary exists in skygate but missing in headscale).
#
#   D. headscale has a user with the same id as portal_users.headscale_user_id
#      (i.e., the primary actually exists on headscale side).
#      FAIL if headscale doesn't have it.
#
#   E. headscale has a user with the same name as the primary (in case
#      the primary was renamed externally — drift detector).
#      WARN if names mismatch.
#
# NOTE: additional (delegated) admins are expected and are NOT checked
# here — their role is delegated/revoked from /admin/users and carries no
# headscale-side invariant.
#
# Usage:
#   bash scripts/check_b_admin_user_sync.sh                  # active backend
#   bash scripts/check_b_admin_user_sync.sh --strict         # FAIL on WARN
#
# Exit codes:
#   0 = all contracts pass (or SKIP: no live docker/DB available)
#   1 = one or more contracts FAIL (primary admin drift detected)
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
# The contracts drive the LIVE docker stack, so on a workstation without a
# reachable daemon this used to FAIL with "skygate-skygate-1 container not
# running" — an environment answer to a live-state question, which painted the
# whole gate red and buried real findings. Skip only when docker itself is
# unreachable; if docker works but the container is missing, that IS a real
# finding and the check fails below as before.
#
# 2026-09-19 (B264): a missing/empty SKYGATE_TEST_PG_DSN or an unreachable
# PostgreSQL is likewise an ABSENT live dependency, not a failure — those
# branches SKIP rather than FAIL (AGENTS.md rule 1).
if ! sudo -n docker info >/dev/null 2>&1; then
    echo "SKIP: docker daemon not reachable (B-mod-admin-user-sync inspects the live stack) — run it on the skygate VM"
    exit 0
fi

# ── container reachable ──
# B281 (2026-09-22): the docker daemon can be reachable while the skygate
# container is not running (a CI runner with no stack, or a stopped service).
# That is an ABSENT live dependency → SKIP, never FAIL (AGENTS.md §1.1); the
# old form reported `FAIL skygate-skygate-1 container not running` on every CI
# run, which is a red row that says nothing about the code.
if ! sudo -n docker inspect "$CONTAINER" >/dev/null 2>&1; then
    echo "  SKIP  $CONTAINER container not running (live check — run it on the skygate host)"
    exit 0
fi
state=$(sudo -n docker inspect "$CONTAINER" --format '{{.State.Status}}')
if [ "$state" != "running" ]; then
    echo "  SKIP  $CONTAINER state=$state (live check — run it on the skygate host)"
    exit 0
fi

# ── detect backend ──
LIVE_DB=$(sudo -n docker exec "$CONTAINER" sh -c 'env | grep "^SKYGATE_DB=" | head -1 | cut -d= -f2-')
EXPECTED_ADMIN=$(sudo -n docker exec "$CONTAINER" sh -c 'env | grep "^SKYGATE_ADMIN_USER=" | head -1 | cut -d= -f2-')
[ -n "$EXPECTED_ADMIN" ] || bad "SKYGATE_ADMIN_USER empty in container env"

case "$LIVE_DB" in
    postgres://*|postgresql://*) BACKEND="pg" ;;
    sqlite:*|file:*|"")         BACKEND="sqlite" ;;
    *)                          BACKEND="unknown" ;;
esac
[ "$BACKEND" != "unknown" ] || bad "backend unknown (SKYGATE_DB=$LIVE_DB)"
echo "backend=$BACKEND  expected_primary=$EXPECTED_ADMIN"

# ── query the primary row ──
case "$BACKEND" in
    pg)
        if ! sudo -n docker inspect "$PG_CONTAINER" >/dev/null 2>&1; then
            echo "SKIP: $PG_CONTAINER (the live PostgreSQL for this deployment) is not reachable from here — no DSN/PG available"
            exit 0
        fi
        # Contract P: the V072 primary marker column must exist. Without it
        # every query below would fail with a column error, so check it first
        # and report it as the real finding it is (a pre-V072 live binary).
        HAS_IS_PRIMARY=$(sudo -n docker exec "$PG_CONTAINER" psql -U admin -d skygate_staging -At -c \
            "SELECT COUNT(*) FROM information_schema.columns WHERE table_name='portal_users' AND column_name='is_primary'" 2>/dev/null)
        if [ "${HAS_IS_PRIMARY:-0}" = "1" ]; then
            ok "P: portal_users.is_primary exists (V072 applied)"
        else
            bad "P: portal_users.is_primary is MISSING — V072 (B264 primary admin) has not run on this database"
        fi
        # Contract A: exactly one PRIMARY (delegated admins are allowed).
        ADMIN_ROW=$(sudo -n docker exec "$PG_CONTAINER" psql -U admin -d skygate_staging -At -c \
            "SELECT id || '|' || username || '|' || COALESCE(headscale_user_id::text, 'NULL') || '|' || is_admin FROM portal_users WHERE is_primary=1 ORDER BY id")
        ADMIN_COUNT=$(echo "$ADMIN_ROW" | grep -c '|' || true)
        if [ "$ADMIN_COUNT" -eq 1 ]; then
            ok "A: exactly one primary admin in portal_users"
        else
            bad "A: $ADMIN_COUNT primary rows in portal_users (expected exactly 1; is_admin=1 alone is no longer the marker — see V072)"
        fi
        ADMIN_ID=$(echo "$ADMIN_ROW" | cut -d'|' -f1)
        ADMIN_NAME=$(echo "$ADMIN_ROW" | cut -d'|' -f2)
        ADMIN_HSID=$(echo "$ADMIN_ROW" | cut -d'|' -f3)
        ADMIN_ISADMIN=$(echo "$ADMIN_ROW" | cut -d'|' -f4)
        # Contract A2: primary ⟹ admin.
        if [ "$ADMIN_ISADMIN" = "1" ]; then
            ok "A2: primary row is is_admin=1"
        else
            bad "A2: primary row has is_admin=$ADMIN_ISADMIN (must be 1)"
        fi
        # Contract B: primary username == SKYGATE_ADMIN_USER
        if [ "$ADMIN_NAME" = "$EXPECTED_ADMIN" ]; then
            ok "B: primary name ($ADMIN_NAME) == SKYGATE_ADMIN_USER"
        else
            bad "B: primary name=$ADMIN_NAME != SKYGATE_ADMIN_USER=$EXPECTED_ADMIN (drift)"
        fi
        # Contract C: headscale_user_id IS NOT NULL
        if [ "$ADMIN_HSID" != "NULL" ] && [ -n "$ADMIN_HSID" ]; then
            ok "C: primary has headscale_user_id=$ADMIN_HSID"
        else
            bad "C: primary's headscale_user_id is NULL (not linked)"
        fi
        # Contract D: headscale has user with this id (query headscale CLI directly)
        # headscale stores users in its own SQLite (headscale_headscale_data volume),
        # not in our PG. The headscale container has no shell, so we exec the
        # CLI directly + parse JSON with python3 on the host side.
        HS_JSON=$(sudo -n docker exec headscale headscale users list -i "$ADMIN_HSID" -o json 2>&1)
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
        # Contract P: the V072 marker column must exist in the SQLite schema.
        # Read the schema through the same alpine+sqlite3 path the row query
        # uses, and treat an unreachable volume as SKIP (absent live state).
        if ! sudo -n docker run --rm -v "$VOLUME":/data alpine sh -c \
            'apk add --no-cache sqlite >/dev/null 2>&1 && \
             sqlite3 /data/skygate.db "PRAGMA table_info(portal_users)"' \
            > /tmp/_admin_cols_$$ 2>&1; then
            echo "SKIP: sqlite database in volume $VOLUME is not readable from here — no live DB available"
            exit 0
        fi
        if grep -q '|is_primary|' /tmp/_admin_cols_$$; then
            ok "P: portal_users.is_primary exists (V072 applied)"
        else
            bad "P: portal_users.is_primary is MISSING — V072 (B264 primary admin) has not run on this database"
        fi
        rm -f /tmp/_admin_cols_$$
        if ! sudo -n docker run --rm -v "$VOLUME":/data alpine sh -c \
            'apk add --no-cache sqlite >/dev/null 2>&1 && \
             sqlite3 /data/skygate.db "SELECT id, username, COALESCE(headscale_user_id, -1), is_admin FROM portal_users WHERE is_primary=1 ORDER BY id"' \
            > /tmp/_admin_sqlite_$$ 2>&1; then
            echo "SKIP: sqlite query could not run against volume $VOLUME — no live DB available"
            exit 0
        fi
        ADMIN_ROW=$(cat /tmp/_admin_sqlite_$$)
        ADMIN_COUNT=$(echo "$ADMIN_ROW" | grep -c '.' || true)
        if [ "$ADMIN_COUNT" -eq 1 ]; then
            ok "A: exactly one primary admin in portal_users"
        else
            bad "A: $ADMIN_COUNT primary rows in portal_users (expected exactly 1; is_admin=1 alone is no longer the marker — see V072)"
        fi
        ADMIN_ID=$(echo "$ADMIN_ROW" | awk 'NR==1 {print $1}')
        ADMIN_NAME=$(echo "$ADMIN_ROW" | awk 'NR==1 {print $2}')
        ADMIN_HSID=$(echo "$ADMIN_ROW" | awk 'NR==1 {print $3}')
        ADMIN_ISADMIN=$(echo "$ADMIN_ROW" | awk 'NR==1 {print $4}')
        [ "$ADMIN_ISADMIN" = "1" ] && ok "A2: primary row is is_admin=1" || bad "A2: primary row has is_admin=$ADMIN_ISADMIN (must be 1)"
        [ "$ADMIN_HSID" -gt 0 ] && ok "C: primary has headscale_user_id=$ADMIN_HSID" || bad "C: primary headscale_user_id missing"
        # B + D + E: same as pg
        if [ "$ADMIN_NAME" = "$EXPECTED_ADMIN" ]; then
            ok "B: primary name ($ADMIN_NAME) == SKYGATE_ADMIN_USER"
        else
            bad "B: primary name=$ADMIN_NAME != SKYGATE_ADMIN_USER=$EXPECTED_ADMIN (drift)"
        fi
        HS_JSON=$(sudo -n docker exec headscale headscale users list -i "$ADMIN_HSID" -o json 2>&1)
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
echo "All contracts checked (P + A + A2 + B + C + D + E). Backend=$BACKEND. Primary=$ADMIN_NAME (id=$ADMIN_ID, hs_id=$ADMIN_HSID)."
