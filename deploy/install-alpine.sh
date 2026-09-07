#!/bin/bash
# install-alpine.sh — V4 (B237.13) per-OS installer for Alpine Linux.
#
# Alpine is the odd one out:
#   - musl libc, not glibc (skygate's prebuilt static binary
#     doesn't care, but anything that links dynamically — like
#     the systemd unit's helper binaries — would)
#   - OpenRC, not systemd (the init system that ships with
#     Alpine by default; the skygate systemd unit doesn't apply
#     here, we have an OpenRC service script instead)
#   - BusyBox + apk (not bash + apt). The shebang on this
#     script is `#!/bin/bash` because we use bash arrays
#     (the common installer does `mapfile -t urls`), but the
#     script is otherwise portable.
#
# The shape of this script is the same as install-debian.sh /
# install-rh.sh: install deps → user + dirs → download →
# env file → service → enable + start. Only the dep install
# (apk instead of apt/dnf) and the service bits (OpenRC
# instead of systemd) differ.

set -euo pipefail

. "$(dirname "$0")/install-common.sh"

. /etc/os-release
case "$ID" in
    alpine) ;;
    *)
        echo "ERROR: install-alpine.sh called on non-Alpine system (ID='$ID')" >&2
        echo "Run install.sh instead — it auto-dispatches to the right per-OS installer." >&2
        exit 1
        ;;
esac

echo "[install-alpine] starting on $ID $VERSION_ID"

# -------- 1. apk deps --------
# We need:
#   - ca-certificates, curl, tar: download + extract
#   - bash: this script's shebang. Alpine's /bin/sh is busybox
#     ash, which doesn't support arrays (we need mapfile). bash
#     is small (~4 MB on Alpine) and a sensible default.
#   - openrc: the init system. Alpine ships it by default on
#     the "extended" image, but the "base" image doesn't.
#   - openssh-client-default: only needed if the operator wants
#     to use B202.5 SSHDumpTransport from the systemd-less
#     Alpine path. We install it for consistency with the
#     Debian/RH installers.
echo "[install-alpine] installing apk dependencies"
apk add --no-cache \
    ca-certificates \
    curl \
    tar \
    bash \
    openrc \
    openssh-client-default \
    >/dev/null

# OpenRC is the init on Alpine. If /run/openrc doesn't exist
# (e.g. the container was started with a different init), we
# fail loudly. This installer assumes OpenRC.
if [ ! -d /run/openrc ]; then
    echo "ERROR: OpenRC is not the init on this system." >&2
    echo "For systemd-on-Alpine (rare), use install-bare.sh +" >&2
    echo "your own service file." >&2
    exit 1
fi

# -------- 2. user + dirs --------
# Alpine's adduser is from busybox (no --system flag). We add
# the skygate user via adduser with -D (no password) + -H
# (no home) + -s /sbin/nologin (no shell). The uid is picked
# from the adduser range (1000-60000 by default on Alpine;
# we don't pin it because the system installer needs to
# coordinate uids with the host's other users).
SKYGATE_USER="${SKYGATE_USER:-skygate}"
if ! id "$SKYGATE_USER" >/dev/null 2>&1; then
    adduser -D -H -s /sbin/nologin -g "Skygate service account" "$SKYGATE_USER"
    echo "[install-alpine] created system user: $SKYGATE_USER"
else
    echo "[install-alpine] system user $SKYGATE_USER already exists"
fi

install -d -m 0750 -o "$SKYGATE_USER" -g "$SKYGATE_USER" "$SKYGATE_DATA_DIR"
install -d -m 0750 -o "$SKYGATE_USER" -g "$SKYGATE_USER" "$SKYGATE_ETC_DIR"
install -d -m 0750 -o "$SKYGATE_USER" -g "$SKYGATE_USER" "$SKYGATE_DATA_DIR/ts"
echo "[install-alpine] created dirs: $SKYGATE_DATA_DIR $SKYGATE_ETC_DIR"

# -------- 3. download + verify + install binary --------
arch="$(detect_arch)"
triple="linux-${arch}"
mapfile -t urls < <(resolve_release_url "$SKYGATE_VERSION" "$triple")
tarball_url="${urls[0]}"
sums_url="${urls[1]}"
download_and_verify "$tarball_url" "$sums_url" "$(dirname "$SKYGATE_BIN")" "$SKIP_VERIFY"

# -------- 4. env file + OpenRC service --------
# Same env file as the systemd path. The OpenRC service just
# sources the file instead of using systemd's EnvironmentFile=
# directive.
write_env_file "$SKYGATE_ETC_DIR" "$SKYGATE_DATA_DIR" "$SKYGATE_PORT" "$SKYGATE_USER"

# OpenRC service at /etc/init.d/skygate. The script is a
# standard start-stop-daemon wrapper — matches the style of
# every other Alpine service (acpid, crond, etcd, etc.).
cat > /etc/init.d/skygate <<EOF
#!/sbin/openrc-run
# /etc/init.d/skygate — OpenRC service for skygate.
# 2026-09-07 (V4 / B237.13): written by install-alpine.sh.
# Re-running the installer overwrites this file (intentional).
# /etc/skygate/skygate.env is the operator's file and is preserved.
name="skygate"
description="Skygate — self-service Tailscale/headscale portal"
command="${SKYGATE_BIN}"
command_user="${SKYGATE_USER}:${SKYGATE_USER}"
command_background="yes"
pidfile="/run/\${name}.pid"
directory="${SKYGATE_DATA_DIR}"
output_log="/var/log/\${name}.log"
error_log="/var/log/\${name}.log"
# Source the env file. openrc-run exports anything in the
# /etc/conf.d/ file matching the service name, so we just
# write a symlink to /etc/skygate/skygate.env there.
depend() {
    need net
    after firewall
}

start_pre() {
    if [ ! -f "${SKYGATE_ETC_DIR}/skygate.env" ]; then
        eerror "Missing ${SKYGATE_ETC_DIR}/skygate.env. Run install-alpine.sh first or create the file manually."
        return 1
    fi
}
EOF
chmod 0755 /etc/init.d/skygate
# /etc/conf.d/skygate sources the env file. openrc-run reads
# this BEFORE the service starts and exports all variables
# into the service's environment.
cat > /etc/conf.d/skygate <<EOF
# /etc/conf.d/skygate — sourced by openrc-run before start.
# We delegate to /etc/skygate/skygate.env (the operator's file)
# so the operator has ONE place to edit the config (matches the
# systemd EnvironmentFile= behavior in install-{debian,rh}.sh).
. ${SKYGATE_ETC_DIR}/skygate.env
EOF
echo "[install-alpine] wrote /etc/init.d/skygate + /etc/conf.d/skygate"

# -------- 5. enable + start --------
# OpenRC: `rc-update add` for boot-time, `rc-service start` for now.
# `rc-service` is OpenRC's per-service CLI; it returns non-zero if
# the service fails to start (unlike systemd, there's no "may be
# unhealthy" distinction — if start fails, the install fails).
rc-update add skygate default
if rc-service skygate start; then
    echo "[install-alpine] service skygate started (may be unhealthy until /etc/skygate/skygate.env is filled in)"
else
    echo "WARN: service skygate failed to start. Check: tail -f /var/log/skygate.log" >&2
fi

# -------- 6. done --------
print_next_steps "$SKYGATE_PORT" "skygate"
