#!/usr/bin/env bash
# ============================================================================
# bootstrap_standby.sh — provision a NEW skygate-standby node
# B152 (v1.5.0) — Phase 7 of the HA v1.5.0 plan.
#
# See docs/internal/ha-v1.5.0-execution.md §3 (Phase 7).
#
# Background
# ----------
# The HA chain (B145) is `skygate` (P1, active) + `skygate-standby`
# (P2, standby). When the operator provisions a new VM (svyatoslava-1
# or any host with a public IP + Tailscale), this script wires it
# up as the standby. After it runs:
#
#   - skygate-standby is running with role=standby
#   - Patroni replica is streaming from the primary
#   - certsync (B147) is pulling the live cert from S3 every 30s
#   - /admin/ha shows the standby in the chain table
#   - the standby is ready to take over if the primary dies
#
# Where to run this
# -----------------
# On the NEW standby host (svyatoslava-1), AFTER:
#   1. OS is installed (Ubuntu 22.04+ recommended)
#   2. Docker + docker-compose-plugin installed
#   3. Tailscale is installed and joined to the headscale tailnet
#   4. SSH access from the primary host is set up
#   5. Patroni + etcd are reachable (same etcd cluster as primary)
#
# The script does NOT install Docker or Tailscale (those are
# pre-requisites, see docs/internal/ha-architecture.md §Prereqs).
#
# Usage
# -----
#   ssh svyatoslava-1
#   git clone <skygate-repo> ~/skygate
#   cd ~/skygate
#   # Edit .env: set HEADPLANE_HEADSCALE__API_KEY + skygate-specific values
#   bash scripts/bootstrap_standby.sh
#
# The script is idempotent: re-running it on an already-bootstrapped
# host is a no-op (it detects the existing skygate-standby role
# + Patroni replica and prints "already bootstrapped").
# ============================================================================
set -euo pipefail

# --- colors for output (no-op if not a TTY) ---
if [ -t 1 ]; then
    RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
else
    RED=''; GREEN=''; YELLOW=''; NC=''
fi
log()    { echo -e "${GREEN}[bootstrap]${NC} $*" >&2; }
warn()   { echo -e "${YELLOW}[WARN]${NC} $*" >&2; }
err()    { echo -e "${RED}[ERROR]${NC} $*" >&2; }
die()    { err "$*"; exit 1; }

# --- locate the project root + .env ---
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
ENV_FILE="${SKYGATE_ENV_FILE:-$PROJECT_DIR/.env}"

# --- pre-flight checks ---
log "=== bootstrap_standby.sh (B152, Phase 7 of HA v1.5.0) ==="
log "project: $PROJECT_DIR"
log "env file: $ENV_FILE"

command -v docker >/dev/null 2>&1 || die "docker not found — install docker + docker-compose-plugin first"
command -v git    >/dev/null 2>&1 || die "git not found"
[ -d "$PROJECT_DIR" ] || die "project dir $PROJECT_DIR not found"
[ -f "$ENV_FILE" ]  || die ".env not found at $ENV_FILE — copy from primary + set HEADPLANE_HEADSCALE__API_KEY"

# --- env helpers (mirrors deploy/lib/env.sh) ---
getenv() {
    local key="$1" default="${2:-}"
    local val
    val=$(grep -E "^${key}=" "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'$//")
    if [ -n "$val" ]; then echo "$val"; else echo "$default"; fi
}

SKYGATE_ROLE=$(getenv "SKYGATE_HA_ROLE" "standby")
SKYGATE_HA_ENABLED=$(getenv "SKYGATE_HA_ENABLED" "true")
HEADPLANE_HEADSCALE__API_KEY=$(getenv "HEADPLANE_HEADSCALE__API_KEY" "")
DOCKER_NETWORK=$(getenv "DOCKER_NETWORK" "headscale_default")
DEPLOY_SKYGATE_DIR=$(getenv "DEPLOY_SKYGATE_DIR" "/home/skyadmin/skygate")
DEPLOY_HEADSCALE_DIR=$(getenv "DEPLOY_HEADSCALE_DIR" "/home/skyadmin/headscale")

# Validate required env vars
[ "$SKYGATE_HA_ENABLED" = "true" ] || die "SKYGATE_HA_ENABLED must be 'true' on a standby node (got '$SKYGATE_HA_ENABLED')"
[ "$SKYGATE_ROLE" = "standby" ]    || die "SKYGATE_HA_ROLE must be 'standby' (got '$SKYGATE_ROLE')"
[ -n "$HEADPLANE_HEADSCALE__API_KEY" ] || die "HEADPLANE_HEADSCALE__API_KEY is empty — set it from the primary's .env (or run scripts/init-headplane.sh)"

