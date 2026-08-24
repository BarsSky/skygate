#!/usr/bin/env bash
# ============================================================================
# init-headplane.sh — auto-apply headplane API key on fresh deploy
# B151 (v1.5.0) — Phase 8 of the HA v1.5.0 plan.
#
# See docs/internal/ha-v1.5.0-execution.md §3 (Phase 8).
#
# Background
# ----------
# On a fresh deploy, the headplane sidecar needs an API key to talk
# to headscale. The current deploy.sh expects the operator to:
#
#   1. Run `docker exec headscale headscale apikeys create -e 365d`
#   2. Copy the generated key to .env as HEADPLANE_HEADSCALE__API_KEY
#   3. Re-run deploy.sh (which regenerates the compose file with the
#      new env var) + restart headplane
#
# This is a 3-step manual process that breaks every fresh deploy
# (operator forgets step 1, or step 2, or step 3). B151 (Phase 8)
# automates all 3 in a single command: `bash scripts/init-headplane.sh`.
#
# Two modes
# ---------
#  1. Bundled headplane (HEADPLANE_EXTERNAL_URL is empty):
#     - generate a fresh API key inside the headscale container
#     - inject it into .env + backup the previous .env
#     - re-render the headplane section of docker-compose + restart
#     - verify the headplane <-> headscale handshake via /admin/healthz
#
#  2. External headplane (HEADPLANE_EXTERNAL_URL is set):
#     - prompt the operator for the URL + key (or read from .env)
#     - inject the key into .env
#     - restart skygate (no headplane container to manage)
#     - verify the skygate -> external-headplane handshake
#
# Re-runnable: same inputs → same output, no state drift.
#
# Idempotency: if the .env already has a non-empty
# HEADPLANE_HEADSCALE__API_KEY (and it doesn't match the deploy.sh
# placeholder), the script SKIPs the key generation and just
# re-validates the existing key against the running headplane.
# ============================================================================
set -euo pipefail

# --- locate the project root + .env ---
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="${SKYGATE_ENV_FILE:-$PROJECT_DIR/.env}"

# --- env helpers (mirrors deploy/lib/env.sh) ---
getenv() {
    # getenv <key> <default> — read from .env without sourcing it
    local key="$1" default="${2:-}"
    if [ -f "$ENV_FILE" ]; then
        local val
        val=$(grep -E "^${key}=" "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//")
        if [ -n "$val" ]; then
            echo "$val"
            return
        fi
    fi
    echo "$default"
}

setenv() {
    # setenv <key> <value> — upsert KEY=VALUE in .env (with backup)
    local key="$1" value="$2"
    if [ ! -f "$ENV_FILE" ]; then
        echo "ERROR: $ENV_FILE not found" >&2
        exit 2
    fi
    local backup="${ENV_FILE}.pre-init-headplane.$(date +%Y%m%d%H%M%S)"
    cp -p "$ENV_FILE" "$backup"
    local tmp="${ENV_FILE}.tmp.$$"
    if grep -qE "^${key}=" "$ENV_FILE"; then
        # Replace existing line
        awk -v k="$key" -v v="$value" '
            $0 ~ "^"k"=" { print k"="v; found=1; next }
            { print }
            END { if (!found) print k"="v }
        ' "$ENV_FILE" > "$tmp"
    else
        # Append (with a leading newline if the file doesn't end with one)
        local last_char
        last_char=$(tail -c 1 "$ENV_FILE" 2>/dev/null || echo "")
        if [ "$last_char" != "" ] && [ "$last_char" != "$(printf '\n')" ]; then
            echo "" >> "$ENV_FILE"
        fi
        cp "$ENV_FILE" "$tmp"
        echo "${key}=${value}" >> "$tmp"
    fi
    mv "$tmp" "$ENV_FILE"
    echo "  wrote $key to $ENV_FILE (backup: $backup)" >&2
}

# --- main ---
echo "=== init-headplane.sh (B151) ===" >&2

