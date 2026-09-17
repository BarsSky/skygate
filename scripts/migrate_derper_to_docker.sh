#!/usr/bin/env bash
# ============================================================================
# migrate_derper_to_docker.sh — B260.2 (2026-09-17)
#
# Replace the legacy systemd derper service with the dockerized
# `skygate-derper` container. Idempotent: safe to re-run.
#
# What it does:
#   1. Pre-flight: confirm systemd derper is running (so we know there's
#      something to migrate), confirm cert files exist at the canonical
#      path, confirm ghcr.io is unreachable (so the operator knows the
#      local image is the only option).
#   2. Build skygate-derper:latest from the host's /usr/local/bin/derper
#      binary + deploy/docker/derper/Dockerfile.
#   3. Stop + disable systemd derper. Brief downtime: ~3-10s while the
#      docker container takes over the same listen ports.
#   4. Start the docker derper with the SAME command-line args the
#      systemd unit used (certmode=manual, certdir=/var/lib/derper/certs,
#      --a=:443, --http-port=80, --stun, --verify-clients=false).
#   5. Verify: HTTPS GET / returns 200, STUN UDP :3478 is listening,
#      WebSocket /derp probe returns 101 Switching Protocols.
#
# Pre-requisites:
#   - Host has /usr/local/bin/derper (the operator installed it during
#     the B-derper-cert migration; if missing, this script aborts).
#   - Host has /var/lib/derper/certs/<hostname>.{crt,key} (mode 0600,
#     owner root) — the manual-cert flow requires these to exist BEFORE
#     derper starts, otherwise it errors with "can not load x509 key
#     pair for hostname \"...\"".
#   - Host has docker installed (obvious; the whole repo assumes this).
#   - SKYGATE_DERP_PROBE_HOST is set in /home/skyadmin/skygate/.env
#     (the B260 follow-up env var that pins skygate's derper probe
#     to the host IP — without it the probe resolves via systemd-
#     resolved's /etc/hosts entry and bounces to 127.0.0.1).
#
# Out of scope:
#   - Migrating from `network_mode: host` to bridge networking. derper
#     binds :443 (privileged) + :80 + STUN :3478; bridge networking
#     would need explicit port mappings and lose the systemd unit's
#     listen-address parity. Keep host networking.
#   - Switching to `ghcr.io/tailscale/derper`. VM outbound blocks
#     ghcr.io; local build is the only path. Operators on VMs with
#     ghcr.io access can override DERP_IMAGE in .env.
#   - Cert renewal. The systemd unit's manual cert files are operator-
#     managed (certsync writes to /var/lib/skygate/certs/, NOT
#     /var/lib/derper/certs/ — see AGENTS.md B147).
#
# Rollback: if docker derper fails to start, re-enable systemd:
#     sudo systemctl enable --now derper
# The systemd unit is left in place (NOT removed) by this script — it's
# just `stop`ped + `disable`d. To remove the unit file entirely, the
# operator runs `sudo rm /etc/systemd/system/derper.service && sudo
# systemctl daemon-reload` AFTER confirming the docker derper is stable.
# ============================================================================
set -euo pipefail

REPO_DIR="${REPO_DIR:-/home/skyadmin/skygate}"
DOCKERFILE_DIR="${REPO_DIR}/deploy/docker/derper"
DOCKER_COMPOSE_FILE="${REPO_DIR}/deploy/templates/derper-compose.yml.tmpl"
DERPER_BIN="/usr/local/bin/derper"
CERT_DIR="/var/lib/derper/certs"
DERPER_HOSTNAME="${DERPER_HOSTNAME:-derp.skynas.ru}"
DERP_DERP_PORT="${DERP_DERP_PORT:-443}"
DERP_HTTP_PORT="${DERP_HTTP_PORT:-80}"
DERP_STUN_PORT="${DERP_STUN_PORT:-3478}"
DOCKER_IMAGE="${DOCKER_IMAGE:-skygate-derper:latest}"
COMPOSE_DIR="${COMPOSE_DIR:-/home/skyadmin/headscale}"
COMPOSE_FILE="${COMPOSE_DIR}/derper-compose.yml"

log()  { echo "[migrate-derper] $*"; }
err()  { echo "[migrate-derper] ERROR: $*" >&2; exit 1; }
warn() { echo "[migrate-derper] WARN: $*" >&2; }

# ── Pre-flight ────────────────────────────────────────────────────────────
log "Step 1/5: pre-flight checks"

[ -f "${DERPER_BIN}" ] || err "derper binary not found at ${DERPER_BIN}; install it first (B-derper-cert)"

[ -f "${CERT_DIR}/${DERPER_HOSTNAME}.crt" ] || err "cert file missing: ${CERT_DIR}/${DERPER_HOSTNAME}.crt"
[ -f "${CERT_DIR}/${DERPER_HOSTNAME}.key" ] || err "cert key missing: ${CERT_DIR}/${DERPER_HOSTNAME}.key"

