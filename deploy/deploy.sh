#!/usr/bin/env bash
#===============================================================================
# deploy.sh — Unified Skygate deployment
#===============================================================================
# Usage:
#   ./deploy/deploy.sh                        # Fresh install / reconfigure
#   ./deploy/deploy.sh --from-path <dir>      # Restore from backup directory
#
# This script is the SINGLE entry point for:
#   - Fresh headscale + headplane + skygate setup
#   - Migration from backup (--from-path)
#   - Reconfiguration after .env changes
#===============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# ── Parse args ─────────────────────────────────────────────────────────────
FROM_PATH=""
while [ $# -gt 0 ]; do
    case "$1" in
        --from-path) FROM_PATH="$2"; shift 2 ;;
        *) echo "Unknown arg: $1"; echo "Usage: $0 [--from-path <dir>]"; exit 1 ;;
    esac
done

# ── Load env ───────────────────────────────────────────────────────────────
# When restoring, use the backup's .env
if [ -n "${FROM_PATH}" ] && [ -f "${FROM_PATH}/.env" ]; then
    export SKYGATE_ENV="${FROM_PATH}/.env"
fi
source "${SCRIPT_DIR}/lib/env.sh"
source "${SCRIPT_DIR}/lib/docker.sh"

MODE="fresh"
if [ -n "${FROM_PATH}" ]; then
    MODE="restore"
    if [ ! -d "${FROM_PATH}" ]; then
        err "--from-path directory not found: ${FROM_PATH}"
    fi
    log "Restore mode — source: ${FROM_PATH}"
else
    log "Fresh install mode"
fi

echo "=============================================="
echo "  Skygate Unified Deploy"
echo "  Mode:     ${MODE}"
echo "  Project:  ${PROJECT_DIR}"
echo "  Headscale dir: ${DEPLOY_HEADSCALE_DIR}"
echo "=============================================="

# ── Helper: render template ────────────────────────────────────────────────
render_template() {
    local tmpl="$1"
    local dest="$2"

    # Use envsubst for safe variable substitution.
    # Falls back to Python if envsubst not available.
    if command -v envsubst >/dev/null 2>&1; then
        envsubst < "${tmpl}" > "${dest}.tmp"
    else
        python3 -c "
import os, re
with open('${tmpl}') as f:
    content = f.read()
# Substitute ${VAR} with env values
def sub_var(m):
    var = m.group(1)
    return os.environ.get(var, m.group(0))
content = re.sub(r'\$\{([A-Za-z_][A-Za-z0-9_]*)\}', sub_var, content)
with open('${dest}.tmp', 'w') as f:
    f.write(content)
" || err "Template rendering failed for ${tmpl}"
    fi

    # Standard env var substitution
    while IFS= read -r line; do
        # Only process lines with ${...} patterns
        if [[ "${line}" =~ \$\{[A-Za-z_][A-Za-z0-9_]*\} ]]; then
            eval "line=\"${line}\""
        fi
        echo "${line}"
    done <<< "${content}" > "${dest}.tmp"

    # Special markers that need YAML list conversion:
    # __HEADSCALE_AUTO_APPROVE_ROUTES__ → YAML list from comma-separated
    local routes_list=""
    IFS=',' read -ra ROUTES <<< "${HEADSCALE_AUTO_APPROVE_ROUTES}"
    for r in "${ROUTES[@]}"; do
        r=$(echo "$r" | xargs)  # trim
        [ -n "$r" ] && routes_list+="     - ${r}\n"
    done

    # __HEADSCALE_DERP_URLS__ → YAML list from comma-separated
    local derp_list=""
    IFS=',' read -ra DERPS <<< "${HEADSCALE_DERP_URLS}"
    for d in "${DERPS[@]}"; do
        d=$(echo "$d" | xargs)
        [ -n "$d" ] && derp_list+="    - ${d}\n"
    done

    # Apply special markers
    sed -i "s|__HEADSCALE_AUTO_APPROVE_ROUTES__|${routes_list}|g" "${dest}.tmp"
    sed -i "s|__HEADSCALE_DERP_URLS__|${derp_list}|g" "${dest}.tmp"

    mv "${dest}.tmp" "${dest}"
    log "Rendered: ${dest}"
}

# ═══════════════════════════════════════════════════════════════════════════
# STEP 1: Directories & prerequisites
# ═══════════════════════════════════════════════════════════════════════════
echo ""
echo "── Step 1: Directories & network ──"

mkdir -p "${DEPLOY_HEADSCALE_DIR}/config"
mkdir -p "${DEPLOY_HEADSCALE_DIR}/headplane"
mkdir -p /home/skyadmin/.ssh
chmod 700 /home/skyadmin/.ssh 2>/dev/null || true

ensure_network "${DOCKER_NETWORK}" "${DOCKER_SUBNET}"

