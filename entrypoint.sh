#!/bin/sh
# 2026-07-31: v0.32.8 — simplified entrypoint. The Go build is now
# done at `docker compose build` time (see Dockerfile), so the
# entrypoint just sets up Tailscale and execs the prebuilt binary.
# Container startup is now <5s instead of ~100s.
#
# What the entrypoint does (in order):
#
#   1. (optional) Start tailscaled and bring up the Tailscale client
#      with `--accept-routes`. Skipped entirely when TS_AUTHKEY_FILE
#      is not set, so a non-RF deployment can run the same image
#      without joining a tailnet at all.
#
#   2. exec /app/skygate so it becomes PID 1 of the container and
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
    # 2026-07-25: v0.28.5c — explicitly add --exit-node= (empty)
    # to the `tailscale up` invocation. Reason: Tailscale
    # persists ALL prefs in tailscaled.state (bind-mounted from
    # the host). If the operator ever ran `tailscale set
    # --exit-node=relay-1` on the container (even once, for
    # testing), the state remembers it. The next `tailscale up`
    # then errors with "requires mentioning all non-default
    # flags" because the new invocation doesn't list the
    # previously-set --exit-node. Adding --exit-node= to the
    # entrypoint ensures the exit-node is cleared on every
    # skygate restart.
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

echo "Skygate ready, starting..."

# 2. Exec skygate as PID 1. tailscaled (if running) is orphaned to
# PID 1 (= skygate now) and continues serving; when the container
# exits docker sends SIGTERM to PID 1 and SIGKILL to the rest after
# the grace period, so tailscaled doesn't leak.
#
# 2026-07-31: v0.32.12 — exec from /usr/local/bin/skygate instead
# of /app/skygate. The runtime image's /app is the bind-mount
# target for the host source tree (see docker-compose.yml), and a
# bind-mount REPLACES the image's /app contents. If we exec'd
# /app/skygate, the running binary would be whatever is on the
# HOST (a stale v0.32.5-era binary or nothing), not the freshly
# built one in the image. /usr/local/bin is outside the bind-mount
# so the image's binary always wins.
exec /usr/local/bin/skygate