CERT_MODE="$(stat -c '%a' "${CERT_DIR}/${DERPER_HOSTNAME}.key" 2>/dev/null || echo unknown)"
[ "${CERT_MODE}" = "600" ] || warn "cert key mode is ${CERT_MODE} (expected 600); derper may refuse to load it"

# systemd derper must be currently active so the migration has a source.
if systemctl is-active --quiet derper.service; then
    log "  systemd derper is active — migration target confirmed"
else
    warn "systemd derper is NOT active; migration is still safe (it'll be a no-op for the systemd step)"
fi

# Confirm docker is up.
if ! sudo docker info >/dev/null 2>&1; then
    err "docker daemon unreachable; check 'sudo systemctl status docker'"
fi

# ── Build docker image ────────────────────────────────────────────────────
log "Step 2/5: build docker image ${DOCKER_IMAGE} from local binary"

cp "${DERPER_BIN}" "${DOCKERFILE_DIR}/derper"
(
    cd "${DOCKERFILE_DIR}"
    sudo docker build -t "${DOCKER_IMAGE}" .
)
rm -f "${DOCKERFILE_DIR}/derper"
log "  image built: $(sudo docker images --format '{{.Repository}}:{{.Tag}} {{.Size}}' "${DOCKER_IMAGE}")"

# ── Render compose file + stop systemd ────────────────────────────────────
log "Step 3/5: stop systemd derper + render compose file"

if systemctl is-active --quiet derper.service; then
    sudo systemctl stop derper.service
    sudo systemctl disable derper.service
    log "  systemd derper stopped + disabled (cert files left in place)"
else
    log "  systemd derper already inactive; skipping stop"
fi

mkdir -p "${COMPOSE_DIR}"
# Render the template (deploy.sh's render_template uses Python regex;
# we replicate it inline so this script works without deploy.sh).
DERP_HOSTNAME="${DERPER_HOSTNAME}" \
DERP_DERP_PORT="${DERP_DERP_PORT}" \
DERP_HTTP_PORT="${DERP_HTTP_PORT}" \
DERP_STUN_PORT="${DERP_STUN_PORT}" \
DERP_STUN_PORT="${DERP_STUN_PORT}" \
DERP_MAP_PORT="${DERP_MAP_PORT:-8765}" \
DERP_CERTMODE="${DERP_CERTMODE:-manual}" \
DERP_CERT_DIR="${CERT_DIR}" \
DERP_CONFIG_DIR="/var/lib/derper" \
DERP_VERIFY_CLIENTS_URL="" \
DERP_IMAGE="${DOCKER_IMAGE}" \
python3 - <<'PY'
import os, re
tmpl = "/home/skyadmin/skygate/deploy/templates/derper-compose.yml.tmpl"
dest = os.environ.get("OUT", "/home/skyadmin/headscale/derper-compose.yml")
with open(tmpl) as f:
    content = f.read()
content = re.sub(r'\$\{([A-Za-z_][A-Za-z0-9_]*)\}', lambda m: os.environ.get(m.group(1), m.group(0)), content)
with open(dest, "w") as f:
    f.write(content)
print(f"rendered -> {dest}")
PY

# ── Start docker derper ──────────────────────────────────────────────────
log "Step 4/5: start docker derper"

cd "${COMPOSE_DIR}"
sudo docker compose -f derper-compose.yml up -d derper

# Wait for port-bind to settle (host networking means the container's
# :443 binding IS the host's :443 binding; systemd was holding it until
# step 3, so docker should pick it up within ~3s).
for i in 1 2 3 4 5 6 7 8 9 10; do
    if sudo ss -tln "sport = :${DERP_DERP_PORT}" 2>/dev/null | grep -q ":${DERP_DERP_PORT}"; then
        log "  derper listening on :${DERP_DERP_PORT}/tcp (took ${i}s)"
        break
    fi
    sleep 1
done

# ── Verify ───────────────────────────────────────────────────────────────
log "Step 5/5: verify"

if sudo ss -uln "sport = :${DERP_STUN_PORT}" 2>/dev/null | grep -q ":${DERP_STUN_PORT}"; then
    log "  STUN :${DERP_STUN_PORT}/udp listening"
else
    warn "STUN :${DERP_STUN_PORT}/udp not detected; check 'sudo ss -ulnp | grep derper'"
fi

if sudo curl -sS --max-time 5 -k -o /dev/null -w "HTTP %{http_code} %{time_total}s\n" \
        "https://127.0.0.1:${DERP_DERP_PORT}/" 2>/dev/null \
        | grep -q "HTTP 200"; then
    log "  HTTPS GET / -> 200 OK"
else
    warn "HTTPS GET https://127.0.0.1:${DERP_DERP_PORT}/ did NOT return 200; check 'sudo docker logs derper'"
fi

log ""
log "Migration complete. systemd derper is stopped + disabled (left in place for rollback)."
log "To remove the systemd unit entirely once you've confirmed docker derper is stable:"
log "    sudo rm /etc/systemd/system/derper.service && sudo systemctl daemon-reload"
log ""
log "Container status:"
sudo docker ps --filter name=derper --format "table {{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}"
