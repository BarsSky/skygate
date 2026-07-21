#!/usr/bin/env bash
# ============================================================================
# headscale-deprovision.sh — tear down a per-user headscale container
# 2026-07-21: v0.23.0 Phase 1
#
# Usage:
#   headscale-deprovision.sh <username>
#
# Stops and removes:
#   - the headscale-<username> container
#   - the docker-compose.user-<username>.yml override file
#
# PRESERVES:
#   - /home/skyadmin/headscale/users/<username>/ (config + data)
#     so the operator can manually recover / migrate / inspect
#     after teardown. The directory is moved to a sibling
#     `.decommissioned-<timestamp>` so a re-bootstrap doesn't
#     clobber it.
#
# Does NOT touch:
#   - portal_users.headscale_url / headscale_api_key_enc — the
#     caller is responsible for clearing those via
#     db.ClearUserHeadscaleConfig or the /admin/users/{id}/plane
#     "Clear" button AFTER the deprovision succeeds. Doing it
#     the other way around (clear DB first, then container)
#     would leave skygate's per-user client pointing at a dead
#     container until the next skygate restart invalidates the
#     cache.
# ============================================================================
set -euo pipefail

USERNAME="${1:-}"

if [ -z "$USERNAME" ]; then
    echo "usage: headscale-deprovision.sh <username>" >&2
    exit 2
fi

GLOBAL_HEADSCALE_DIR="${SKYGATE_HEADSCALE_DIR:-/home/skyadmin/headscale}"
USER_DIR="$GLOBAL_HEADSCALE_DIR/users/$USERNAME"
COMPOSE_OVERRIDE="$GLOBAL_HEADSCALE_DIR/docker-compose.user-$USERNAME.yml"
CONTAINER_NAME="headscale-$USERNAME"

# Stop + remove the container. `docker compose ... down` is the
# safe path — it sends SIGTERM, waits for graceful shutdown, then
# removes the container. We pass the override file as -f so
# `docker compose` knows about the service (without it, `down`
# would error with "no such service" if the global compose doesn't
# declare it).
if [ -f "$COMPOSE_OVERRIDE" ]; then
    cd "$GLOBAL_HEADSCALE_DIR"
    docker compose -f docker-compose.yml -f "$COMPOSE_OVERRIDE" down "$CONTAINER_NAME" 2>/dev/null || \
        docker compose -f "$COMPOSE_OVERRIDE" down "$CONTAINER_NAME" 2>/dev/null || \
        docker rm -f "$CONTAINER_NAME" 2>/dev/null || true
    rm -f "$COMPOSE_OVERRIDE"
fi

# Belt-and-suspenders: if the container is still running (e.g. the
# compose file was missing), force-remove it.
if docker ps -a --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
fi

# Move the per-user directory aside instead of removing it. The
# operator can manually inspect / back up / re-bootstrap from the
# preserved copy.
if [ -d "$USER_DIR" ]; then
    TIMESTAMP=$(date +%s)
    PARENT=$(dirname "$USER_DIR")
    mv "$USER_DIR" "$PARENT/.decommissioned-$USERNAME-$TIMESTAMP"
    echo "preserved: $PARENT/.decommissioned-$USERNAME-$TIMESTAMP"
fi

echo "OK: deprovisioned $USERNAME (container + override removed, data preserved)"
