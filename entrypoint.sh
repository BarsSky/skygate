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
# We guard on the authkey file path (a docker secret path, or
# /data/ts/authkey written by the /admin/tailscale web UI) being
# present AND readable. A non-RF deployment that doesn't need
# Tailscale at all can simply not set the env var + not mount the
# secret; the entrypoint then skips tailscaled and skygate starts
# with direct internet access.
#
# v0.33.1.9: accept BOTH the legacy TS_AUTHKEY_FILE and the newer
# SKYGATE_TS_AUTHKEY_FILE (set in docker-compose.yml). The two names
# were a long-standing mismatch — docker-compose sets
# SKYGATE_TS_AUTHKEY_FILE=/run/secrets/ts_authkey, but the entrypoint
# only checked TS_AUTHKEY_FILE, so the auth key was being silently
# ignored. Picking the first non-empty of the two in order (legacy
# first, so any operator who manually set the old name still wins)
# keeps the change additive and backwards-compatible.
TS_AUTHKEY_FILE="${TS_AUTHKEY_FILE:-${SKYGATE_TS_AUTHKEY_FILE:-}}"
# v0.33.1.9: also pick up /data/ts/authkey (the file the
# /admin/tailscale web UI writes to). Lowest priority so the
# explicit env vars still win on a fresh boot; the web UI
# key is the per-deploy override that survives container
# restarts (the /data dir is bind-mounted from the host).
if [ -z "$TS_AUTHKEY_FILE" ] && [ -f /data/ts/authkey ]; then
    TS_AUTHKEY_FILE="/data/ts/authkey"
fi
export TS_AUTHKEY_FILE
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
    LOGIN_SERVER="${TS_LOGIN_SERVER:-https://head.example.com}"
    HOSTNAME="${TS_HOSTNAME:-skygate-host-1}"

    echo "[init] tailscale up --accept-routes (login-server=$LOGIN_SERVER, hostname=$HOSTNAME)"
    # 2026-07-14: --accept-dns=false is critical. By default
    # tailscaled overwrites /etc/resolv.conf with Tailscale's
    # MagicDNS resolver (100.100.100.100), which only knows
    # about tailnet names. Docker's own DNS (127.0.0.11) is no
    # longer consulted, so the container can't resolve its
    # Docker-network peers by name — most importantly
    # "headscale" (the headscale API endpoint configured via
    # HEADSCALE_URL=http://headscale:50444).
    #
    # Disabling accept-dns means the container keeps using
    # Docker's DNS. The downside is that tailnet names (e.g.
    # `relay-1.tailnet`) won't resolve from inside the
    # container — but skygate doesn't currently need to
    # resolve tailnet names; it only talks to the Docker
    # service named "headscale" and to api.telegram.org (an
    # IP literal, after the resolver at probe time).
    #
    # The previous "sidecar + network_mode: service:tailscale"
    # setup hit the same DNS problem differently: the shared
    # netns broke Docker's embedded DNS responder. In-image
    # tailscaled doesn't share netns, so the responder works
    # — we just need to stop tailscaled from replacing
    # /etc/resolv.conf.
    #
    # 2026-07-25: v0.28.5c — explicitly add --exit-node= (empty)
    # to the `tailscale up` invocation. Reason: Tailscale
    # persists ALL prefs in tailscaled.state (bind-mounted from
    # the host). If the operator ever ran `tailscale set
    # --exit-node=relay-1` on the container (even once, for
    # testing), the state remembers it. The next `tailscale up`
    # then errors with "requires mentioning all non-default
    # flags" because the new invocation doesn't list the
    # previously-set --exit-node. The entrypoint prints a
    # warning and continues — but the OLD exit-node is still
    # active. Symptom: skygate-host-1 routes ALL traffic (including
    # 172.18.0.0/16 Docker bridge) through the exit-node, which
    # breaks connectivity to the openresty/Caddy upstream
    # (504 Gateway Time-out on https://skygate.example.com).
    # Adding --exit-node= to the entrypoint ensures the exit-node
    # is cleared on every skygate restart. If the operator wants
    # the skygate container to be an exit-node itself, they can
    # explicitly set it AFTER the entrypoint runs (`docker exec
    # skygate tailscale set --exit-node=<tag>`).
    if ! tailscale up \
        --login-server="$LOGIN_SERVER" \
        --authkey="$AUTHKEY" \
        --hostname="$HOSTNAME" \
        --accept-routes \
        --accept-dns=false \
        --exit-node= 2>&1; then
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
# tarball), fall back to "dev". The alpine workstation-8 image does NOT include
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
go build -buildvcs=false -tags postgres -ldflags "${LDFLAGS}" -o /app/skygate ./cmd/skygate || { echo "BUILD FAILED"; exit 1; }
chmod +x /app/skygate
echo "Skygate ready, starting..."

# 3. Exec skygate as PID 1. tailscaled (if running) is orphaned to
# PID 1 (= skygate now) and continues serving; when the container
# exits docker sends SIGTERM to PID 1 and SIGKILL to the rest after
# the grace period, so tailscaled doesn't leak.
exec /app/skygate
