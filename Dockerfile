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

# Stage 1: extract tailscale + tailscaled from the official image.
FROM tailscale/tailscale:latest AS tailscale

# Stage 2: skygate runtime — Go 1.23 alpine + tailscale binaries.
FROM golang:1.23-alpine

# Network tools + Go build deps. tailscaled wants iptables on Linux
# (netfilter-mode=on); without ip6tables tailscaled refuses to start
# on Alpine. libcap, ca-certificates, sqlite-libs round out the
# tailscale/Go runtime needs.
RUN apk add --no-cache \
        ca-certificates \
        docker-cli \
        gcc \
        iptables \
        ip6tables \
        libcap \
        musl-dev \
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
