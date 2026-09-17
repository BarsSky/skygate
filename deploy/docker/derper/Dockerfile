# ============================================================================
# skygate-derper — local Tailscale DERP relay image
#
# B260.2 (2026-09-17): build the derper image locally from the host's
# `/usr/local/bin/derper` binary instead of pulling `ghcr.io/tailscale/derper`.
#
# Why local build: the VM has outbound network restrictions that block
# `ghcr.io` (verified 2026-09-17 — `docker pull ghcr.io/tailscale/derper`
# returns "denied", and `docker pull tailscale/derper` returns "repository
# does not exist" because the upstream image is ghcr.io-only). The
# pre-B260.2 `deploy/templates/derper-compose.yml.tmpl` referenced
# `ghcr.io/tailscale/derper:latest` and would have failed on first
# deploy with no clear error path.
#
# Why debian-slim base: the host's `/usr/local/bin/derper` is a glibc-linked
# ELF binary (`file` reports "dynamically linked, interpreter
# /lib64/ld-linux-x86-64.so.2"). Alpine's musl cannot run glibc binaries
# without `libc6-compat` shims that miss some symbol versions; debian-slim
# has glibc by default and is the safe base.
#
# Why COPY a pre-built binary instead of `go install` from source: avoids
# pulling the entire Go toolchain + tailscale source tree at build time,
# keeps the image small (~80MB vs ~1GB with Go SDK), and uses the SAME
# binary the operator already trusted in the systemd unit (no version
# drift between systemd and docker derper).
#
# Build (run from repo root):
#     cp /usr/local/bin/derper deploy/docker/derper/derper
#     cd deploy/docker/derper
#     sudo docker build -t skygate-derper:latest .
#
# The COPY step means the build context MUST contain a `derper` file
# at the root. The migration script (`scripts/migrate_derper_to_docker.sh`)
# does the copy + build automatically. Operators who build by hand should
# `rm deploy/docker/derper/derper` after the build (it's a 12MB binary
# that shouldn't be committed — see .gitignore patterns below).
# ============================================================================

FROM debian:bookworm-slim

# ca-certificates lets derper do outbound HTTPS to the Tailscale
# control plane (for `--verify-clients=` URL fetches). The
# `:latest`-tagged bookworm-slim base is ~75MB; with ca-certificates
# added it grows to ~85MB. Multi-stage would shave a few MB but adds
# complexity — not worth it for a relay container.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# /var/lib/derper is the canonical cert + config directory (matches
# the path in the B-derper-cert systemd unit and the B260.2 compose
# file's volume mounts).
RUN mkdir -p /var/lib/derper/certs

# derper binary. Build context must include a `derper` file (the
# migrate_derper_to_docker.sh script copies /usr/local/bin/derper
# in before `docker build`).
COPY derper /usr/local/bin/derper
RUN chmod 0755 /usr/local/bin/derper

# derper needs to bind :443 + :80 (TCP) and :3478 (UDP). Running
# as root inside the container is fine for a DERP relay — there's
# no untrusted input surface, and dropping privileges would force
# `setcap` shims that aren't worth the complexity.
# ENTRYPOINT (not CMD) so the compose `command:` fully overrides
# argv without ambiguity.
ENTRYPOINT ["/usr/local/bin/derper"]

# Default args: match the systemd unit that shipped before B260.2.
# Compose `command:` overrides these — operators can change the
# port, cert mode, hostname, etc. without rebuilding the image.
# The compose file is the single source of truth for runtime config;
# these defaults are just for `docker run skygate-derper --help`
# ergonomics.
CMD ["--help"]
