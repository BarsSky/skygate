#!/bin/bash
# install-bare.sh — V7 (B237.13) bare-binary installer.
#
# For the "I just want the binary on a server" use case: no
# systemd, no OpenRC, no service manager. The operator runs
# skygate manually (under tmux, nohup, supervisord, or a
# hand-rolled wrapper script). This installer:
#
#   1. Downloads + verifies the tarball (same as install-{debian,rh,alpine}.sh)
#   2. Drops the binary at /usr/local/bin/skygate
#   3. Writes /etc/skygate/skygate.env with starter values
#   4. Creates the skygate system user + /var/lib/skygate
#   5. Prints the run command (nohup / systemd-run / supervisord snippet)
#
# The installer does NOT start the service. The operator is
# expected to do that with whatever supervisor they prefer.
#
# When to use this:
#   - macOS dev machine (no systemd, no OpenRC)
#   - Windows WSL2 (you could use systemd in WSL2, but most
#     WSL2 setups don't enable it — bare is simpler)
#   - Embedded / appliance Linux without systemd (routers,
#     OpenWrt, etc.)
#   - You want skygate as a child process of your own
#     supervisor (supervisord, circus, honcho, tmux)
#   - You're hacking on skygate itself and want the
#     "as little between me and the binary" path
#
# What you lose vs the systemd install:
#   - No auto-restart on crash (your supervisor must do that)
#   - No auto-start on boot (your supervisor must do that)
#   - No log rotation (your supervisor must do that, or you
#     use a wrapper that pipes to logrotate)
#   - No clean shutdown on SIGTERM (skygate handles SIGTERM
#     fine — it just exits — but there's no "service stop"
#     command unless you write a wrapper)

set -euo pipefail

. "$(dirname "$0")/install-common.sh"

# Sanity: must run as root
if [ "$(id -u)" -ne 0 ]; then
    echo "ERROR: install-bare.sh must be run as root (use sudo)" >&2
    exit 1
fi

echo "[install-bare] starting"

# -------- 1. user + dirs (reuses the helper from install-common.sh) --------
create_user_and_dirs "$SKYGATE_USER" "$SKYGATE_DATA_DIR" "$SKYGATE_ETC_DIR"

# -------- 2. download + verify + install binary --------
arch="$(detect_arch)"
triple="linux-${arch}"   # (macOS users: use install-mac.sh or build from source)
mapfile -t urls < <(resolve_release_url "$SKYGATE_VERSION" "$triple")
tarball_url="${urls[0]}"
sums_url="${urls[1]}"
download_and_verify "$tarball_url" "$sums_url" "$(dirname "$SKYGATE_BIN")" "$SKIP_VERIFY"

# -------- 3. env file --------
write_env_file "$SKYGATE_ETC_DIR" "$SKYGATE_DATA_DIR" "$SKYGATE_PORT" "$SKYGATE_USER"

# -------- 4. done — operator runs it --------
cat <<EOF

================================================================
  Skygate installed (bare): $(${SKYGATE_BIN} version 2>/dev/null || echo "unknown")
================================================================

  No service manager was set up. You choose how to run skygate.

  1. Edit the env file:

       sudo \$EDITOR /etc/skygate/skygate.env

     Fill in HEADSCALE_URL + HEADSCALE_API_KEY (placeholders in
     the file).

  2. Run skygate. Pick your supervisor:

     A. nohup (simplest — no auto-restart):

        sudo -u $SKYGATE_USER nohup env \$(cat /etc/skygate/skygate.env | xargs) \\
            ${SKYGATE_BIN} \\
            > /var/log/skygate.log 2>&1 &

     B. systemd-run (one-shot systemd unit, no install):

        sudo systemd-run --unit=skygate --working-directory=$SKYGATE_DATA_DIR \\
            --setenv=HEADSCALE_URL=... --setenv=HEADSCALE_API_KEY=... \\
            ${SKYGATE_BIN}

     C. supervisord (snippet for /etc/supervisor/conf.d/skygate.conf):

        [program:skygate]
        command=${SKYGATE_BIN}
        directory=$SKYGATE_DATA_DIR
        user=$SKYGATE_USER
        autostart=true
        autorestart=true
        environment=HEADSCALE_URL="...",HEADSCALE_API_KEY="..."
        stdout_logfile=/var/log/skygate.log
        stderr_logfile=/var/log/skygate.log

  3. Open the portal:

       http://localhost:$SKYGATE_PORT/login

================================================================
EOF
