#!/usr/bin/env bash
#===============================================================================
# deploy.sh — Unified Skygate deployment (cross-platform: Linux + Windows)
#===============================================================================
# Usage:
#   ./deploy/deploy.sh                        # Fresh install / reconfigure
#   ./deploy/deploy.sh --from-path <dir>      # Restore from backup directory
#===============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# ── Parse args ─────────────────────────────────────────────────────────────
FROM_PATH=""
while [ $# -gt 0 ]; do case "$1" in
    --from-path) FROM_PATH="$2"; shift 2 ;;
    *) echo "Unknown arg: $1"; echo "Usage: $0 [--from-path <dir>]"; exit 1 ;;
esac; done

# ── Load env ───────────────────────────────────────────────────────────────
if [ -n "${FROM_PATH}" ] && [ -f "${FROM_PATH}/.env" ]; then
    export SKYGATE_ENV="${FROM_PATH}/.env"; fi
source "${SCRIPT_DIR}/lib/env.sh"
source "${SCRIPT_DIR}/lib/docker.sh"

MODE="fresh"
[ -n "${FROM_PATH}" ] && MODE="restore"
[ "${MODE}" = "restore" ] && [ ! -d "${FROM_PATH}" ] && err "--from-path directory not found: ${FROM_PATH}"
log "${MODE} mode — ${FROM_PATH:-fresh install}"

echo "=============================================="
echo "  Skygate Unified Deploy  (OS: ${SKYGATE_OS})"
echo "  Mode:     ${MODE}"
echo "  Project:  ${PROJECT_DIR}"
echo "  Headscale dir: ${DEPLOY_HEADSCALE_DIR}"
echo "=============================================="

# ── Helper: render template (Python-only, cross-platform) ──────────────────
render_template() {
    local tmpl="$1"; local dest="$2"
    python3 -c "
import os, re
with open('${tmpl}') as f:
    content = f.read()
def sub_var(m):
    var = m.group(1)
    return os.environ.get(var, m.group(0))
content = re.sub(r'\\$\\{([A-Za-z_][A-Za-z0-9_]*)\\}', sub_var, content)
with open('${dest}.tmp', 'w') as f:
    f.write(content)
" || err "Template rendering failed for ${tmpl}"

    # Special markers -> YAML list conversion
    local routes_list=""; IFS=',' read -ra ROUTES <<< "${HEADSCALE_AUTO_APPROVE_ROUTES}"
    for r in "${ROUTES[@]}"; do r=$(echo "$r" | xargs); [ -n "$r" ] && routes_list+="     - ${r}\n"; done
    # 2026-07-15: v0.10.12 — DERP_EXTERNAL_URLS is appended to the
    # headscale DERP map list (alongside HEADSCALE_DERP_URLS and
    # the bundled derper, if DERP_ENABLED=true). This lets an
    # operator point at third-party derpers without touching the
    # default Tailscale DERP relay list.
    local derp_list=""; IFS=',' read -ra DERPS <<< "${HEADSCALE_DERP_URLS}"
    for d in "${DERPS[@]}"; do d=$(echo "$d" | xargs); [ -n "$d" ] && derp_list+="    - ${d}\n"; done
    if [ -n "${DERP_EXTERNAL_URLS}" ]; then
        IFS=',' read -ra EXT_DERPS <<< "${DERP_EXTERNAL_URLS}"
        for d in "${EXT_DERPS[@]}"; do d=$(echo "$d" | xargs); [ -n "$d" ] && derp_list+="    - ${d}\n"; done
    fi
    # Use python for sed replacement to avoid BSD/GNU sed differences
    python3 -c "
import re
with open('${dest}.tmp') as f: c = f.read()
c = c.replace('__HEADSCALE_AUTO_APPROVE_ROUTES__', '''${routes_list}''')
c = c.replace('__HEADSCALE_DERP_URLS__', '''${derp_list}''')
with open('${dest}', 'w') as f: f.write(c)
"
    rm -f "${dest}.tmp"
    log "Rendered: ${dest}"
}

# ── Helper: chmod if not windows ───────────────────────────────────────────
xchmod() { [ "${SKYGATE_OS}" = "windows" ] && return 0; chmod "$@"; }