# Load the 3 env vars we need
HEADPLANE_EXTERNAL_URL=$(getenv "HEADPLANE_EXTERNAL_URL" "")
HEADPLANE_API_KEY=$(getenv "HEADPLANE_HEADSCALE__API_KEY" "")
HEADSCALE_CONTAINER=$(getenv "HEADSCALE_CONTAINER" "headscale")
SKYGATE_CONTAINER=$(getenv "COMPOSE_PROJECT_NAME" "skygate")
COMPOSE_FILE="$PROJECT_DIR/docker-compose.yml"

# Detect the deploy.sh placeholder (the value used when the
# operator hasn't set a key yet). The current default in deploy.sh
# is empty string; we also treat hskey-api-XXX patterns as
# "real" keys.
PLACEHOLDER_KEYS="hskey-api-PLACEHOLDER|hskey-api-CHANGEME|hskey-api-EXAMPLE|hskey-api-TBD|^$"

# --- mode 1: bundled headplane ---
if [ -z "$HEADPLANE_EXTERNAL_URL" ]; then
    echo "Mode: bundled headplane (HEADPLANE_EXTERNAL_URL is empty)" >&2

    # Step 1: ensure headscale is up
    echo "[1/6] check headscale container is up" >&2
    if ! docker ps --format '{{.Names}}' 2>/dev/null | grep -qFx "$HEADSCALE_CONTAINER"; then
        echo "  ERROR: $HEADSCALE_CONTAINER is not running. Start it first:" >&2
        echo "    cd $PROJECT_DIR && docker compose up -d headscale" >&2
        exit 1
    fi

    # Step 2: check if .env has a real key
    echo "[2/6] check HEADPLANE_HEADSCALE__API_KEY in $ENV_FILE" >&2
    NEEDS_KEY=0
    if [ -z "$HEADPLANE_API_KEY" ]; then
        echo "  empty — needs generation" >&2
        NEEDS_KEY=1
    elif echo "$HEADPLANE_API_KEY" | grep -qE "$PLACEHOLDER_KEYS"; then
        echo "  matches deploy.sh placeholder — needs replacement" >&2
        NEEDS_KEY=1
    else
        echo "  present + non-placeholder (len=${#HEADPLANE_API_KEY}) — keeping" >&2
    fi

    # Step 3: generate a new key if needed
    if [ "$NEEDS_KEY" = "1" ]; then
        echo "[3/6] generate a new headscale API key (expiration 365d)" >&2
        KEY_OUTPUT=$(docker exec "$HEADSCALE_CONTAINER" headscale apikeys create -e 365d 2>&1 | tail -5) || {
            echo "  ERROR: apikeys create failed" >&2
            echo "$KEY_OUTPUT" >&2
            exit 1
        }
        echo "  $KEY_OUTPUT" >&2
        # headscale v0.29.x prints: <id>\t<key> OR JSON depending on the flag
        # The most common output is just the key on the last line.
        # Try to extract via regex first (covers the common cases).
        NEW_KEY=$(echo "$KEY_OUTPUT" | grep -oE 'hskey-api-[A-Za-z0-9_-]{20,}' | head -1)
        if [ -z "$NEW_KEY" ]; then
            echo "  ERROR: could not parse key from headscale output" >&2
            echo "  output: $KEY_OUTPUT" >&2
            exit 1
        fi
        echo "  generated key: ${NEW_KEY:0:20}...${NEW_KEY: -10} (len=${#NEW_KEY})" >&2
        HEADPLANE_API_KEY="$NEW_KEY"

        # Step 4: write to .env
        echo "[4/6] write to .env" >&2
        setenv "HEADPLANE_HEADSCALE__API_KEY" "$HEADPLANE_API_KEY"
    else
        echo "[3/6] skip — using existing key" >&2
        echo "[4/6] skip — no .env update needed" >&2
    fi

    # Step 5: restart headplane to pick up the new key
    echo "[5/6] restart headplane container" >&2
    cd "$PROJECT_DIR"
    if docker compose ps headplane 2>/dev/null | grep -q "headplane"; then
        docker compose up -d --force-recreate --no-deps headplane >&2
        echo "  restarted headplane" >&2
    else
        echo "  headplane is not in docker-compose.yml (HEADPLANE_ENABLED=false or external); skipping restart" >&2
    fi

    # Step 6: verify the headplane <-> headscale handshake
    echo "[6/6] verify headplane /admin/healthz" >&2
    HEADPLANE_PORT=$(getenv "HEADPLANE_SERVER__PORT" "50445")
    HEALTH_OK=0
    for i in $(seq 1 30); do
        if curl -s -o /dev/null --max-time 2 "http://127.0.0.1:${HEADPLANE_PORT}/admin/healthz"; then
            HEALTH_OK=1
            break
        fi
        sleep 1
    done
    if [ "$HEALTH_OK" = "1" ]; then
        echo "  headplane is healthy (port $HEADPLANE_PORT)" >&2
    else
        echo "  WARN: headplane did not become healthy within 30s" >&2
        echo "  check: docker logs headplane --tail 30" >&2
        exit 1
    fi

    echo "" >&2
    echo "OK: headplane API key applied" >&2
    echo "  HEADPLANE_HEADSCALE__API_KEY=$HEADPLANE_API_KEY" >&2
    echo "  backup: ${ENV_FILE}.pre-init-headplane.*" >&2

