#!/usr/bin/env bash
#===============================================================================
# docker.sh — Docker helpers for Skygate deploy system
#===============================================================================
set -euo pipefail

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

log()    { echo -e "${GREEN}[OK]${NC} $1"; }
warn()   { echo -e "${YELLOW}[!!]${NC} $1"; }
err()    { echo -e "${RED}[FAIL]${NC} $1"; exit 1; }

# ensure_network — create Docker network if it doesn't exist
ensure_network() {
    local net="${1:-headscale_default}"
    local subnet="${2:-172.18.0.0/16}"

    if docker network inspect "${net}" >/dev/null 2>&1; then
        log "Docker network '${net}' already exists"
        return 0
    fi

    log "Creating Docker network '${net}' (${subnet})..."
    docker network create "${net}"         --driver bridge         --subnet "${subnet}"         2>/dev/null || {
        # Subnet might conflict — try without explicit subnet
        warn "Subnet ${subnet} unavailable, creating without explicit subnet..."
        docker network create "${net}" --driver bridge ||             err "Failed to create network '${net}'"
    }
    log "Network '${net}' created"
}

# volume_copy_in — copy file from host into a Docker volume
volume_copy_in() {
    local volume="$1"
    local src="$2"
    local dest="${3:-$(basename "${src}")}"

    docker run --rm         -v "${volume}:/target"         -v "$(dirname "$(realpath "${src}")"):/host"         alpine sh -c "cp /host/$(basename "${src}") /target/${dest}" &&         log "Copied $(basename "${src}") → volume ${volume}" ||         warn "Failed to copy $(basename "${src}") → volume ${volume}"
}

# volume_copy_dir — copy directory contents into a Docker volume
volume_copy_dir() {
    local volume="$1"
    local src_dir="$2"

    docker run --rm         -v "${volume}:/target"         -v "$(realpath "${src_dir}"):/host"         alpine sh -c "cp -r /host/* /target/" 2>/dev/null &&         log "Copied dir ${src_dir} → volume ${volume}" ||         warn "Failed to copy dir → volume ${volume}"
}

# wait_for_http — poll until endpoint returns expected HTTP code
wait_for_http() {
    local url="$1"
    local expected="${2:-200}"
    local timeout="${3:-60}"
    local interval="${4:-2}"

    local elapsed=0
    while [ "${elapsed}" -lt "${timeout}" ]; do
        local code
        code=$(curl -s -o /dev/null -w "%{http_code}" --max-time 3 "${url}" 2>/dev/null || echo "000")
        if [ "${code}" = "${expected}" ] || { [ "${expected}" = "2xx" ] && [[ "${code}" =~ ^2 ]]; }; then
            log "${url} → HTTP ${code} (ready in ${elapsed}s)"
            return 0
        fi
        if [ "${code}" != "000" ] && [ "${expected}" != "2xx" ]; then
            # Got a response but wrong code — still progress
            warn "${url} → HTTP ${code} (expected ${expected}), waiting..."
        fi
        sleep "${interval}"
        elapsed=$((elapsed + interval))
    done
    err "${url} did not respond with ${expected} within ${timeout}s"
}

# container_stable — verify container is running and not in restart loop
container_stable() {
    local ctr="$1"
    local min_uptime="${2:-10}"

    local status running started_at
    status=$(docker inspect "${ctr}" --format='{{.State.Status}}' 2>/dev/null || echo "missing")
    running=$(docker inspect "${ctr}" --format='{{.State.Running}}' 2>/dev/null || echo "false")

    if [ "${status}" != "running" ] || [ "${running}" != "true" ]; then
        warn "${ctr}: status=${status} running=${running}"
        return 1
    fi

    # Check uptime > min_uptime seconds
    started_at=$(docker inspect "${ctr}" --format='{{.State.StartedAt}}' 2>/dev/null)
    local started_ts
    started_ts=$(date -d "${started_at}" +%s 2>/dev/null || date -jf "%Y-%m-%dT%H:%M:%S" "${started_at%%Z*}" +%s 2>/dev/null || echo "0")
    local now_ts
    now_ts=$(date +%s)
    local uptime=$((now_ts - started_ts))

    if [ "${uptime}" -lt "${min_uptime}" ]; then
        warn "${ctr}: uptime ${uptime}s < ${min_uptime}s (possible restart loop)"
        return 1
    fi

    log "${ctr}: stable (uptime ${uptime}s)"
    return 0
}

# sqlite_checkpoint — run WAL checkpoint on a SQLite DB in a Docker volume
sqlite_checkpoint() {
    local volume="$1"
    local db_path="${2:-db.sqlite}"

    docker run --rm         -v "${volume}:/data"         alpine sh -c "apk add --no-cache sqlite >/dev/null 2>&1 && sqlite3 /data/${db_path} 'PRAGMA wal_checkpoint(TRUNCATE)'" 2>/dev/null &&         log "WAL checkpoint: ${volume}/${db_path}" ||         warn "WAL checkpoint failed for ${volume}/${db_path} (may be running)"
}
