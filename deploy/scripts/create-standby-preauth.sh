#!/usr/bin/env bash
# ============================================================================
# create-standby-preauth.sh — create a Tailscale preauth key for a NEW standby
# B-new (v1.5.2+, 2026-09-09) — closes the B175 Strategy E gap.
#
# Background
# ----------
# Pre-B-new: when operator bootstrapped a new HA standby (e.g. svyatoslava-1),
# they ran `headscale preauthkeys create` by hand with NO --user flag.
# Headscale's default fallback put the new node in the synthetic
# `tagged-devices` user, not the real `infra` or `skyadmin` user.
# Net effect:
#   1. The new node's tag (e.g. `tag:dev-skyadmin-skyworker`) is set
#      manually AFTER auth (via `headscale nodes tag`), but...
#   2. ...the node_owner_map (skygate DB) doesn't know about it because
#      the user mapping is wrong.
#   3. `GetPerUserDeviceTags` JOIN with `portal_users` excludes the
#      synthetic `tagged-devices` user, so the per-DEVICE grants in
#      the headscale policy don't include the new node.
#   4. Result: the new standby can ping exit nodes (emilia/karolina/
#      sharlotta) via the catch-all `* → tag:dev-infra-<exit>` grants,
#      but it CANNOT reach the primary skygate-host-1-1 via Tailscale
#      (no per-DEVICE grant for `tag:dev-skyadmin-skyworker ↔
#      tag:dev-infra-skygate-host-1-1` exists).
#
# Fix
# ---
# This script creates the preauth key with the correct user mapping,
# so the new node is born in the right headscale user from the start.
# Default: `infra` (user id 85) — because HA standbys run etcd + Patroni
# (infra services). Override with `--user skyadmin` if the standby is
# a general-purpose user device, not infra.
#
# The script returns the new key on stdout (caller captures it).
# Run this on the PRIMARY (skygate) host, not on the standby.
#
# Usage
# -----
#   bash deploy/scripts/create-standby-preauth.sh --hostname svyatoslava-2
#   # → outputs: hskey-auth-XXXXXXXX (capture this!)
#
#   # Then on the new standby VM:
#   export SKYGATE_STANDBY_TS_AUTHKEY="hskey-auth-XXXXXXXX"
#   bash scripts/bootstrap_standby.sh --hostname svyatoslava-2
#
# Options
# -------
#   --hostname <name>    REQUIRED — Tailscale hostname for the new node
#                         (must be unique in the headscale tailnet)
#   --user <id|name>      user id (numeric, e.g. 85) or name (e.g. "infra")
#                         DEFAULT: 85 (infra)
#   --expiration <dur>    key TTL: 1h, 24h, 720h, etc. DEFAULT: 24h
#   --reusable            single-use vs reusable. DEFAULT: single-use
#                         (operator runs this ONCE per new VM, the
#                         standby consumes the key during `tailscale up`)
#   --acls <list>         ACL tags to attach on registration.
#                         DEFAULT: "tag:dev-infra-<hostname>:auto"
#                         (auto-tag by the headscale auth flow so the
#                         new node gets `tag:dev-infra-svyatoslava-2`
#                         on first contact — see B175 Strategy E)
#   --dry-run             print the command without executing
#
# Examples
# --------
#   # HA standby (runs etcd/Patroni):
#   bash deploy/scripts/create-standby-preauth.sh --hostname svyatoslava-2
#
#   # General-purpose user device:
#   bash deploy/scripts/create-standby-preauth.sh --hostname some-laptop \
#       --user skyadmin
#
# Exit codes
# ----------
#   0  key created + printed on stdout
#   1  preflight failed (docker / headscale not running, .env missing, etc.)
#   2  args parse error
#   3  headscale CLI failed
# ============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# --- args ---
HOSTNAME=""
USER_ID="85"  # default: infra
EXPIRATION="24h"
REUSABLE=""
ACLS=""
DRY_RUN=0
while [ $# -gt 0 ]; do
    case "$1" in
        --hostname) HOSTNAME="$2"; shift 2;;
        --user) USER_ID="$2"; shift 2;;
        --expiration) EXPIRATION="$2"; shift 2;;
        --reusable) REUSABLE="--reusable"; shift;;
        --acls) ACLS="$2"; shift 2;;
        --dry-run) DRY_RUN=1; shift;;
        --help|-h)
            sed -n '2,80p' "$0" | sed 's/^# \{0,1\}//'
            exit 0;;
        *) echo "ERROR: unknown flag: $1" >&2; exit 2;;
    esac