# ═══════════════════════════════════════════════════════════════════════════
# STEP 1: Directories & network
# ═══════════════════════════════════════════════════════════════════════════
echo ""; echo "-- Step 1: Directories & network --"
mkdir -p "${DEPLOY_HEADSCALE_DIR}/config" "${DEPLOY_HEADSCALE_DIR}/headplane"
mkdir -p "${SSH_DIR}" 2>/dev/null || true
xchmod 700 "${SSH_DIR}" 2>/dev/null || true
ensure_network "${DOCKER_NETWORK}" "${DOCKER_SUBNET}"

# ═══════════════════════════════════════════════════════════════════════════
# STEP 2: Headscale configuration
# ═══════════════════════════════════════════════════════════════════════════
echo ""; echo "-- Step 2: Headscale configuration --"
render_template "${PROJECT_DIR}/deploy/templates/headscale-config.yaml.tmpl"     "${DEPLOY_HEADSCALE_DIR}/config/config.yaml"

if [ "${MODE}" = "restore" ]; then
    NOISE_SRC="${FROM_PATH}/headscale-config/noise_private.key"
    if [ -f "${NOISE_SRC}" ]; then
        cp "${NOISE_SRC}" "${DEPLOY_HEADSCALE_DIR}/config/noise_private.key"
        xchmod 600 "${DEPLOY_HEADSCALE_DIR}/config/noise_private.key"
        log "Restored noise_private.key"
    else
        warn "noise_private.key not in backup — API keys will be INVALID"; fi
else
    if [ ! -f "${DEPLOY_HEADSCALE_DIR}/config/noise_private.key" ]; then
        openssl rand -base64 32 > "${DEPLOY_HEADSCALE_DIR}/config/noise_private.key"
        xchmod 600 "${DEPLOY_HEADSCALE_DIR}/config/noise_private.key"
        log "Generated new noise_private.key"; fi
fi

render_template "${PROJECT_DIR}/deploy/templates/headscale-compose.yml.tmpl"     "${DEPLOY_HEADSCALE_DIR}/docker-compose.yml"

