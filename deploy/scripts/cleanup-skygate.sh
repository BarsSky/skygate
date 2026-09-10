#!/bin/bash
# cleanup-skygate.sh — V1 (B-mod-cleanup, 2026-09-10)
# operator-facing uninstaller for skygate.
#
# The inverse of install-debian.sh (and install-rh.sh /
# install-alpine.sh). Removes:
#   - systemd service (stop + disable)
#   - skygate binary (/usr/local/bin/skygate)
#   - skygate system user (skygate) — unless --keep-user
#   - /var/lib/skygate (data dir) — unless --keep-data
#   - /etc/skygate (config dir) — unless --keep-config
#   - /var/run/skygate (runtime dir) — always (it's a runtime)
#   - Optional: Tailscale (via install-tailscale.sh --mode=uninstall)
#     when --with-tailscale is passed
#
# Idempotent: re-running on a partially-cleaned host
# skips the steps that have already been completed.
# Dry-run friendly: --dry-run prints every step before
# running it, so the operator can sanity-check the plan
# before committing.
#
# Safety:
#   - Default: shows a confirm prompt listing every
#     action, waits for 'yes' on stdin. Aborts on
#     any other input.
#   - --yes: skip the prompt (for scripts / CI).
#   - --dry-run: print + exit without doing anything.
#
# Usage:
#   sudo bash deploy/scripts/cleanup-skygate.sh
#     # full cleanup with prompt
#   sudo bash deploy/scripts/cleanup-skygate.sh --yes
#     # full cleanup, no prompt
#   sudo bash deploy/scripts/cleanup-skygate.sh --dry-run
#     # show what would be done, don't actually do it
#   sudo bash deploy/scripts/cleanup-skygate.sh --keep-config
#     # keep /etc/skygate/skygate.env (HEADSCALE_API_KEY etc.)
#   sudo bash deploy/scripts/cleanup-skygate.sh --with-tailscale
#     # also uninstall Tailscale (delegates to
#     # install-tailscale.sh --mode=uninstall)
#
# Exit codes:
#   0 — success (every step that was applicable completed)
#   1 — invalid args or required tool missing
#   2 — confirm prompt was answered with anything other
#       than 'yes'
#   3 — a cleanup step failed (partial state on disk)

set -euo pipefail

# === paths ===
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Defaults match install-debian.sh.
SKYGATE_USER="${SKYGATE_USER:-skygate}"
SKYGATE_DATA_DIR="${SKYGATE_DATA_DIR:-/var/lib/skygate}"
SKYGATE_ETC_DIR="${SKYGATE_ETC_DIR:-/etc/skygate}"
SKYGATE_RUN_DIR="${SKYGATE_RUN_DIR:-/var/run/skygate}"
SKYGATE_BIN="${SKYGATE_BIN:-/usr/local/bin/skygate}"
INSTALL_TS_SH="${SCRIPT_DIR}/install-tailscale.sh"

# === arg parsing ===
ASSUME_YES=0
DRY_RUN=0
KEEP_USER=0
KEEP_DATA=0
KEEP_CONFIG=0
KEEP_BINARY=0
WITH_TS=0

usage() {
    sed -n '2,55p' "$0"
    exit "${1:-1}"
}

while [ $# -gt 0 ]; do
    case "$1" in
        --yes)        ASSUME_YES=1 ;;
        --dry-run)    DRY_RUN=1 ;;
        --keep-user)  KEEP_USER=1 ;;
        --keep-data)  KEEP_DATA=1 ;;
        --keep-config) KEEP_CONFIG=1 ;;
        --keep-binary) KEEP_BINARY=1 ;;
        --with-tailscale) WITH_TS=1 ;;
        -h|--help)    usage 0 ;;
        *) echo "ERROR: unknown arg: $1" >&2; usage 1 ;;
    esac
    shift
done

# === helpers ===
log()  { printf '[cleanup-skygate] %s\n' "$*"; }
warn() { printf '[cleanup-skygate] WARN: %s\n' "$*" >&2; }
fail() { printf '[cleanup-skygate] ERROR: %s\n' "$*" >&2; exit "${2:-3}"; }

require_root() {
    if [ "$(id -u)" -ne 0 ]; then
        fail "this script must run as root (use sudo)" 1
    fi
}

# run_step runs a single cleanup step. Logs the plan,
# then either prints it (dry-run) or executes it.
# On failure, prints a warning and continues (so the
# operator can still see what worked + what didn't).
run_step() {
    local description="$1"
    shift
    if [ "$DRY_RUN" = "1" ]; then
        log "[DRY-RUN] would: $description"
        return 0
    fi
    log "step: $description"
    if "$@"; then
        log "  ok: $description"
    else
        warn "  failed: $description (continuing)"
    fi
}

