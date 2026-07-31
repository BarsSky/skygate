# Multi-stage: pull tailscale binaries, then build the skygate image.
#
# 2026-07-14: Этап 14 v2 — Tailscale in-image (replaces the sidecar
# pattern from the previous commit). The sidecar approach had two
# failure modes:
#
#   (a) `network_mode: service:tailscale` broke docker's embedded DNS
#       (127.0.0.11:53 refused UDP), so the bot's getUpdates polling
#       timed out on every attempt.
#   (b) The sidecar's entrypoint.sh called `tailscale up --state=...`
#       with a flag that `tailscale up` doesn't accept. tailscale up
#       printed the help text and exited 2, the sidecar died, and
#       skygate lost its network namespace and got SIGKILL'd (exit
#       137).
#
# In-image is simpler: skygate is a normal container, runs tailscaled
# itself, and joins the tailnet via `tailscale up --accept-routes`. No
# `--exit-node` is ever set on skygate — the relay (a different node)
# advertises the Telegram IP ranges as subnet routes, and skygate
# accepts them, so api.telegram.org traffic flows through the relay
# and other traffic (headscale, etc.) stays direct.
#
# 2026-07-31: v0.32.13 — REVERT v0.32.8 multi-stage build pattern.
# The v0.32.8 build-at-image-time approach (commit 2d2d91f) had two
# runtime bugs that could only be reproduced on the live VM:
#
#   1. ENV CGO_ENABLED=0 broke go-sqlite3 (CGO required). Fixed in
#      v0.32.12 commit 292648e by setting CGO_ENABLED=1 and adding
#      gcc/musl-dev/sqlite-dev to the build stage.
#
#   2. The resulting CGO+musl binary has a separate TCP/HTTP issue
#      where http.Client.Do() goroutines can wedge in a stuck read()
#      syscall that doesn't honour http.Client.Timeout, context
#      cancellation, or Transport.DisableKeepAlives. The wedged
#      goroutine holds a sync.Mutex inside ListAllNodes that every
#      other goroutine (handlers, autoupdater, exit-node-monitor,
#      expirewatch, telegram bot) waits on — the whole binary
#      degrades within minutes. The goroutine+select+4s timeout
#      workaround in commit 79e512e is correct in theory but the
#      pattern doesn't propagate to every ListAllNodes() call site
#      in the codebase; missing one re-introduces the deadlock.
#
# The fix is to go back to the v0.32.5 build pattern: single-stage
# Dockerfile based on golang:1.25-alpine, build at container start
# via entrypoint.sh. The runtime `go build` runs in the same alpine
# + CGO toolchain as v0.32.8, so the CGO_ENABLED=0 issue doesn't
# apply (gcc/musl-dev/sqlite-libs are in the runtime image), AND
# the runtime build is short (~80s) so the start-up cost is
# acceptable. Plus the v0.29.0 self-update orchestrator already
# depends on this pattern (it `git checkout`s the source in /app
# and runs `go build` to produce a new binary, then `docker
# compose up -d --force-recreate` — which is exactly what
# entrypoint.sh does for every container start).

# Stage 1: extract tailscale + tailscaled from the official image.
FROM tailscale/tailscale:latest AS tailscale

# Stage 2: skygate runtime — Go 1.25 alpine + tailscale binaries.
FROM golang:1.25-alpine

# Network tools + Go build deps. tailscaled wants iptables on Linux
# (netfilter-mode=on); without ip6tables tailscaled refuses to start
# on Alpine. libcap, ca-certificates, sqlite-libs round out the
# tailscale/Go runtime needs. gcc + musl-dev are required for
# go-sqlite3 (pure-CGO driver) — CGO_ENABLED defaults to 1 on the
# golang:1.25-alpine base image so this is the only place that
# matters for the CGO contract.
#
# 2026-07-27: v0.29.0 — docker-cli-compose is the v0.29.0 self-update
# orchestrator's only way to run `docker compose` from inside the
# skygate container. Without it the orchestrator's
# `docker compose build skygate` step errors with
# "docker: unknown command: docker compose".
RUN apk add --no-cache \
        ca-certificates \
        docker-cli \
        docker-cli-compose \
        gcc \
        git \
        iptables \
        ip6tables \
        libcap \
        musl-dev \
        openssh-client \
        sqlite-libs

# Copy the official tailscale binaries from stage 1. The official image
# puts both at /usr/local/bin/; we re-chmod to be safe.
COPY --from=tailscale /usr/local/bin/tailscale /usr/local/bin/tailscale
COPY --from=tailscale /usr/local/bin/tailscaled /usr/local/bin/tailscaled
RUN chmod +x /usr/local/bin/tailscale /usr/local/bin/tailscaled

# Create workdir owned by non-root user so we can build without root.
# tailscaled itself runs as root (it needs CAP_NET_ADMIN to manipulate
# the tun device and iptables); the Go build is also done as root in
# this single-stage-for-runtime setup.
RUN mkdir -p /app && chmod 777 /app
# Tailscale state directory. tailscaled writes tailscaled.state here
# and exposes the control socket at /var/run/tailscale/tailscaled.sock.
# Both directories are bind-mounted from the host in docker-compose.yml
# so the state survives container restarts.
RUN mkdir -p /var/lib/tailscale /var/run/tailscale && \
    chmod 700 /var/lib/tailscale /var/run/tailscale
WORKDIR /app

# Build happens at container start via entrypoint.
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