# ═══════════════════════════════════════════════════════════════════════════
# STEP 2b: Caddy (v0.15.0) — optional TLS terminator
# ═══════════════════════════════════════════════════════════════════════════
# Renders deploy/templates/Caddyfile.tmpl to
# ${DEPLOY_SKYGATE_DIR}/caddy/Caddyfile. Skipped entirely
# when CADDY_ENABLED=false. The docker-compose.yml above
# also references the caddy-data and caddy-config volumes;
# they exist regardless of CADDY_ENABLED (compose refuses
# to start a service whose volume isn't declared).
if [ "${CADDY_ENABLED:-true}" = "true" ]; then
    mkdir -p "${DEPLOY_SKYGATE_DIR}/caddy"
    # 2026-07-15: v0.15.0 — the Caddy DNS-01 challenge
    # needs the API token at RUNTIME (the Caddyfile
    # references it as `env.CADDY_DNS_TOKEN_VALUE`).
    # We pass the token to the Caddy container via
    # `env_file:` (a Caddy-specific .env written to
    # /var/lib/skygate/caddy/caddy.env, mode 0600). The
    # operator's main .env (which IS often committed to
    # git) is not touched.
    #
    # If the operator's source token file doesn't exist
    # yet, we still render the Caddyfile and warn —
    # Caddy will fail to issue certs until the operator
    # creates the file, but the deploy itself succeeds
    # (this is the right behaviour: the operator may be
    # in the middle of provisioning the DNS provider
    # account and shouldn't have to wait for deploy).
    CADDY_ENV_FILE="${DEPLOY_SKYGATE_DIR}/caddy/caddy.env"
    : > "${CADDY_ENV_FILE}"  # truncate
    if [ -f "${CADDY_DNS_API_TOKEN_FILE}" ]; then
        echo "CADDY_DNS_TOKEN_VALUE=$(cat "${CADDY_DNS_API_TOKEN_FILE}")" >> "${CADDY_ENV_FILE}"
        log "Caddy DNS-01 token read from ${CADDY_DNS_API_TOKEN_FILE}"
    else
        warn "CADDY_DNS_API_TOKEN_FILE (${CADDY_DNS_API_TOKEN_FILE}) not found — Caddy will fail to issue certs until the operator creates it. See docs/https-setup.md for the token shape."
        echo "CADDY_DNS_TOKEN_VALUE=" >> "${CADDY_ENV_FILE}"
    fi
    xchmod 600 "${CADDY_ENV_FILE}"
    # Caddy DNS-01 module selector. "http" = HTTP-01
    # challenge (no token needed, port 80 must be
    # reachable). Anything else = the named provider
    # (token required).
    if [ "${CADDY_DNS_PROVIDER:-cloudflare}" = "http" ]; then
        export CADDY_TLS_DIRECTIVES="        # HTTP-01 challenge (port 80 must be reachable from the public Internet).
        # No DNS API token needed; Caddy writes nothing
        # to your DNS provider's records."
    else
        export CADDY_TLS_DIRECTIVES="        # DNS-01 challenge via ${CADDY_DNS_PROVIDER}.
        # The token is read from \$CADDY_DNS_TOKEN_VALUE
        # (loaded from /etc/caddy/caddy.env, mode 0600;
        # the rendered Caddyfile does not embed the token).
        dns ${CADDY_DNS_PROVIDER}"
    fi
    # DERP upstream. derper-compose.yml.tmpl uses
    # network_mode: host, so from the Caddy container's
    # perspective the derper is on the host's loopback.
    export CADDY_DERP_UPSTREAM="127.0.0.1:443"
    # Verify the public hostnames resolve before Caddy
    # tries to issue certs. Failing this check just
    # warns (the operator might be in the middle of
    # adding DNS records); Caddy will retry on the next
    # start.
    for h in "${CADDY_HOSTS_HEAD}" "${CADDY_HOSTS_HEADPLANE}" "${CADDY_HOSTS_DERP}"; do
        if [ -n "${h}" ] && ! getent hosts "${h}" >/dev/null 2>&1; then
            warn "Caddy vhost '${h}' doesn't resolve in this host's DNS. Caddy will fail to issue a cert until '${h}' points at this host's public IP."
        fi
    done
    render_template "${PROJECT_DIR}/deploy/templates/Caddyfile.tmpl"     "${DEPLOY_SKYGATE_DIR}/caddy/Caddyfile"
    log "Rendered Caddyfile with vhosts: HEAD=${CADDY_HOSTS_HEAD}, HEADPLANE=${CADDY_HOSTS_HEADPLANE}, DERP=${CADDY_HOSTS_DERP}"
else
    log "CADDY_ENABLED=false — skipping Caddyfile render (no TLS terminator; operator takes responsibility per docs/https-setup.md)"
fi

# 2026-07-14: Этап 14 v11 — Headplane is a documented optional
# module. When HEADPLANE_ENABLED=false, strip the headplane service
# block + its volume from the rendered compose file so the
# container is never started. The template still includes the
# block (so a future flip to true re-creates the container).
# See docs/headplane.md for the integration contract.
#
# 2026-07-15: v0.10.12 — when HEADPLANE_EXTERNAL_URL is set, also
# strip the sidecar (we point at the existing one). The two
# conditions are equivalent for the deploy step; the only
# difference is the backup manifest records the external URL
# so a restore on another host can reproduce the wiring.
if [ "${HEADPLANE_ENABLED}" = "false" ] || [ -n "${HEADPLANE_EXTERNAL_URL}" ]; then
    if [ -n "${HEADPLANE_EXTERNAL_URL}" ]; then
        log "HEADPLANE_EXTERNAL_URL is set — stripping Headplane sidecar (using existing ${HEADPLANE_EXTERNAL_URL})"
    else
        log "HEADPLANE_ENABLED=false — stripping Headplane from docker-compose.yml"
    fi
    # Delete the headplane service block (from "  headplane:" up
    # to the next top-level key). sed -n with `p` after a `/^  headplane:/,/^[^ ]/!p`
    # doesn't work portably, so use a Python one-liner.
    python3 - <<'PY'