done

[ -n "$HOSTNAME" ] || { echo "ERROR: --hostname is required" >&2; exit 2; }

# --- preflight: headscale container running + .env present ---
if ! command -v docker >/dev/null 2>&1; then
    echo "ERROR: docker not found — run this on the skygate primary host" >&2
    exit 1
fi
if ! sudo -n docker ps --filter name=headscale --format '{{.Names}}' 2>/dev/null | grep -q '^headscale$'; then
    echo "ERROR: headscale container not running" >&2
    exit 1
fi

# --- resolve user: numeric id stays as-is, name → id via headscale users list ---
if [[ "$USER_ID" =~ ^[0-9]+$ ]]; then
    : # already numeric
else
    RESOLVED_ID=$(sudo -n docker exec headscale headscale users list -o json 2>/dev/null \
        | python3 -c "import json,sys
for u in json.load(sys.stdin):
    if u.get(chr(0x6e)+chr(0x61)+chr(0x6d)+chr(0x65)) == sys.argv[1]:
        print(u.get(chr(0x69)+chr(0x64))); break" "$USER_ID" 2>/dev/null || true)
    if [ -z "$RESOLVED_ID" ]; then
        echo "ERROR: headscale user '$USER_ID' not found (use a numeric id, e.g. 85 for infra)" >&2
        exit 1
    fi
    USER_ID="$RESOLVED_ID"
fi

# --- build the command ---
CMD="headscale preauthkeys create --user $USER_ID --expiration $EXPIRATION"
[ -n "$REUSABLE" ] && CMD="$CMD $REUSABLE"
# The auto-tag must be set at auth time so headscale applies it
# to the new node on first contact (B175 Strategy E).
if [ -z "$ACLS" ]; then
    ACLS="tag:dev-infra-$HOSTNAME"
fi
CMD="$CMD --tags $ACLS"

if [ "$DRY_RUN" = "1" ]; then
    echo "[dry-run] would run: docker exec headscale $CMD" >&2
    echo "[dry-run] would output: hskey-auth-XXXXX" >&2
    exit 0
fi

# --- run the command, parse the key from the noisy output ---
KEY=$(sudo -n docker exec headscale $CMD 2>&1 | tr -d '\r' | grep -E '^hskey-' | head -1)

if [ -z "$KEY" ] || [[ "$KEY" != hskey-* ]]; then
    echo "ERROR: failed to parse preauth key from headscale output" >&2
    echo "command was: docker exec headscale $CMD" >&2
    exit 3
fi

# --- record in skygate audit (best-effort) ---
if [ -f "$PROJECT_DIR/.env" ]; then
    SKYADMIN_DB_DSN=$(grep -E '^SKYGATE_DB_DSN=' "$PROJECT_DIR/.env" 2>/dev/null | head -1 | cut -d= -f2- | tr -d "[:space:]'\"")
    if [ -n "$SKYADMIN_DB_DSN" ] && command -v psql >/dev/null 2>&1; then
        PGPASSWORD=$(echo "$SKYADMIN_DB_DSN" | sed -E 's|.*://[^:]+:([^@]+)@.*|\1|') \
        psql "$SKYADMIN_DB_DSN" -tA -c "
            INSERT INTO audit_log (action, detail, created_at)
            VALUES (
                'ha.preauth.create',
                jsonb_build_object(
                    'hostname', '$HOSTNAME',
                    'headscale_user_id', $USER_ID,
                    'expiration', '$EXPIRATION',
                    'tags', '$ACLS',
                    'created_at', strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
                )::text,
                strftime('%s', 'now')
            );" >/dev/null 2>&1 || true
    fi
fi

echo "$KEY"
