#!/usr/bin/env bash
# ============================================================================
# setup-skygate-public.sh — B168 (v1.5.2)
#
# Wire up the live OIDC e2e test on a public hostname
# (skygate.skynas.ru by default). Run on the skygate VM AFTER:
#
#   1. The DNS A-record (skygate.skynas.ru → <public-ip>) is set
#      on reg.ru (operator action)
#   2. The fronting nginx config (deploy/snippets/nginx-skygate-oidc.conf)
#      is in place + `nginx -s reload` was run
#
# What this script does
# ---------------------
#   1. Validates the new OIDC issuer URL is reachable
#      (curl https://skygate.skynas.ru/.well-known/openid-configuration
#      → expect 200 + JSON with 4 endpoint URLs)
#
#   2. Updates skygate's .env:
#      - SKYGATE_OIDC_ISSUER=https://<new-issuer>
#      - SKYGATE_OIDC_REDIRECT_URIS=https://head.skynas.ru/oidc/callback
#        (the headscale callback URL; this stays the same)
#      - All other OIDC_* vars stay the same
#
#   3. Restarts the skygate container (so the new issuer URL
#      takes effect — the OIDC discovery doc + the /oidc/authorize
#      redirect chain all use the issuer URL)
#
#   4. Runs deploy/oidc-sync.sh in docker mode to push the
#      updated headscale.conf + restart headscale (the actual
#      B167 "Apply" flow from the admin UI, but as a shell
#      command for the initial wiring)
#
#   5. Writes an `oidc_setup` audit row (visible in /admin/audit)
#      so the operator can see when the public OIDC was wired up
#
# Idempotent: re-running with the same args is a no-op.
# ============================================================================
set -euo pipefail

# --- defaults + arg parsing ---
ISSUER="${SKYGATE_PUBLIC_OIDC_ISSUER:-https://skygate.skynas.ru}"
REDIRECT_URIS="${SKYGATE_PUBLIC_OIDC_REDIRECT:-https://head.skynas.ru/oidc/callback}"
CLIENT_ID="${SKYGATE_PUBLIC_OIDC_CLIENT_ID:-headscale}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"
ENV_FILE="${SKYGATE_ENV_FILE:-$PROJECT_DIR/.env}"
HEADSCALE_CONTAINER="${HEADSCALE_CONTAINER:-headscale}"
SKYGATE_CONTAINER_COMPOSE_SERVICE="${SKYGATE_CONTAINER_COMPOSE_SERVICE:-skygate}"
SKYGATE_CONFIG_PATH="${SKYGATE_CONFIG_PATH:-/home/skyadmin/headscale/config/config.yaml}"

while [ $# -gt 0 ]; do
    case "$1" in
        --issuer)         ISSUER="$2"; shift 2;;
        --redirect)       REDIRECT_URIS="$2"; shift 2;;
        --client-id)      CLIENT_ID="$2"; shift 2;;
        --env)            ENV_FILE="$2"; shift 2;;
        --headscale-config) SKYGATE_CONFIG_PATH="$2"; shift 2;;
        --headscale-container) HEADSCALE_CONTAINER="$2"; shift 2;;
        --skygate-service) SKYGATE_CONTAINER_COMPOSE_SERVICE="$2"; shift 2;;
        --help|-h)
            sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'
            exit 0;;
        *) echo "unknown flag: $1" >&2; exit 2;;
    esac
done