import re, pathlib
p = pathlib.Path("/home/skyadmin/headscale/docker-compose.yml")
text = p.read_text()
# Drop the headplane service block.
text = re.sub(r"\n  headplane:.*?(?=\nvolumes:)", "", text, count=1, flags=re.S)
# Drop the headplane_data volume.
text = re.sub(r"\n  headplane_data:\n", "", text, count=1)
p.write_text(text)
PY
fi

# ═══════════════════════════════════════════════════════════════════════════
# STEP 3: Headplane configuration
# ═══════════════════════════════════════════════════════════════════════════
echo ""; echo "-- Step 3: Headplane configuration --"
if [ "${HEADPLANE_ENABLED}" = "false" ]; then
    log "  skipped (HEADPLANE_ENABLED=false)"
else
    cp "${PROJECT_DIR}/deploy/templates/headplane-config.yaml"    "${DEPLOY_HEADSCALE_DIR}/headplane/config.yaml"
    log "Copied headplane config"
fi

# ═══════════════════════════════════════════════════════════════════════════
# STEP 4: Start Headscale + Headplane
# ═══════════════════════════════════════════════════════════════════════════
echo ""; echo "-- Step 4: Starting Headscale + Headplane --"
cd "${DEPLOY_HEADSCALE_DIR}"

if [ "${MODE}" = "restore" ]; then
    HS_DB="${FROM_PATH}/headscale-db.sqlite"
    if [ -f "${HS_DB}" ]; then
        ${DOCKER_CMD} volume create headscale_headscale_data 2>/dev/null || true
        volume_copy_in headscale_headscale_data "${HS_DB}" db.sqlite; fi
    if [ "${HEADPLANE_ENABLED}" != "false" ] && [ -d "${FROM_PATH}/headplane-data" ] && [ "$(ls -A "${FROM_PATH}/headplane-data" 2>/dev/null)" ]; then
        ${DOCKER_CMD} volume create headscale_headplane_data 2>/dev/null || true
        volume_copy_dir headscale_headplane_data "${FROM_PATH}/headplane-data"; fi
fi

${DOCKER_CMD} compose up -d 2>&1 || warn "docker compose up had warnings"
wait_for_http "http://localhost:50444/api/v1/node" 200 60
container_stable headscale 10
if [ "${HEADPLANE_ENABLED}" = "false" ] || [ -n "${HEADPLANE_EXTERNAL_URL}" ]; then
    if [ -n "${HEADPLANE_EXTERNAL_URL}" ]; then
        log "  skipped headplane readiness check (using HEADPLANE_EXTERNAL_URL=${HEADPLANE_EXTERNAL_URL})"
    else
        log "  skipped headplane readiness check (HEADPLANE_ENABLED=false)"
    fi
else
    wait_for_http "http://localhost:50445/admin/" "2xx" 30
    container_stable headplane 5
fi

# ═══════════════════════════════════════════════════════════════════════════
# STEP 5: Skygate
# ═══════════════════════════════════════════════════════════════════════════
echo ""; echo "-- Step 5: Skygate --"
cd "${PROJECT_DIR}"

if [ "${MODE}" = "restore" ] && [ ! -d "${PROJECT_DIR}/.git" ]; then
    BUNDLE="${FROM_PATH}/skygate-repo.bundle"
    if [ -f "${BUNDLE}" ]; then
        log "Restoring Skygate source from git bundle..."
        git clone "${BUNDLE}" "${PROJECT_DIR}" 2>/dev/null || {
            warn "Git clone failed — manual extraction"
            cd "${PROJECT_DIR}" && git init && git fetch "${BUNDLE}" && git checkout -b main FETCH_HEAD 2>/dev/null ||                 warn "Manual restore also failed — source may be incomplete"; }
        log "Source code restored"; fi
fi

if [ "${MODE}" = "restore" ] && [ -f "${FROM_PATH}/.env" ]; then
    cp "${FROM_PATH}/.env" "${PROJECT_DIR}/.env"
    xchmod 600 "${PROJECT_DIR}/.env"
    log "Restored .env"; fi