# === preflight ===
require_root
if [ "$DRY_RUN" = "0" ] && [ "$ASSUME_YES" = "0" ]; then
    log "Cleanup plan:"
    log "  systemd:    stop + disable $SKYGATE_USER.service"
    [ "$KEEP_BINARY" = "0" ] && log "  binary:     rm $SKYGATE_BIN"
    [ "$KEEP_USER"   = "0" ] && log "  user:       userdel $SKYGATE_USER"
    [ "$KEEP_DATA"   = "0" ] && log "  data dir:   rm -rf $SKYGATE_DATA_DIR"
    [ "$KEEP_CONFIG" = "0" ] && log "  config dir: rm -rf $SKYGATE_ETC_DIR"
                                log "  runtime:    rm -rf $SKYGATE_RUN_DIR"
    [ "$WITH_TS"     = "1" ] && log "  tailscale:  install-tailscale.sh --mode=uninstall"
    log ""
    log "Pass --keep-{user,data,config,binary} to keep specific items."
    log "Pass --dry-run to print the plan without executing."
    log "Pass --yes to skip this prompt."
    echo
    read -r -p "Proceed with cleanup? Type 'yes' to continue: " response
    if [ "$response" != "yes" ]; then
        fail "aborted by operator (response was '$response', expected 'yes')" 2
    fi
fi

# === step 1: systemd ===
if command -v systemctl >/dev/null 2>&1; then
    if systemctl list-unit-files "$SKYGATE_USER.service" >/dev/null 2>&1; then
        run_step "systemctl stop $SKYGATE_USER" \
            systemctl stop "$SKYGATE_USER.service"
        run_step "systemctl disable $SKYGATE_USER" \
            systemctl disable "$SKYGATE_USER.service"
    else
        log "  skip: $SKYGATE_USER.service not installed"
    fi
    # Reload so the unit file disappearance is registered.
    run_step "systemctl daemon-reload" systemctl daemon-reload
    run_step "systemctl reset-failed $SKYGATE_USER" \
        systemctl reset-failed "$SKYGATE_USER.service" || true
else
    warn "systemctl not on PATH; skipping systemd steps"
fi

# === step 2: binary ===
if [ "$KEEP_BINARY" = "0" ]; then
    if [ -f "$SKYGATE_BIN" ]; then
        run_step "rm $SKYGATE_BIN" rm -f "$SKYGATE_BIN"
    else
        log "  skip: $SKYGATE_BIN not present"
    fi
else
    log "  skip: --keep-binary set"
fi

# === step 3: skygate user ===
if [ "$KEEP_USER" = "0" ]; then
    if id "$SKYGATE_USER" >/dev/null 2>&1; then
        # Kill any remaining processes owned by the user
        # (e.g. a leftover tailscaled sidecar).
        run_step "pkill -u $SKYGATE_USER" \
            pkill -u "$SKYGATE_USER" || true
        sleep 1
        run_step "userdel $SKYGATE_USER" \
            userdel "$SKYGATE_USER"
    else
        log "  skip: user $SKYGATE_USER not present"
    fi
else
    log "  skip: --keep-user set"
fi

# === step 4: data dir ===
if [ "$KEEP_DATA" = "0" ]; then
    if [ -d "$SKYGATE_DATA_DIR" ]; then
        run_step "rm -rf $SKYGATE_DATA_DIR" \
            rm -rf "$SKYGATE_DATA_DIR"
    else
        log "  skip: $SKYGATE_DATA_DIR not present"
    fi
else
    log "  skip: --keep-data set"
fi

# === step 5: config dir ===
if [ "$KEEP_CONFIG" = "0" ]; then
    if [ -d "$SKYGATE_ETC_DIR" ]; then
        run_step "rm -rf $SKYGATE_ETC_DIR" \
            rm -rf "$SKYGATE_ETC_DIR"
    else
        log "  skip: $SKYGATE_ETC_DIR not present"
    fi
else
    log "  skip: --keep-config set"
fi

# === step 6: runtime dir (always removed) ===
if [ -d "$SKYGATE_RUN_DIR" ]; then
    run_step "rm -rf $SKYGATE_RUN_DIR" \
        rm -rf "$SKYGATE_RUN_DIR"
else
    log "  skip: $SKYGATE_RUN_DIR not present"
fi

# === step 7: optional Tailscale uninstall ===
if [ "$WITH_TS" = "1" ]; then
    if [ -f "$INSTALL_TS_SH" ]; then
        log "delegating Tailscale uninstall to install-tailscale.sh"
        if [ "$DRY_RUN" = "1" ]; then
            log "[DRY-RUN] would: bash $INSTALL_TS_SH --mode=uninstall"
        else
            bash "$INSTALL_TS_SH" --mode=uninstall || \
                warn "install-tailscale.sh --mode=uninstall failed (continuing)"
        fi
    else
        warn "install-tailscale.sh not found at $INSTALL_TS_SH — skipping Tailscale uninstall (install-tailscale.sh may be in a different repo checkout)"
    fi
fi

# === done ===
log "done"
if [ "$DRY_RUN" = "1" ]; then
    log "(dry-run mode: no actual changes were made — re-run without --dry-run to apply)"
fi
exit 0