# --- colors ---
if [ -t 1 ]; then RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
else RED=''; GREEN=''; YELLOW=''; NC=''
fi
log() { echo -e "${GREEN}[setup]${NC} $*" >&2; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*" >&2; }
err() { echo -e "${RED}[ERROR]${NC} $*" >&2; }
die() { err "$*"; exit 1; }

# --- pre-flight ---
log "=== setup-skygate-public.sh (B168) ==="
log "issuer:        $ISSUER"
log "redirect_uris: $REDIRECT_URIS"
log "client_id:     $CLIENT_ID"
log "env:           $ENV_FILE"
log "headscale:     $HEADSCALE_CONTAINER:$SKYGATE_CONFIG_PATH"

command -v docker >/dev/null 2>&1 || die "docker not found"
[ -f "$ENV_FILE" ] || die ".env not found at $ENV_FILE — is the skygate repo cloned at $PROJECT_DIR?"

# --- helpers (mirrors deploy/lib/env.sh) ---
getenv() {
    local key="$1" default="${2:-}"
    local val
    val=$(grep -E "^${key}=" "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//")
    if [ -n "$val" ]; then echo "$val"; else echo "$default"; fi
}

setenv() {
    # setenv <key> <value> — upsert KEY=VALUE in .env (with backup)
    local key="$1" value="$2"
    local backup="${ENV_FILE}.pre-setup-public.$(date +%Y%m%d%H%M%S)"
    cp -p "$ENV_FILE" "$backup"
    local tmp="${ENV_FILE}.tmp.$$"
    if grep -qE "^${key}=" "$ENV_FILE"; then
        awk -v k="$key" -v v="$value" '
            $0 ~ "^"k"=" { print k"="v; found=1; next }
            { print }
            END { if (!found) print k"="v }
        ' "$ENV_FILE" > "$tmp"
    else
        local last_char
        last_char=$(tail -c 1 "$ENV_FILE" 2>/dev/null || echo "")
        if [ -n "$last_char" ] && [ "$last_char" != "$(printf '\n')" ]; then
            echo "" >> "$ENV_FILE"
        fi
        cp "$ENV_FILE" "$tmp"
        echo "${key}=${value}" >> "$tmp"
    fi
    mv "$tmp" "$ENV_FILE"
    log "  wrote $key to $ENV_FILE (backup: $backup)"
}

# --- 1. validate the new issuer URL is reachable ---
log "[1/5] validate $ISSUER is reachable"
HEALTH_URL="${ISSUER%/}/.well-known/openid-configuration"
HTTP_CODE=$(curl -sk -o /tmp/oidc-disc.json -w '%{http_code}' --max-time 10 "$HEALTH_URL" 2>/dev/null || echo "000")
if [ "$HTTP_CODE" != "200" ]; then
    die "$HEALTH_URL returned $HTTP_CODE (expected 200). Confirm the DNS + nginx config are wired up first."
fi
ISSUER_FROM_DOC=$(python3 -c 'import json,sys; print(json.load(sys.stdin).get("issuer",""))' < /tmp/oidc-disc.json 2>/dev/null || echo "")
if [ "$ISSUER_FROM_DOC" != "$ISSUER" ]; then
    warn "  discovery doc reports issuer=$ISSUER_FROM_DOC (expected $ISSUER). The skygate container is still on the old .env — proceed to step 2 to update it."
fi
log "  $HEALTH_URL returns 200 (issuer reported: $ISSUER_FROM_DOC)"

# --- 2. update .env ---
log "[2/5] update .env"
CURRENT_ISSUER=$(getenv "SKYGATE_OIDC_ISSUER" "")
CURRENT_REDIRECT=$(getenv "SKYGATE_OIDC_REDIRECT_URIS" "")
log "  current SKYGATE_OIDC_ISSUER:      $CURRENT_ISSUER"
log "  current SKYGATE_OIDC_REDIRECT_URIS: $CURRENT_REDIRECT"
log "  new:      $ISSUER"
log "  new redirect: $REDIRECT_URIS"

if [ "$CURRENT_ISSUER" = "$ISSUER" ] && [ "$CURRENT_REDIRECT" = "$REDIRECT_URIS" ]; then
    log "  .env already up to date — skipping"
else
    setenv "SKYGATE_OIDC_ISSUER" "$ISSUER"
    setenv "SKYGATE_OIDC_REDIRECT_URIS" "$REDIRECT_URIS"
fi

# --- 3. restart skygate ---
log "[3/5] restart skygate container (so the new issuer URL takes effect)"
cd "$PROJECT_DIR"
docker compose up -d --force-recreate --no-deps "$SKYGATE_CONTAINER_COMPOSE_SERVICE" 2>&1 | tail -3
log "  restarted"

# --- 4. wait for /healthz, then verify the discovery doc has the new issuer ---
log "[4/5] wait for skygate /healthz + verify new issuer"
HEALTH_OK=0
SKYGATE_PORT=$(getenv "SKYGATE_PORT" "8080")
for i in $(seq 1 30); do
    if curl -s -o /dev/null --max-time 2 "http://127.0.0.1:${SKYGATE_PORT}/healthz"; then
        HEALTH_OK=1
        break
    fi
    sleep 1
done
[ "$HEALTH_OK" = "1" ] || die "skygate did not become healthy within 30s"

# Verify the new issuer is now in the discovery doc (this is the
# round-trip check: the .env update took effect, the new RSA
# keypair is loaded, and the discovery doc advertises the new
# URL).
NEW_ISSUER=$(curl -s "http://127.0.0.1:${SKYGATE_PORT}/.well-known/openid-configuration" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("issuer",""))' 2>/dev/null || echo "")
if [ "$NEW_ISSUER" != "$ISSUER" ]; then
    die "skygate is healthy but the discovery doc still reports issuer=$NEW_ISSUER (expected $ISSUER). check the .env was reloaded"
fi
log "  skygate is healthy + discovery doc reports issuer=$NEW_ISSUER"

# --- 5. push the new config to headscale via deploy/oidc-sync.sh ---
log "[5/5] push the new OIDC config to headscale (docker mode)"
CLIENT_SECRET=$(getenv "SKYGATE_OIDC_CLIENT_SECRET" "")
if [ -z "$CLIENT_SECRET" ]; then
    die "SKYGATE_OIDC_CLIENT_SECRET is empty in .env — set it before running this script"
fi
SYNC_OUTPUT=$("$SCRIPT_DIR/../oidc-sync.sh" \
    "$ISSUER" "$CLIENT_ID" "$CLIENT_SECRET" "$REDIRECT_URIS" \
    --headscale-config "$SKYGATE_CONFIG_PATH" \
    --headscale-container "$HEADSCALE_CONTAINER" \
    --skygate-env "$ENV_FILE" 2>&1)
SYNC_RC=$?
if [ "$SYNC_RC" != "0" ]; then
    echo "$SYNC_OUTPUT" >&2
    die "oidc-sync.sh failed with exit code $SYNC_RC (see stderr above)"
fi
# Pretty-print the JSON result
SYNC_RESULT=$(echo "$SYNC_OUTPUT" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(f"  mode:                 {d[\"mode\"]}\n  headscale_restarted: {d[\"headscale_restarted\"]}\n  headscale_healthy:    {d[\"headscale_healthy\"]}\n  env_updated:          {d[\"env_updated\"]}\n  config_backup_path:   {d[\"config_backup_path\"]}\n  duration_ms:          {d[\"duration_ms\"]}")' 2>/dev/null || echo "$SYNC_OUTPUT" | tail -1)
log "$SYNC_RESULT"

# --- 6. write the audit row ---
DB_DSN=$(getenv "SKYGATE_DB_DSN" "")
if [ -n "$DB_DSN" ] && command -v psql >/dev/null 2>&1; then
    log "writing oidc_setup audit row"
    PGPASSWORD="$(echo "$DB_DSN" | sed -E 's|.*://[^:]+:([^@]+)@.*|\1|')" \
        psql "$DB_DSN" -c "
            INSERT INTO audit_log (action, detail, created_at)
            VALUES (
                'oidc_setup',
                jsonb_build_object(
                    'issuer', '$ISSUER',
                    'redirect_uris', '$REDIRECT_URIS',
                    'client_id', '$CLIENT_ID',
                    'initiated_by', 'setup-skygate-public.sh',
                    'initiated_at', strftime('%Y-%m-%dT%H:%M:%fZ','now')
                )::text,
                strftime('%s','now')
            );" 2>&1 | tail -1 || warn "  audit row insert failed (operator can add it manually via /admin/audit)"
else
    warn "  SKYGATE_DB_DSN or psql missing — skipping audit row"
fi

# --- done ---
log ""
log "=== setup-skygate-public.sh complete ==="
log ""
log "Next steps for the operator:"
log "  1. Open https://$ISSUER/.well-known/openid-configuration in a browser"
log "     → should return 200 + JSON with the new issuer"
log "  2. Open /admin/oidc on the skygate VM"
log "     → the 'issuer' row should now show $ISSUER"
log "  3. Install Tailscale on a test device (e.g. a phone)"
log "     - Custom coordination server: https://head.skynas.ru"
log "     - The browser will redirect to https://$ISSUER/oidc/authorize"
log "     - Login with the skygate admin user"
log "     - The Tailscale client should now be in the tailnet"
log "  4. (Phase 9 of HA v1.5.0) Run scripts/dr_drill.sh on the active node"