if [ "${MODE}" = "restore" ] && [ -d "${FROM_PATH}/ssh" ]; then
    cp "${FROM_PATH}/ssh/"* "${SSH_DIR}/" 2>/dev/null || true
    xchmod 600 "${SSH_DIR}/skygate_sync" 2>/dev/null || true
    log "Restored SSH keys"; fi

if [ "${MODE}" = "restore" ]; then
    SG_DB="${FROM_PATH}/skygate.db"
    if [ -f "${SG_DB}" ]; then
        ${DOCKER_CMD} volume create skygate-data 2>/dev/null || true
        volume_copy_in skygate-data "${SG_DB}" skygate.db; fi
fi

${DOCKER_CMD} compose build 2>&1 | tail -3 || warn "docker compose build had warnings"
${DOCKER_CMD} compose up -d 2>&1 || warn "docker compose up had warnings"
wait_for_http "http://localhost:${SKYGATE_PORT}/login" 200 120
container_stable skygate 20

# ═══════════════════════════════════════════════════════════════════════════
# STEP 6: DERP (if enabled)
# ═══════════════════════════════════════════════════════════════════════════
if [ "${DERP_ENABLED}" = "true" ]; then
    echo ""; echo "-- Step 6: DERP Relay --"
    render_template "${PROJECT_DIR}/deploy/templates/derper-compose.yml.tmpl"         "${DEPLOY_HEADSCALE_DIR}/derper-compose.yml"
    mkdir -p /var/lib/derper/certs 2>/dev/null || mkdir -p "${DEPLOY_HEADSCALE_DIR}/derper-certs" 2>/dev/null || true

    if [ ! -f "${DEPLOY_HEADSCALE_DIR}/derpmap.json" ]; then
        cat > "${DEPLOY_HEADSCALE_DIR}/derpmap.json" << DERPEOF
{"Regions":{"900":{"RegionID":900,"RegionCode":"custom","RegionName":"Skygate DERP","Nodes":[{"Name":"1","RegionID":900,"HostName":"${DERP_HOSTNAME}","DERPPort":443,"STUNPort":${DERP_STUN_PORT},"STUNOnly":false}]}}}
DERPEOF
        log "Generated derpmap.json"; fi

    if [ "${MODE}" = "restore" ] && [ -f "${FROM_PATH}/derper.conf" ]; then
        cp "${FROM_PATH}/derper.conf" /var/lib/derper/derper.conf 2>/dev/null || cp "${FROM_PATH}/derper.conf" "${DEPLOY_HEADSCALE_DIR}/derper.conf" 2>/dev/null
        log "Restored derper.conf"
    elif [ ! -f /var/lib/derper/derper.conf ] && [ ! -f "${DEPLOY_HEADSCALE_DIR}/derper.conf" ]; then
        echo "{"PrivateKey":"privkey:${DERP_PRIVATE_KEY}"}" > "${DEPLOY_HEADSCALE_DIR}/derper.conf"
        log "Generated derper.conf"; fi

    [ "${MODE}" = "restore" ] && [ -f "${FROM_PATH}/derpmap.json" ] && cp "${FROM_PATH}/derpmap.json" "${DEPLOY_HEADSCALE_DIR}/derpmap.json"
    cd "${DEPLOY_HEADSCALE_DIR}"
    ${DOCKER_CMD} compose -f derper-compose.yml up -d 2>/dev/null || warn "DERP start failed"
    log "DERP relay started"; fi

# ═══════════════════════════════════════════════════════════════════════════
# Done
# ═══════════════════════════════════════════════════════════════════════════
echo ""; echo "=============================================="
echo "  Deploy complete!  (OS: ${SKYGATE_OS})"
echo "=============================================="
echo ""
echo "Services:"
echo "  Headscale API:  http://localhost:50444"
echo "  Headplane UI:   http://localhost:50445/admin/"
echo "  Skygate:        http://localhost:${SKYGATE_PORT}/login"
[ "${DERP_ENABLED}" = "true" ] && echo "  DERP relay:     https://${DERP_HOSTNAME}"
echo ""
echo "Next: ./deploy/validate.sh"

[ -x "${SCRIPT_DIR}/validate.sh" ] && { echo "Running validation..."; bash "${SCRIPT_DIR}/validate.sh"; }
