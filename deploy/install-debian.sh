#!/bin/bash
# install-debian.sh — V4 (B237.13) per-OS installer for
# Debian / Ubuntu / Mint / Pop / Elementary / Kali / Raspbian.
#
# Idempotent: re-running with an existing install updates
# the binary but preserves /etc/skygate/skygate.env (operator's
# HEADSCALE_URL + HEADSCALE_API_KEY are NOT clobbered). To
# reset, delete /etc/skygate/skygate.env and re-run.
#
# Two ways to run this:
#   (a) Via install.sh (auto-detected): one URL + one command.
#       `curl -fsSL .../install.sh | sudo bash`
#   (b) Standalone: `sudo SKYGATE_VERSION=v1.5.0 bash
#       install-debian.sh` (useful for air-gapped + for
#       operators who want to audit the exact per-OS script
#       before running it).
#
# The hard work (downloading + verifying + extracting + writing
# the unit + enabling the service) is in install-common.sh.
# This script only does the Debian-specific bits: apt-get
# install + the right useradd flags + the right service name.
#
# Why no `set -e` on apt-get: a few of the apt-get packages
# (ca-certificates, curl, tar) are already installed on every
# modern Debian/Ubuntu. We use `|| true` defensively so a
# missing package doesn't fail the whole install.

set -euo pipefail

# SCRIPT_DIR / REPO_ROOT are used by step 7 (B-mod-install
# delegate to install-tailscale.sh). SCRIPT_DIR is the
# deploy/ dir, REPO_ROOT is the project root (one level
# above deploy/).
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

. "${SCRIPT_DIR}/install-common.sh"

# Sanity: only run on Debian-family
. /etc/os-release
case "$ID" in
    debian|ubuntu|linuxmint|pop|elementary|zorin|kali|raspbian) ;;
    *)
        echo "ERROR: install-debian.sh called on non-Debian system (ID='$ID')" >&2
        echo "Run install.sh instead — it auto-dispatches to the right per-OS installer." >&2
        exit 1
        ;;
esac

echo "[install-debian] starting on $ID $VERSION_ID"

# -------- 1. apt deps --------
# We need:
#   - curl + tar + ca-certificates: download + extract the tarball
#   - systemd: the service manager (all modern Debian/Ubuntu ship
#     systemd as the default init). If /run/systemd/system doesn't
#     exist, we fail loudly — this installer assumes systemd.
#   - passwd + adduser: for the useradd command (in passwd on
#     modern Debian/Ubuntu; the `adduser` package is a wrapper).
#   - openssh-client: NOT required for the systemd path
#     (only for B202.5 SSHDumpTransport in the docker path). But
#     the operator might use `ssh` to reach other tailnet peers
#     from this host, so install it.
echo "[install-debian] installing apt dependencies"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    tar \
    passwd \
    openssh-client \
    systemd \
    >/dev/null

# Verify systemd is the init (PID 1). If not, fail loudly —
# running skygate under sysvinit / openrc-on-debian is out
# of scope for this installer.
if [ ! -d /run/systemd/system ]; then
    echo "ERROR: systemd is not PID 1 on this system." >&2
    echo "This installer assumes systemd. For OpenRC-on-Debian" >&2
    echo "(rare), use install-bare.sh + your own service file." >&2
    exit 1
fi

# -------- 2. user + dirs --------
create_user_and_dirs "$SKYGATE_USER" "$SKYGATE_DATA_DIR" "$SKYGATE_ETC_DIR"

# -------- 3. download + verify + install binary --------
arch="$(detect_arch)"
triple="linux-${arch}"
mapfile -t urls < <(resolve_release_url "$SKYGATE_VERSION" "$triple")
tarball_url="${urls[0]}"
sums_url="${urls[1]}"
download_and_verify "$tarball_url" "$sums_url" "$(dirname "$SKYGATE_BIN")" "$SKIP_VERIFY"

# -------- 4. env file + systemd unit --------
write_env_file "$SKYGATE_ETC_DIR" "$SKYGATE_DATA_DIR" "$SKYGATE_PORT" "$SKYGATE_USER"
write_systemd_unit "$SKYGATE_USER" "$SKYGATE_DATA_DIR" "$SKYGATE_ETC_DIR"

# -------- 5. enable + start --------
enable_and_start_service "skygate"

# -------- 6. done --------
print_next_steps "$SKYGATE_PORT" "skygate"

# -------- 7. optional Tailscale install (B-mod-install, 2026-09-10) --------
# If the operator pre-set SKYGATE_TS_INSTALL_MODE (or
# SKYGATE_TS_AUTHKEY), the install-debian.sh path
# delegates to install-tailscale.sh for the Tailscale
# install. This is the "single command" flow operators
# want — `curl install.sh | sudo SKYGATE_TS_INSTALL_MODE=attach
# SKYGATE_TS_AUTHKEY=tskey-auth-XXX bash`.
#
# The script is opt-in: if no SKYGATE_TS_* env vars are
# set, this step is a no-op (the operator can run
# install-tailscale.sh manually later).
if [ -n "${SKYGATE_TS_INSTALL_MODE:-}" ] || [ -n "${SKYGATE_TS_AUTHKEY:-}" ]; then
    if [ -f "${REPO_ROOT}/deploy/scripts/install-tailscale.sh" ]; then
        echo "[install-debian] B-mod-install: delegating to install-tailscale.sh (mode=${SKYGATE_TS_INSTALL_MODE:-unset})"
        # Build the args from env. AUTHKEY is passed
        # only if set; LOGIN_SERVER from /etc/skygate/skygate.env
        # if present (it has the right value post-install).
        ts_args=(--mode="${SKYGATE_TS_INSTALL_MODE:-attach}")
        if [ -n "${SKYGATE_TS_AUTHKEY:-}" ]; then
            ts_args+=(--authkey="${SKYGATE_TS_AUTHKEY}")
        fi
        if [ -n "${SKYGATE_TS_LOGIN_SERVER:-}" ]; then
            ts_args+=(--login-server="${SKYGATE_TS_LOGIN_SERVER}")
        elif [ -f "${SKYGATE_ETC_DIR}/skygate.env" ]; then
            # Auto-pick from skygate.env (HEADSCALE_URL line).
            # Comment lines start with # — skip them.
            # Match `HEADSCALE_URL=` or `SKYGATE_HEADSCALE_URL=`.
            login_url=$(grep -E '^(SKYGATE_)?HEADSCALE_URL=' "${SKYGATE_ETC_DIR}/skygate.env" 2>/dev/null | tail -1 | sed -E 's/^[^=]+=//' || true)
            if [ -n "$login_url" ]; then
                ts_args+=(--login-server="$login_url")
            fi
        fi
        if [ -n "${SKYGATE_TS_HOSTNAME:-}" ]; then
            ts_args+=(--hostname="${SKYGATE_TS_HOSTNAME}")
        fi
        if [ -n "${SKYGATE_TS_CONTAINER_NAME:-}" ]; then
            ts_args+=(--container-name="${SKYGATE_TS_CONTAINER_NAME}")
        fi
        if ! bash "${REPO_ROOT}/deploy/scripts/install-tailscale.sh" "${ts_args[@]}"; then
            echo "[install-debian] WARN: install-tailscale.sh failed (continuing — skygate is up, operator can re-run install-tailscale.sh manually)" >&2
        fi
    else
        echo "[install-debian] WARN: SKYGATE_TS_* set but install-tailscale.sh not found at ${REPO_ROOT}/deploy/scripts/" >&2
    fi
fi
