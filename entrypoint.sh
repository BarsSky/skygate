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
    # 2026-08-10: v0.33.1.34 B86 — accept BOTH the legacy
    # TS_LOGIN_SERVER and the newer SKYGATE_TS_LOGIN_SERVER
    # (set in docker-compose.yml), mirroring the
    # TS_AUTHKEY_FILE → SKYGATE_TS_AUTHKEY_FILE fallback that
    # v0.33.1.9 already added for the authkey. The
    # docker-compose.yml was updated in v0.33.1.16 (B65) to
    # set SKYGATE_TS_LOGIN_SERVER (so .env / env_file wins
    # over a hardcoded value), but the entrypoint was never
    # updated to read the new name. The pre-B86 default
    # `https://head.example.com` is a placeholder that points
    # at the Tailscale example domain — `tailscale up` against
    # it silently failed (the 30s timeout + "WARNING"
    # swallowed the error), the state file ended up with
    # ControlURL=`https://head.example.com`, and the
    # container's tailscaled is in NoState forever after.
    # Live symptom: 100.64.0.3 unreachable from inside the
    # skygate container even though tailscale0 is up
    # (state shows "logged out, fetch control key from
    # head.example.com: no DNS fallback"). The fallback chain
    # preserves the legacy name (so an operator who manually
    # set TS_LOGIN_SERVER still wins) and adds the newer
    # SKYGATE_TS_LOGIN_SERVER (which docker-compose / .env
    # set).
    LOGIN_SERVER="${TS_LOGIN_SERVER:-${SKYGATE_TS_LOGIN_SERVER:-https://head.example.com}}"
    # Same pattern for the hostname: legacy TS_HOSTNAME +
    # newer SKYGATE_TS_HOSTNAME.
    HOSTNAME="${TS_HOSTNAME:-${SKYGATE_TS_HOSTNAME:-skygate-host-1}}"
    # 2026-08-25 (B185): Tailscale's `tailscale up` is an
    # "all-or-nothing" command — if the persisted state has
    # any non-default flag set (e.g. --advertise-tags), the
    # next `tailscale up` MUST mention that flag, or it
    # errors with "requires mentioning all non-default
    # flags" and the entire call is a no-op. Live symptom
    # (2026-08-25): the operator's earlier `tailscale set
    # --advertise-tags=tag:dev-infra-skygate-host-1,tag:private`
    # persisted into tailscaled.state, and every subsequent
    # `tailscale up --accept-routes` silently failed
    # (WARNING logged, exit code non-zero), leaving
    # `RouteAll=false` in the state. Result: skygate
    # container never accepted the relay's advertised
    # subnet routes, and the Telegram probe stayed
    # "unreachable" even though everything else was correct.
    #
    # The fix: read the existing state's advertise-tags
    # (if any) and pass them back. If the state has no
    # advertise-tags, fall back to the B111-canonical
    # value (`tag:dev-infra-skygate-host-1,tag:private`).
    # Operators can override with SKYGATE_TS_ADVERTISE_TAGS.
    ADVERTISE_TAGS="${SKYGATE_TS_ADVERTISE_TAGS:-}"
    if [ -z "$ADVERTISE_TAGS" ] && [ -f /var/lib/tailscale/tailscaled.state ]; then
        # tailscaled.state is a JSON blob. The current
        # profile's AdvertiseTags field is base64-encoded
        # inside _profiles. Python is the most reliable way
        # to extract it (jq is also fine if present).
        ADVERTISE_TAGS=$(python3 -c "
import json, base64, sys
try:
    with open('/var/lib/tailscale/tailscaled.state') as f:
        d = json.load(f)
    profiles = json.loads(d.get('_profiles', '{}'))
    cur = d.get('_current-profile', '')
    p = profiles.get(cur, {})
    tags = p.get('AdvertiseTags') or []
    if tags:
        print(','.join(tags))
except Exception:
    pass
" 2>/dev/null)
    fi
    ADVERTISE_TAGS="${ADVERTISE_TAGS:-tag:dev-infra-skygate-host-1,tag:private}"
    echo "[init] advertise-tags=$ADVERTISE_TAGS"

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
    if ! timeout 30 tailscale up \
        --login-server="$LOGIN_SERVER" \
        --authkey="$AUTHKEY" \
        --hostname="$HOSTNAME" \
        --accept-routes \
        --accept-dns=false \
        --exit-node= \
        --advertise-tags="$ADVERTISE_TAGS" 2>&1; then
        # timeout exits 124, tailscale up itself exits non-zero
        # on auth failure. Either way: log and move on. v0.33.1.9
        # entry-point fix — without the timeout, `tailscale up`
        # blocks forever when tailscaled didn't come up (e.g.
        # because the headscale login-server was unreachable),
        # which hung the entrypoint and prevented skygate from
        # ever reaching /healthz. The 30s ceiling matches the
        # tailscaled-readiness wait above.
        echo "[init] WARNING: tailscale up failed or timed out; continuing without Tailscale"
    fi

    echo "[init] tailscale status:"
    tailscale status 2>&1 | head -10 || true
else
    echo "[init] TS_AUTHKEY_FILE not set — Tailscale skipped (non-RF mode)"
fi

# 2026-08-10: v0.33.1.39 B91 — pre-flight headscale wait.
#
# skygate is designed to start INDEPENDENTLY of headscale. The
# admin explicitly configures HEADSCALE_URL via .env (or the
# /admin/headscale web UI), and headscale being down should
# never block the skygate container from booting. This is the
# whole point of B90-style loose coupling between services.
#
# That said, when the VM reboots, ALL containers restart in
# parallel. headscale (gRPC, DB migrations, policy reload)
# takes ~30s to come up; skygate is up in ~5s. For the first
# 25s after a reboot, every skygate → headscale API call fails
# (auth.go New() builds the client eagerly, main.go's
# ensureHeadscaleUser runs at startup, B77 autoupdater polls
# immediately, /readyz returns 503 because headscale isn't
# reachable yet). The errors are non-fatal — skygate keeps
# running and recovers when headscale comes up — but the
# operator sees a wall of "headscale unreachable" errors and
# may incorrectly diagnose it as a broken startup.
#
# This pre-flight wait polls HEADSCALE_URL /health for up to
# 60s. It does NOT block skygate startup if the URL is empty
# (no .env value, no per-admin override), unreachable, or
# still booting — it just logs a warning and continues. The
# 60s ceiling matches the tailscaled-wait above (both are
# best-effort startup probes that don't gate the build).
#
# HEADSCALE_URL is read directly (set by docker-compose's
# env_file from .env). No SKYGATE_ prefix — headscale runs
# outside the skygate container, so it doesn't know about
# the SKYGATE_ namespace. The /health endpoint is a
# headscale-native endpoint (gRPC health-checked) that
# returns 200 once the daemon is ready to accept API calls.
HEADSCALE_URL="${HEADSCALE_URL:-}"
# 2026-08-11: v0.33.1.42 D4 — pre-flight wait timeout is now
# configurable via SKYGATE_HEADSCALE_WAIT_TIMEOUT (in seconds).
# The 60s default is fine for most VMs (headscale boots in
# <30s on a single-VM deploy). Operators with slow disks, large
# headscale DBs, or NAS-backed headscale data dirs can set this
# higher (e.g. SKYGATE_HEADSCALE_WAIT_TIMEOUT=180 for a 3-min
# ceiling). Set to 0 to disable the wait entirely (skygate
# starts immediately; the autoupdater + B91 pattern still
# tolerates headscale being down — the pre-flight wait is just
# cosmetic noise-reduction on a cold boot).
HS_WAIT_TIMEOUT="${SKYGATE_HEADSCALE_WAIT_TIMEOUT:-60}"
if [ -n "$HEADSCALE_URL" ] && [ "$HS_WAIT_TIMEOUT" -gt 0 ]; then
    echo "[init] pre-flight: waiting for headscale at $HEADSCALE_URL (max ${HS_WAIT_TIMEOUT}s)"
    HS_READY=""
    for i in $(seq 1 "$HS_WAIT_TIMEOUT"); do
        if wget -qO- --timeout=2 "$HEADSCALE_URL/health" >/dev/null 2>&1; then
            HS_READY="yes"
            echo "[init] headscale ready after ${i}s"
            break
        fi
        sleep 1
    done
    if [ -z "$HS_READY" ]; then
        echo "[init] WARNING: headscale not ready after ${HS_WAIT_TIMEOUT}s; skygate starting anyway (admin can fix HEADSCALE_URL via /admin/headscale if it points to the wrong place)"
    fi
elif [ -n "$HEADSCALE_URL" ] && [ "$HS_WAIT_TIMEOUT" = "0" ]; then
    echo "[init] SKYGATE_HEADSCALE_WAIT_TIMEOUT=0; pre-flight wait disabled (skygate starts immediately; B91 pattern tolerates headscale being down)"
else
    echo "[init] HEADSCALE_URL not set; headscale calls will return errors until configured (set in .env or /admin/headscale)"
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
# tarball), fall back to "dev". The alpine image does NOT include
# git, so we install it via apk above.
# git 2.35+ refuses to operate on a repo whose owner doesn't match
# the current uid ("dubious ownership"). The host bind-mounts .git
# as uid 1000 while we run as root, so mark /app as safe explicitly.
#
# 2026-08-12: v1.3.1 (Phase 2 of SQLite removal) — REMOVED `-tags postgres`
# build tag. v1.3.0 (commit b1baa4a) deleted the `//go:build postgres`
# conditional; the only DB driver left is github.com/jackc/pgx/v5
# (pure Go). The runtime build is now CGO_ENABLED=0 (the default
# when no `import "C"` exists; verified by `grep -r "import \"C\"" cmd/
# internal/ 2>/dev/null` returning zero matches on 2026-08-12). Result:
# 24 MB static binary that doesn't link against musl/libc.
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

# 3. SSH key setup (B202.5). The agent's ~/.ssh/ is bind-mounted
# at /etc/skygate/ssh_key. We copy id_ed25519 to /tmp/ssh_key
# and chmod 600 (ssh requires private keys to be 0600 — without
# this the transport's `ssh svi "pg_dump ..."` would fail with
# "Permissions 0755 are too open" regardless of which user runs
# the ssh inside the container).
#
# Workaround: this host's overlayfs turns single-file bind-mounts
# into empty directories (a kernel issue on the agent's docker
# version). When the bind mount is empty inside the container,
# we fall back to reading the key from the host via a different
# mechanism: the `docker.sock` mount + the docker CLI, then
# `docker cp` from the HOST filesystem (passed via a host
# bind-mount at /host-keys, which IS a directory mount that
# works correctly on this host's overlayfs). The /host-keys
# path is set up by docker-compose.yml to point at the host's
# .ssh/ directory; we cat the id_ed25519 from there.
if [ -f /etc/skygate/ssh_key/id_ed25519 ]; then
    cp /etc/skygate/ssh_key/id_ed25519 /tmp/ssh_key
    chmod 600 /tmp/ssh_key
elif [ -f /host-keys/id_ed25519 ]; then
    # overlayfs workaround: read from the host's .ssh/ via a
    # directory mount at /host-keys (which works on this host's
    # overlayfs where single-file mounts become empty dirs).
    echo "[init] B202.5: cat /host-keys/id_ed25519 > /tmp/ssh_key (overlayfs workaround)"
    cp /host-keys/id_ed25519 /tmp/ssh_key
    chmod 600 /tmp/ssh_key
fi

# 3. Exec skygate as PID 1. tailscaled (if running) is orphaned to
# PID 1 (= skygate now) and continues serving; when the container
# exits docker sends SIGTERM to PID 1 and SIGKILL to the rest after
# the grace period, so tailscaled doesn't leak.
exec /app/skygate