# ═══════════════════════════════════════════════════════════════════════════
# STEP 2: Headscale configuration
# ═══════════════════════════════════════════════════════════════════════════
echo ""
echo "── Step 2: Headscale configuration ──"

# Render config.yaml from template
render_template     "${PROJECT_DIR}/deploy/templates/headscale-config.yaml.tmpl"     "${DEPLOY_HEADSCALE_DIR}/config/config.yaml"

# Restore noise_private.key (critical for API key validity)
if [ "${MODE}" = "restore" ]; then
    NOISE_SRC="${FROM_PATH}/headscale-config/noise_private.key"
    if [ -f "${NOISE_SRC}" ]; then
        cp "${NOISE_SRC}" "${DEPLOY_HEADSCALE_DIR}/config/noise_private.key"
        chmod 600 "${DEPLOY_HEADSCALE_DIR}/config/noise_private.key"
        log "Restored noise_private.key"
    else
        warn "noise_private.key not in backup — headscale API keys will be INVALID"
        warn "You will need to regenerate HEADSCALE_API_KEY after deploy"
    fi
else
    # Fresh install — generate new key
    if [ ! -f "${DEPLOY_HEADSCALE_DIR}/config/noise_private.key" ]; then
        openssl rand -base64 32 > "${DEPLOY_HEADSCALE_DIR}/config/noise_private.key"
        chmod 600 "${DEPLOY_HEADSCALE_DIR}/config/noise_private.key"
        log "Generated new noise_private.key"
    fi
fi

# Render headscale docker-compose
render_template     "${PROJECT_DIR}/deploy/templates/headscale-compose.yml.tmpl"     "${DEPLOY_HEADSCALE_DIR}/docker-compose.yml"

# ═══════════════════════════════════════════════════════════════════════════
# STEP 3: Headplane configuration
# ═══════════════════════════════════════════════════════════════════════════
echo ""
echo "── Step 3: Headplane configuration ──"

# Copy static headplane config (secrets come from env vars)
cp "${PROJECT_DIR}/deploy/templates/headplane-config.yaml"    "${DEPLOY_HEADSCALE_DIR}/headplane/config.yaml"
log "Copied headplane config"

# ═══════════════════════════════════════════════════════════════════════════
# STEP 4: Start Headscale + Headplane
# ═══════════════════════════════════════════════════════════════════════════
echo ""
echo "── Step 4: Starting Headscale + Headplane ──"

cd "${DEPLOY_HEADSCALE_DIR}"

# Restore headscale DB before starting (if in restore mode)
if [ "${MODE}" = "restore" ]; then
    HS_DB="${FROM_PATH}/headscale-db.sqlite"
    if [ -f "${HS_DB}" ]; then
        docker volume create headscale_headscale_data 2>/dev/null || true
        volume_copy_in headscale_headscale_data "${HS_DB}" db.sqlite
    fi

    # Restore headplane data
    if [ -d "${FROM_PATH}/headplane-data" ] && [ "$(ls -A "${FROM_PATH}/headplane-data" 2>/dev/null)" ]; then
        docker volume create headscale_headplane_data 2>/dev/null || true
        volume_copy_dir headscale_headplane_data "${FROM_PATH}/headplane-data"
    fi
fi

docker compose up -d 2>&1 || warn "docker compose up had warnings"

# Wait for headscale
wait_for_http "http://localhost:50444/api/v1/node" 200 60
container_stable headscale 10

# Wait for headplane
wait_for_http "http://localhost:50445/admin/" "2xx" 30
container_stable headplane 5

# ═══════════════════════════════════════════════════════════════════════════
# STEP 5: Skygate
# ═══════════════════════════════════════════════════════════════════════════
echo ""
echo "── Step 5: Skygate ──"

cd "${PROJECT_DIR}"

# Restore source code from git bundle (if restoring and no .git)
if [ "${MODE}" = "restore" ] && [ ! -d "${PROJECT_DIR}/.git" ]; then
    BUNDLE="${FROM_PATH}/skygate-repo.bundle"
    if [ -f "${BUNDLE}" ]; then
        log "Restoring Skygate source from git bundle..."
        git clone "${BUNDLE}" "${PROJECT_DIR}" 2>/dev/null || {
            warn "Git clone failed — extracting files manually"
            cd "${PROJECT_DIR}" && git init && git fetch "${BUNDLE}" && git checkout -b main FETCH_HEAD 2>/dev/null ||                 warn "Manual restore also failed — source may be incomplete"
        }
        log "Source code restored"
    fi
fi

# Copy .env to project root
if [ "${MODE}" = "restore" ] && [ -f "${FROM_PATH}/.env" ]; then
    cp "${FROM_PATH}/.env" "${PROJECT_DIR}/.env"
    chmod 600 "${PROJECT_DIR}/.env"
    log "Restored .env"