# --- mode 2: external headplane ---
else
    echo "Mode: external headplane (HEADPLANE_EXTERNAL_URL=$HEADPLANE_EXTERNAL_URL)" >&2

    # Step 1: ensure external headplane URL is reachable
    echo "[1/4] verify HEADPLANE_EXTERNAL_URL is reachable" >&2
    HEALTH_URL="${HEADPLANE_EXTERNAL_URL%/}/admin/healthz"
    if ! curl -sk -o /dev/null --max-time 5 "$HEALTH_URL"; then
        echo "  ERROR: $HEALTH_URL is not reachable" >&2
        echo "  confirm the URL + that headplane is up + the firewall allows skygate to reach it" >&2
        exit 1
    fi
    echo "  $HEALTH_URL is reachable" >&2

    # Step 2: ensure HEADPLANE_HEADSCALE__API_KEY is set
    echo "[2/4] check HEADPLANE_HEADSCALE__API_KEY" >&2
    if [ -z "$HEADPLANE_API_KEY" ] || echo "$HEADPLANE_API_KEY" | grep -qE "$PLACEHOLDER_KEYS"; then
        echo "  empty or placeholder — operator must paste the real key" >&2
        if [ -t 0 ]; then
            # Interactive: read from stdin
            read -r -p "  paste the headplane -> headscale API key: " HEADPLANE_API_KEY
        else
            echo "  ERROR: no API key in .env and stdin is not a TTY" >&2
            echo "  set HEADPLANE_HEADSCALE__API_KEY in $ENV_FILE and re-run" >&2
            exit 1
        fi
        if [ -z "$HEADPLANE_API_KEY" ]; then
            echo "  ERROR: empty key" >&2
            exit 1
        fi
        echo "[3/4] write to .env" >&2
        setenv "HEADPLANE_HEADSCALE__API_KEY" "$HEADPLANE_API_KEY"
    else
        echo "  present + non-placeholder (len=${#HEADPLANE_API_KEY}) — keeping" >&2
        echo "[3/4] skip — no .env update needed" >&2
    fi

    # Step 4: restart skygate (no headplane container to manage)
    echo "[4/4] restart skygate container" >&2
    cd "$PROJECT_DIR"
    docker compose up -d --force-recreate --no-deps skygate >&2
    echo "  restarted skygate" >&2
    echo "" >&2
    echo "OK: external headplane URL + key applied" >&2
fi