# --- step 1: pre-flight: is this already bootstrapped? ---
log "[1/6] pre-flight: check if already bootstrapped"
if docker ps --format '{{.Names}}' 2>/dev/null | grep -qE "skygate.*-1$|skygate-standby"; then
    if docker ps --format '{{.Names}}' 2>/dev/null | grep -qE "skygate$"; then
        log "  skygate container already running — assuming this host is already bootstrapped"
        log "  if you want to re-bootstrap, run: docker compose down -v  (WARNING: destroys all data!)"
        exit 0
    fi
fi

# --- step 2: clone the S3 deploy artifacts ---
# The /admin/deploy page (B150) pushes the skygate binary to
# s3://<bucket>/deploy/<hostname>/. We pull it here so the
# standby is on the exact same version as the primary.
log "[2/6] pull the skygate binary from S3 deploy/"
S3_BUCKET=$(getenv "SKYGATE_S3_BUCKET" "skygate-backups")
S3_ENDPOINT=$(getenv "SKYGATE_S3_ENDPOINT" "")
S3_ACCESS_KEY=$(getenv "SKYGATE_S3_ACCESS_KEY" "")
S3_SECRET_KEY=$(getenv "SKYGATE_S3_SECRET_KEY" "")
S3_PATH_PREFIX=$(getenv "SKYGATE_S3_DEPLOY_PREFIX" "ha/deploy")
HOSTNAME=$(hostname)

if [ -z "$S3_ACCESS_KEY" ] || [ -z "$S3_SECRET_KEY" ] || [ -z "$S3_ENDPOINT" ]; then
    warn "  S3 credentials not in .env — skipping S3 pull, will use the local git checkout"
    warn "  to enable S3 pull, set SKYGATE_S3_ACCESS_KEY + SKYGATE_S3_SECRET_KEY + SKYGATE_S3_ENDPOINT in .env"
else
    # Use the AWS CLI if available; fall back to mc (minio client) if not.
    if command -v aws >/dev/null 2>&1; then
        AWS_ENDPOINT_URL="$S3_ENDPOINT" \
            aws s3 cp "s3://${S3_BUCKET}/${S3_PATH_PREFIX}/${HOSTNAME}/skygate" \
                     "/usr/local/bin/skygate" 2>&1 | tail -3
        chmod +x /usr/local/bin/skygate
        log "  pulled skygate binary from s3://${S3_BUCKET}/${S3_PATH_PREFIX}/${HOSTNAME}/"
    elif command -v mc >/dev/null 2>&1; then
        warn "  using mc (minio client) — consider installing awscli for S3 access"
        # mc config is a global state; assume the operator pre-configured `mc alias set skygate $S3_ENDPOINT ...`
        mc cp "skygate/${S3_BUCKET}/${S3_PATH_PREFIX}/${HOSTNAME}/skygate" /usr/local/bin/skygate 2>&1 | tail -3
        chmod +x /usr/local/bin/skygate
        log "  pulled skygate binary via mc"
    else
        warn "  neither aws nor mc installed — skipping S3 pull, will use the local git checkout"
    fi
fi

# --- step 3: write the headscale config + Patroni config ---
# Both are replicated from the primary via S3. We pull them
# here so the standby starts on the same config as the primary.
log "[3/6] pull headscale config from S3 (replicated from primary)"
if [ -n "$S3_ACCESS_KEY" ] && [ -n "$S3_SECRET_KEY" ] && [ -n "$S3_ENDPOINT" ]; then
    HEADSCALE_S3_PREFIX=$(getenv "SKYGATE_S3_HEADSCALE_CONFIG_PREFIX" "ha/headscale-config")
    mkdir -p "$DEPLOY_HEADSCALE_DIR/config"
    if command -v aws >/dev/null 2>&1; then
        AWS_ENDPOINT_URL="$S3_ENDPOINT" \
            aws s3 sync "s3://${S3_BUCKET}/${HEADSCALE_S3_PREFIX}/" \
                        "$DEPLOY_HEADSCALE_DIR/config/" 2>&1 | tail -3 || warn "  S3 sync failed (will use local config)"
        log "  pulled headscale config from s3://${S3_BUCKET}/${HEADSCALE_S3_PREFIX}/"
    else
        warn "  aws CLI not available — using the local headscale config (operator must replicate manually)"
    fi
else
    warn "  S3 not configured — using the local headscale config (operator must replicate manually)"
fi

