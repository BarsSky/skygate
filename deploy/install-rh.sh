#!/bin/bash
# install-rh.sh — V4 (B237.13) per-OS installer for
# RHEL / CentOS / Rocky / Alma / Oracle Linux / Amazon Linux /
# Fedora / Nobara.
#
# Idempotent + standalone (see install-debian.sh header for the
# full contract). The only differences from install-debian.sh:
#   - dnf instead of apt-get (RHEL 8+/Fedora 22+)
#   - RHEL uses shadow-utils for useradd (same flags as Debian
#     for --system, --no-create-home, --shell)
#   - RHEL's firewall (firewalld) doesn't block the skygate port
#     by default IF you open it explicitly. We don't open it
#     automatically (the operator usually fronts skygate with
#     nginx/caddy/NPM, not the raw 8080 port), but we print a
#     one-liner they can run if they do want to expose it.
#
# SELinux: we don't disable it. skygate binds TCP 8080, which
# is allowed by the default SELinux policy (http_port_t includes
# 8080 on RHEL 8+; on older RHEL 7 you may need
# `semanage port -a -t http_port_t -p tcp 8080`). The one-liner
# is in print_next_steps.

set -euo pipefail

. "$(dirname "$0")/install-common.sh"

. /etc/os-release
case "$ID" in
    rhel|centos|rocky|almalinux|ol|amzn|fedora|nobara) ;;
    *)
        echo "ERROR: install-rh.sh called on non-RHEL system (ID='$ID')" >&2
        echo "Run install.sh instead — it auto-dispatches to the right per-OS installer." >&2
        exit 1
        ;;
esac

echo "[install-rh] starting on $ID $VERSION_ID"

# -------- 1. dnf deps --------
# dnf is the package manager on RHEL 8+ / Fedora 22+. RHEL 7
# uses yum — we don't support RHEL 7 because:
#   - Go 1.25 (skygate's runtime requirement) needs a libc that
#     RHEL 7 doesn't have
#   - EOL was June 2024
#   - The skygate prebuilt binary is statically linked, so it
#     WOULD run on RHEL 7, but the install script needs dnf
# If you're on RHEL 7, use install-bare.sh + a manual systemd
# unit (it's 10 lines of YAML).
echo "[install-rh] installing dnf dependencies"
dnf install -y --setopt=install_weak_deps=False \
    ca-certificates \
    curl \
    tar \
    shadow-utils \
    openssh-clients \
    systemd \
    >/dev/null 2>&1

if [ ! -d /run/systemd/system ]; then
    echo "ERROR: systemd is not PID 1 on this system." >&2
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

# Extra RHEL-specific note about firewalld + SELinux
cat <<EOF

RHEL / Fedora notes:
  - If you want to expose port $SKYGATE_PORT through firewalld
    (uncommon; usually you front skygate with nginx/caddy):
      sudo firewall-cmd --permanent --add-port=$SKYGATE_PORT/tcp
      sudo firewall-cmd --reload
  - If you get "Permission denied" binding 0.0.0.0:$SKYGATE_PORT,
    SELinux may be blocking it. On RHEL 8+ port 8080 is allowed
    by default; on older systems you may need:
      sudo semanage port -a -t http_port_t -p tcp $SKYGATE_PORT
  - firewalld logs:  journalctl -u firewalld
  - SELinux denials:  sudo ausearch -m avc -ts recent

EOF
