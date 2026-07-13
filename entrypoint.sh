#!/bin/sh
# 2026-07-14: Этап 14 v2 — Tailscale in-image.
#
# This entrypoint does three things in order:
#
#   1. (optional) Start tailscaled and bring up the Tailscale client
#      with `--accept-routes`. Skipped entirely when TS_AUTHKEY_FILE
#      is not set, so a non-RF deployment can run the same image
#      without joining a tailnet at all.
#
#   2. Build skygate from the bind-mounted source (the original
#      entrypoint flow: go mod download, go mod tidy, git for build
#      labels, go build with LDFLAGS).
#
#   3. exec /app/skygate so it becomes PID 1 of the container and
#      receives signals directly from docker.
#
# We intentionally do NOT call `tailscale set --exit-node=...` here.
# The relay (a separate node in the tailnet) advertises the Telegram
# IP ranges as subnet routes; skygate's `tailscale up --accept-routes`
# picks them up automatically, so api.telegram.org traffic is routed
# via the relay while everything else (headscale, etc.) stays direct.
# This keeps skygate's "exit-node=nodename" off the Tailscale client
# entirely (see the discussion in the commit message).
set -e

# 1. Tailscale setup.
#
# We guard on TS_AUTHKEY_FILE (a docker secret path) being present
# AND readable. A non-RF deployment that doesn't need Tailscale at
# all can simply not mount the secret; the entrypoint then skips
# tailscaled and skygate starts with direct internet access.
if [ -n "$TS_AUTHKEY_FILE" ] && [ -f "$TS_AUTHKEY_FILE" ]; then
    echo "[init] starting tailscaled"
    # tailscaled writes tailscaled.state into --statedir; the control
    # socket is at /var/run/tailscale/tailscaled.sock. Both paths are
    # bind-mounted from the host in docker-compose.yml so the state
    # survives container restarts.
    mkdir -p /var/lib/tailscale /var/run/tailscale
    tailscaled --statedir=/var/lib/tailscale \
        >/var/log/tailscaled.log 2>&1 &
    TAILSCALED_PID=$!
    echo "[init] tailscaled PID=$TAILSCALED_PID"

    # Wait for the control socket. tailscaled takes a few seconds to
    # come up; we give it 30s and continue anyway if it's not ready
    # (skygate still works on the host network even without Tailscale).
    READY=""
    for i in $(seq 1 30); do
        if tailscale status >/dev/null 2>&1; then
            READY="yes"
            echo "[init] tailscaled ready after ${i}s"
            break
        fi
        sleep 1
    done
    if [ -z "$READY" ]; then
        echo "[init] WARNING: tailscaled not ready after 30s; continuing"
    fi

    AUTHKEY=$(cat "$TS_AUTHKEY_FILE")
    LOGIN_SERVER="${TS_LOGIN_SERVER:-https://head.skynas.ru}"
    HOSTNAME="${TS_HOSTNAME:-skygate-vm}"

    echo "[init] tailscale up --accept-routes (login-server=$LOGIN_SERVER, hostname=$HOSTNAME)"
    if ! tailscale up \
        --login-server="$LOGIN_SERVER" \
        --authkey="$AUTHKEY" \
        --hostname="$HOSTNAME" \
        --accept-routes 2>&1; then
        echo "[init] WARNING: tailscale up failed; continuing without Tailscale"
    fi

    echo "[init] tailscale status:"
    tailscale status 2>&1 | head -10 || true
else
    echo "[init] TS_AUTHKEY_FILE not set — Tailscale skipped (non-RF mode)"
fi

# 2. Build skygate (existing flow, preserved verbatim from the
# pre-Tailscale entrypoint).
cd /app
echo "Downloading Go modules..."
go mod download || true
go mod tidy || true
apk add --no-cache openssh-client git 2>/dev/null
echo "Building Skygate..."
# 2026-07-11: inject build label from git so the web footer + telegram
# /version reflect the real tag/commit. .git is bind-mounted via
# docker-compose (`./:/app`); if it's missing (e.g. CI build from a
# tarball), fall back to "dev". The alpine base image does NOT include
# git, so we install it via apk above.
# git 2.35+ refuses to operate on a repo whose owner doesn't match
# the current uid ("dubious ownership"). The host bind-mounts .git
# as uid 1000 while we run as root, so mark /app as safe explicitly.
git config --global --add safe.directory /app
GIT_VER=$(git describe --tags --always 2>/dev/null || echo "dev")
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
if [ "${GIT_VER}" = "dev" ] || [ "${GIT_COMMIT}" = "unknown" ]; then
    echo "  WARN: build label not resolved (GIT_VER=${GIT_VER} GIT_COMMIT=${GIT_COMMIT})" >&2
fi
BUILD_TIME=$(date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS="-X main.version=${GIT_VER} -X main.commit=${GIT_COMMIT} -X main.buildTime=${BUILD_TIME}"
echo "  version=${GIT_VER} commit=${GIT_COMMIT} built=${BUILD_TIME}"
go build -buildvcs=false -ldflags "${LDFLAGS}" -o /app/skygate ./cmd/skygate || { echo "BUILD FAILED"; exit 1; }
chmod +x /app/skygate
echo "Skygate ready, starting..."

# 3. Exec skygate as PID 1. tailscaled (if running) is orphaned to
# PID 1 (= skygate now) and continues serving; when the container
# exits docker sends SIGTERM to PID 1 and SIGKILL to the rest after
# the grace period, so tailscaled doesn't leak.
exec /app/skygate
