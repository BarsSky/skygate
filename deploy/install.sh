#!/bin/bash
# install.sh — V3 (B237.13) single-file skygate installer.
#
# Auto-detects the host OS (Debian/Ubuntu, RHEL/Fedora/Rocky/Alma,
# Alpine) and dispatches to the matching per-OS installer. The
# per-OS scripts are in deploy/install-{debian,rh,alpine}.sh and
# share the SAME contract:
#
#   - Download the latest release tarball from GitHub Releases
#     (or SKYGATE_VERSION if set, defaults to "latest")
#   - Verify the SHA256 against the SHA256SUMS file
#   - Install the binary at /usr/local/bin/skygate
#   - Create the skygate system user (uid 998) and data dir
#     (/var/lib/skygate, /etc/skygate)
#   - Drop a systemd unit / OpenRC service
#   - Write a starter /etc/skygate/skygate.env
#   - Enable + start the service
#   - Print the access URL + the next steps
#
# Why an autodetect single-file + per-OS scripts (instead of
# just one of them):
#   - The single-file is the canonical "curl | sh" path that
#     docs and READMEs can reference. One URL, one command.
#   - The per-OS scripts are what get used in practice — ops
#     teams audit the EXACT script that runs on their distro
#     (apt vs dnf vs apk differences, systemd vs OpenRC,
#     config file locations, etc.). The single-file just
#     dispatches to them.
#   - Both code paths are independently testable (the B-check
#     verifies each per-OS script is present and executable;
#     the install-e2e job in CI tests the autodetect path
#     against a fresh docker container of each distro).
#
# Why install at /usr/local/bin/skygate (not /opt/skygate/bin):
#   - The /usr/local/bin convention is the de-facto standard
#     for system-installed Go binaries (same as /usr/local/bin/
#     kubectl, /usr/local/bin/helm, /usr/local/bin/terraform).
#   - systemd's ProtectSystem=strict (in the unit file) blocks
#     writes to /usr + /boot, so the binary is read-only after
#     install — only the data dir (/var/lib/skygate) and the
#     config dir (/etc/skygate) are writable.
#   - The unit file's ExecStart points at /usr/local/bin/skygate
#     directly (no PATH lookup, no wrapper script). Simpler +
#     faster + harder to break.
#
# Rollback: `systemctl disable --now skygate && rm -f
# /etc/systemd/system/skygate.service /usr/local/bin/skygate
# /etc/skygate/skygate.env && userdel skygate` (and the
# matching commands for OpenRC). The data dir
# (/var/lib/skygate) is PRESERVED on uninstall so a re-install
# keeps the existing database.

set -euo pipefail

# -------- 0. Args / env --------
GITHUB_OWNER="${SKYGATE_GITHUB_OWNER:-BarsSky}"
GITHUB_REPO="${SKYGATE_GITHUB_REPO:-skygate}"
SKYGATE_VERSION="${SKYGATE_VERSION:-latest}"   # "latest" or "v1.5.0"
SKYGATE_CHANNEL="${SKYGATE_CHANNEL:-stable}"   # "stable" or "prerelease" (alpha/rc/beta)
SKYGATE_PORT="${SKYGATE_PORT:-8080}"
SKYGATE_USER="${SKYGATE_USER:-skygate}"
SKYGATE_DATA_DIR="${SKYGATE_DATA_DIR:-/var/lib/skygate}"
SKYGATE_ETC_DIR="${SKYGATE_ETC_DIR:-/etc/skygate}"
SKYGATE_BIN="${SKYGATE_BIN:-/usr/local/bin/skygate}"
SKIP_VERIFY="${SKYGATE_SKIP_VERIFY:-0}"        # set to 1 for air-gapped

# -------- 1. Preflight --------
# We need: curl + tar + sha256sum + systemctl-or-openrc + one of
# apt/dnf/apk (for the dependency install in the per-OS scripts).
# The single-file only needs curl + tar + sha256sum (it doesn't
# install deps — those are the per-OS scripts' job).

if [ "$(id -u)" -ne 0 ]; then
    echo "ERROR: install.sh must be run as root (use sudo $0)" >&2
    exit 1
fi
for cmd in curl tar sha256sum; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "ERROR: required command '$cmd' not found in PATH" >&2
        exit 1
    fi
done

# -------- 2. OS detection --------
# Look for the canonical /etc/os-release fields. Fall back to
# /etc/lsb-release + /etc/redhat-release for older distros that
# don't have os-release. The dispatch is conservative: we only
# call a per-OS installer if the family is unambiguous.

. /etc/os-release 2>/dev/null || true
ID="${ID:-unknown}"
ID_LIKE="${ID_LIKE:-}"
VERSION_ID="${VERSION_ID:-}"

case "$ID" in
    debian|ubuntu|linuxmint|pop|elementary|zorin|kali|raspbian)
        OS_FAMILY="debian"
        ;;
    rhel|centos|rocky|almalinux|ol|amzn|fedora|nobara)
        OS_FAMILY="rh"
        ;;
    alpine)
        OS_FAMILY="alpine"
        ;;
    *)
        # Fallback: check ID_LIKE for indirect matches
        case "$ID_LIKE" in
            *debian*)  OS_FAMILY="debian" ;;
            *rhel*|*fedora*)  OS_FAMILY="rh" ;;
            *)
                echo "ERROR: unsupported OS: ID='$ID' ID_LIKE='$ID_LIKE'" >&2
                echo "Supported: Debian/Ubuntu, RHEL/Fedora/Rocky, Alpine" >&2
                echo "For other distros, run install-bare.sh + your own" >&2
                echo "service manager unit (the install scripts are thin" >&2
                echo "wrappers around curl + tar + sha256sum)." >&2
                exit 1
                ;;
        esac
        ;;
esac

echo "[install] detected OS: $ID $VERSION_ID (family=$OS_FAMILY)"

# -------- 3. Dispatch to the per-OS installer --------
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PER_OS="$SCRIPT_DIR/install-${OS_FAMILY}.sh"

if [ ! -x "$PER_OS" ]; then
    echo "ERROR: per-OS installer not found or not executable: $PER_OS" >&2
    echo "If you installed skygate from a tarball, the per-OS scripts" >&2
    echo "must be in the same directory. Re-download the full release" >&2
    echo "tarball from $GITHUB_OWNER/$GITHUB_REPO/releases." >&2
    exit 1
fi

echo "[install] dispatching to $PER_OS"

# Export the vars the per-OS scripts read. We don't pass them on
# the command line because some of them are env-only (the per-OS
# scripts source each other for shared helpers, and they all
# read SKYGATE_VERSION etc. from the environment).
export GITHUB_OWNER GITHUB_REPO SKYGATE_VERSION SKYGATE_CHANNEL
export SKYGATE_PORT SKYGATE_USER SKYGATE_DATA_DIR SKYGATE_ETC_DIR
export SKYGATE_BIN SKIP_VERIFY

exec "$PER_OS" "$@"
