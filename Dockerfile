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
# 2026-08-12: v1.3.1 (Phase 2 of SQLite removal) — DROP CGO toolchain.
# Pre-v1.3.1 the runtime needed gcc + musl-dev + sqlite-libs because
# the v0.32.x build used CGO_ENABLED=1 + mattn/go-sqlite3 (a pure-CGO
# SQLite driver). Phase 1 of v1.3.0 (commit b1baa4a) removed sqlite3
# entirely: go.mod no longer requires github.com/mattn/go-sqlite3, the
# `//go:build postgres` build tag is gone, and the only DB driver left
# is github.com/jackc/pgx/v5 — a pure-Go PostgreSQL driver. No
# `import "C"` or `#cgo` directives remain anywhere in the source
# (grep'd 2026-08-12, zero matches in cmd/ and internal/). The runtime
# `go build` now runs with CGO_ENABLED=0, producing a 24 MB static
# binary that needs no musl/gcc/sqlite-libs on the runtime image.
# Result: smaller image, faster cold builds, no CGO-attached TCP/HTTP
# wedge behavior the v0.32.8 build had (C-bridge detokenizer was on
# the model server project, not skygate; that issue doesn't apply
# here).
#
# 2026-07-31: v0.32.13 — REVERT v0.32.8 multi-stage build pattern.
# (Historical note: this comment block used to document the CGO+musl
# toolchain. The 2026-08-12 v1.3.1 update removes that requirement
# entirely; see the new note above.)

# Stage 1: extract tailscale + tailscaled from the official image.
FROM tailscale/tailscale:latest AS tailscale

# Stage 2: skygate runtime — Go 1.25 alpine + tailscale binaries.
FROM golang:1.25-alpine

# Runtime deps. tailscaled wants iptables + ip6tables on Linux
# (netfilter-mode=on); without ip6tables tailscaled refuses to start
# on Alpine. libcap is required for the tailscaled binary. ca-certificates
# is required for go mod download (HTTPS to proxy.golang.org). git is
# required at container start (entrypoint.sh runs `git describe --tags`
# to embed the build label). openssh-client is required for the
# SyncAdvertisedRoutes path (B81/B85 — `ssh -p <port> <user>@<host>` to
# approve routes on the relay).
#
# 2026-08-12: v1.3.1 — REMOVED gcc + musl-dev + sqlite-libs. The runtime
# is now CGO_ENABLED=0 (Phase 1 of v1.3.0 removed mattn/go-sqlite3;
# pgx is pure Go). The resulting binary is 24 MB static and needs no
# libc/musl on the runtime image. Smaller image, faster cold builds,
# no CGO coupling.
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
        git \
        iptables \
        ip6tables \
        libcap \
        openssh-client

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
