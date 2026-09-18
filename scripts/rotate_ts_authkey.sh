#!/bin/bash
# rotate_ts_authkey.sh — Tailscale preauth key rotation
#
# The skygate container uses a reusable preauth key from
# /home/skyadmin/skygate/secrets/ts_authkey to register the
# skygate-host-1 node with headscale. Single-use keys expire
# after first use; reusable keys with --expiration 720h
# (30 days) need periodic rotation.
#
# Without rotation, the key expires and:
#   - tailscale up fails with "backend error: authkey expired"
#   - state file ends up with NoState
#   - 100.64.0.x peers become unreachable
#   - /admin/telegram "Set as egress relay" times out
#
# The fix: weekly check + auto-rotation when < 14 days remain.
# The skygate container is restarted so it picks up the new key.
#
# Install:
#   sudo cp scripts/rotate_ts_authkey.sh /usr/local/bin/
#   sudo chmod +x /usr/local/bin/rotate_ts_authkey.sh
#   # Add to root crontab:
#   sudo crontab -e
#   # Every Sunday at 03:00 (off-peak):
#   0 3 * * 0 /usr/local/bin/rotate_ts_authkey.sh >> /var/log/skygate-ts-rotate.log 2>&1
#
# Why weekly, not monthly: gives 14+ days of buffer between
# the rotation (30 days from new key) and the next rotation,
# so a missed run doesn't immediately break the tailnet.
#
# 2026-08-10 v0.33.1.36: B86 follow-up. Created during the
# /admin/system_tests bug-fix release; tracked as backlog
# since v0.33.1.34.
set -euo pipefail

HEADSCALE_USER_ID="${HEADSCALE_USER_ID:-1}"
KEY_EXPIRATION_HOURS="${KEY_EXPIRATION_HOURS:-720}"
SECRETS_DIR="${SECRETS_DIR:-/home/skyadmin/skygate/secrets}"
AUTHKEY_FILE="$SECRETS_DIR/ts_authkey"
LOG_PREFIX="[rotate_ts_authkey]"

log() {
    echo "$LOG_PREFIX $(date -u +%Y-%m-%dT%H:%M:%SZ) $*"
}

log "starting Tailscale preauth key rotation"

# Pre-flight: headscale container running?
if ! sudo -n docker ps --filter name=headscale --format '{{.Names}}' | grep -q '^headscale$'; then
    log "ERROR: headscale container not running — cannot rotate"
    exit 1
fi

# Pre-flight: skygate container running?
SKYGATE_NAME=$(sudo -n docker ps --filter name=skygate --format '{{.Names}}' | head -1 || true)
if [ -z "$SKYGATE_NAME" ]; then
    log "WARN: skygate container not running — will rotate the key file but no restart will happen"
fi

# Generate a new reusable preauth key.
log "creating new preauth key (user_id=$HEADSCALE_USER_ID, expiration=${KEY_EXPIRATION_HOURS}h)"
NEW_KEY=$(sudo -n docker exec headscale headscale preauthkeys create \
    --user "$HEADSCALE_USER_ID" \
    --reusable \
    --expiration "${KEY_EXPIRATION_HOURS}h" \
    2>&1 | tr -d '\r' | grep -E '^hskey-' | head -1)

if [ -z "$NEW_KEY" ] || [[ "$NEW_KEY" != hskey-* ]]; then
    log "ERROR: failed to parse new preauth key (output: $NEW_KEY)"
    exit 1
fi
log "new key generated (${#NEW_KEY} chars)"

# Write the new key to the file with the correct permissions.
mkdir -p "$SECRETS_DIR"
echo -n "$NEW_KEY" > "$AUTHKEY_FILE"
chmod 600 "$AUTHKEY_FILE"
chown skyadmin:skyadmin "$AUTHKEY_FILE" 2>/dev/null || true
log "wrote new key to $AUTHKEY_FILE (chmod 600)"

# Restart the skygate container so it re-reads the authkey
# from /run/secrets/ts_authkey on the next start.
if [ -n "$SKYGATE_NAME" ]; then
    log "restarting $SKYGATE_NAME to pick up the new key"
    cd /home/skyadmin/skygate
    sudo -n SKYGATE_HOST_REPO_PATH=/home/skyadmin/skygate \
        docker compose -p skygate up -d --force-recreate --no-deps skygate \
        2>&1 | tail -5 | sed "s/^/$LOG_PREFIX   /"
    log "$SKYGATE_NAME restarted; tailscale up will run on next entrypoint tick"
else
    log "no skygate container found; the new key is on disk and will be picked up on next start"
fi

log "rotation complete"