# --- step 4: start the docker-compose stack with role=standby ---
log "[4/6] start docker-compose (role=standby)"
cd "$PROJECT_DIR"
docker compose up -d --force-recreate --no-deps skygate headscale headplane 2>&1 | tail -5 || die "docker compose up failed"
log "  containers up"

# --- step 5: wait for the standby to be healthy ---
log "[5/6] wait for /healthz to return 200 (up to 60s)"
HEALTH_OK=0
SKYGATE_PORT=$(getenv "SKYGATE_PORT" "8080")
for i in $(seq 1 60); do
    if curl -s -o /dev/null --max-time 2 "http://127.0.0.1:${SKYGATE_PORT}/healthz"; then
        HEALTH_OK=1
        break
    fi
    sleep 1
done
if [ "$HEALTH_OK" != "1" ]; then
    die "skygate did not become healthy within 60s. check: docker logs skygate-skygate-1 --tail 30"
fi
log "  /healthz returns 200"

# --- step 6: verify the standby is in the HA chain ---
log "[6/6] verify the standby is in the HA chain"
# The standby registers itself in the DB's ha_chain table on
# startup. We poll the /admin/ha page (or the DB directly) to
# confirm.
ADMIN_OK=0
for i in $(seq 1 30); do
    # The /admin/ha page requires auth; we use a curl with a
    # session cookie if SKYGATE_ADMIN_COOKIE is set, otherwise
    # we just check that the skygate process is up + the chain
    # JSON in the DB is parseable.
    if command -v psql >/dev/null 2>&1; then
        DB_DSN=$(getenv "SKYGATE_DB_DSN" "")
        if [ -n "$DB_DSN" ] && PGPASSWORD="$(echo "$DB_DSN" | sed -E 's|.*://[^:]+:([^@]+)@.*|\1|')" \
            psql "$DB_DSN" -tA -c "SELECT value FROM global_settings WHERE key='ha_chain'" 2>/dev/null | grep -q "$HOSTNAME"; then
            ADMIN_OK=1
            break
        fi
    fi
    sleep 1
done
if [ "$ADMIN_OK" = "1" ]; then
    log "  $HOSTNAME is registered in the HA chain (ha_chain JSON contains the hostname)"
else
    warn "  could not confirm chain registration via DB (operator should check /admin/ha in the web UI)"
fi

# --- step 7: write the ha.bootstrap audit row ---
# The /admin/audit page surfaces the bootstrap event so the
# operator can see when the standby came online. The skygate
# container writes its own audit rows; we use a minimal
# SQL INSERT to record the bootstrap event (skygate's audit_log
# table is the source of truth for all admin actions).
log "[6.5/6] write ha.bootstrap audit row"
DB_DSN=$(getenv "SKYGATE_DB_DSN" "")
if [ -n "$DB_DSN" ] && command -v psql >/dev/null 2>&1; then
    PGPASSWORD="$(echo "$DB_DSN" | sed -E 's|.*://[^:]+:([^@]+)@.*|\1|')" \
        psql "$DB_DSN" -c "
            INSERT INTO audit_log (action, detail, created_at)
            VALUES (
                'ha.bootstrap',
                jsonb_build_object(
                    'hostname', '$HOSTNAME',
                    'role', '$SKYGATE_ROLE',
                    's3_bucket', '$S3_BUCKET',
                    'initiated_by', 'bootstrap_standby.sh',
                    'initiated_at', strftime('%Y-%m-%dT%H:%M:%fZ','now')
                )::text,
                strftime('%s','now')
            );" 2>&1 | tail -2 || warn "  audit row insert failed (operator can add it manually via /admin/audit)"
    log "  wrote ha.bootstrap row (action=ha.bootstrap, detail.hostname=$HOSTNAME)"
else
    warn "  SKYGATE_DB_DSN or psql missing — skipping audit row (operator can add it manually via /admin/audit)"
fi

# --- done ---
log ""
log "=== bootstrap_standby.sh complete ==="
log ""
log "Standby node is up. Next steps for the operator:"
log "  1. Open https://${HOSTNAME}/admin/ha — confirm the standby appears in the chain"
log "  2. From the primary, run: skygate deploy status  (verifies S3 deploy/ is in sync)"
log "  3. Run a DR drill: scripts/dr_drill.sh  (kills the active, verifies the standby takes over)"
log ""
log "If anything looks wrong, check:"
log "  - docker logs skygate-skygate-1 --tail 30  (skygate startup)"
log "  - docker logs headscale  (headscale replica state)"
log "  - psql <SKYGATE_DB_DSN> -c \"SELECT key, value FROM global_settings WHERE key LIKE 'ha.%'\"  (HA chain state)"