fi

# Restore SSH keys
if [ "${MODE}" = "restore" ] && [ -d "${FROM_PATH}/ssh" ]; then
    cp "${FROM_PATH}/ssh/"* /home/skyadmin/.ssh/ 2>/dev/null || true
    chmod 600 /home/skyadmin/.ssh/skygate_sync 2>/dev/null || true
    chmod 644 /home/skyadmin/.ssh/skygate_sync.pub 2>/dev/null || true
    log "Restored SSH keys"
fi

# Restore skygate DB before starting
if [ "${MODE}" = "restore" ]; then
    SG_DB="${FROM_PATH}/skygate.db"
    if [ -f "${SG_DB}" ]; then
        docker volume create skygate-data 2>/dev/null || true
        volume_copy_in skygate-data "${SG_DB}" skygate.db
    fi
fi

# Build and start skygate
docker compose build 2>&1 | tail -3 || warn "docker compose build had warnings"
docker compose up -d 2>&1 || warn "docker compose up had warnings"

# Wait for skygate (build takes 10-30s)
wait_for_http "http://localhost:${SKYGATE_PORT}/login" 200 120
container_stable skygate 20

# ═══════════════════════════════════════════════════════════════════════════
# STEP 6: DERP (if enabled)
# ═══════════════════════════════════════════════════════════════════════════
if [ "${DERP_ENABLED}" = "true" ]; then
    echo ""
    echo "── Step 6: DERP Relay ──"

    render_template         "${PROJECT_DIR}/deploy/templates/derper-compose.yml.tmpl"         "${DEPLOY_HEADSCALE_DIR}/derper-compose.yml"

    mkdir -p /var/lib/derper/certs

    # Generate derpmap.json if not present
    if [ ! -f "${DEPLOY_HEADSCALE_DIR}/derpmap.json" ]; then
        DERP_KEY="${DERP_PRIVATE_KEY}"
        cat > "${DEPLOY_HEADSCALE_DIR}/derpmap.json" << DERPEOF
{
  "Regions": {
    "900": {
      "RegionID": 900,
      "RegionCode": "custom",
      "RegionName": "Skygate DERP",
      "Nodes": [{
        "Name": "1",
        "RegionID": 900,
        "HostName": "${DERP_HOSTNAME}",
        "DERPPort": 443,
        "STUNPort": ${DERP_STUN_PORT},
        "STUNOnly": false
      }]
    }
  }
}
DERPEOF
        log "Generated derpmap.json"
    fi

    # Generate/restore derper.conf
    if [ "${MODE}" = "restore" ] && [ -f "${FROM_PATH}/derper.conf" ]; then
        cp "${FROM_PATH}/derper.conf" /var/lib/derper/derper.conf
        chmod 600 /var/lib/derper/derper.conf
        log "Restored derper.conf"
    elif [ ! -f /var/lib/derper/derper.conf ]; then
        cat > /var/lib/derper/derper.conf << DERPEOF
{"PrivateKey": "privkey:${DERP_PRIVATE_KEY}"}
DERPEOF
        chmod 600 /var/lib/derper/derper.conf
        log "Generated derper.conf"
    fi

    if [ "${MODE}" = "restore" ] && [ -f "${FROM_PATH}/derpmap.json" ]; then
        cp "${FROM_PATH}/derpmap.json" "${DEPLOY_HEADSCALE_DIR}/derpmap.json"
    fi

    cd "${DEPLOY_HEADSCALE_DIR}"
    docker compose -f derper-compose.yml up -d 2>/dev/null || warn "DERP start failed"
    log "DERP relay started"
fi

# ═══════════════════════════════════════════════════════════════════════════
# Done
# ═══════════════════════════════════════════════════════════════════════════
echo ""
echo "=============================================="
echo "  Deploy complete!"
echo "=============================================="
echo ""
echo "Services:"
echo "  Headscale API:  http://localhost:50444"
echo "  Headplane UI:   http://localhost:50445/admin/"
echo "  Skygate:        http://localhost:${SKYGATE_PORT}/login"
if [ "${DERP_ENABLED}" = "true" ]; then
    echo "  DERP relay:     https://${DERP_HOSTNAME}"
    echo "  DERP map:       http://localhost:${DERP_MAP_PORT}/derpmap/default"
fi
echo ""
echo "Next steps:"
echo "  1. Run validation:  ./deploy/validate.sh"
echo "  2. Configure NPM reverse proxy for external access"
echo "  3. Check logs:      docker logs skygate"
echo ""

# Run validation
if [ -x "${SCRIPT_DIR}/validate.sh" ]; then
    echo "Running validation..."
    bash "${SCRIPT_DIR}/validate.sh"
fi
