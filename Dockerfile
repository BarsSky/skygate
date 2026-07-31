# Multi-stage Dockerfile for skygate.
#
# Stage 1: build the Go binary from the bind-mountable source dir
#          (or, when run via `docker build -f - . < tarball`, from
#          whatever is in the build context). Uses `golang:1.25-alpine`
#          for the Go toolchain.
#
# Stage 2: minimal runtime image. Just tailscale binaries + the
#          prebuilt skygate binary + the entrypoint script. NO
#          Go toolchain, NO git, NO openssh-client (we don't need
#          to re-build at container start anymore — see
#          entrypoint.sh for the simplified flow).
#
# 2026-07-31: v0.32.8 — speed fix. Previously the build happened
# at container start (in entrypoint.sh's `go mod download` + `go
# build` step). On a fresh image this downloaded 4 Go modules
# (testify, spew, go-difflib, yaml.v3) + the apk deps for git
# + openssh-client, taking ~100s before skygate even started.
# The fix: do the build at `docker compose build` time, copy
# the static binary to the runtime image. Container start is
# now <5s (just tailscaled init + skygate exec).
#
# Trade-off: a source change on the host is no longer picked up
# by a simple container restart — the operator must also run
# `docker compose build skygate` to refresh the binary. This is
# already the case for `make rebuild-deploy` (v0.29.0+) and the
# /admin/update orchestrator, so it's not a new constraint.

# Stage 1: pull tailscale binaries (re-used in stage 2).
FROM tailscale/tailscale:latest AS tailscale

# Stage 1.5: build the skygate binary.
# We copy the source at build time. .git is copied too so
# `git describe --tags --always` can inject the version label
# (matches what the old entrypoint.sh did at runtime).
FROM golang:1.25-alpine AS skygate-build

# Build deps. tailscale isn't needed at build time. We need:
#   - git: for the version label (git describe --tags --always)
#   - gcc, musl-dev: C toolchain for go-sqlite3 (cgo dependency).
#     go-sqlite3 is a pure-CGO driver; without cgo it returns
#     "Binary was compiled with 'CGO_ENABLED=0', go-sqlite3
#     requires cgo to work. This is a stub" on every DB call
#     and the binary crashes on startup with a "db: ping" error.
#     This was the v0.32.8 CGO regression (see RELEASE-NOTES.md
#     v0.32.12 entry for the full incident timeline).
#   - sqlite-dev: sqlite3.h headers + link stubs for go-sqlite3's
#     cgo bridge. Without this the build fails with
#     "fatal error: sqlite3.h: No such file or directory".
#
# We strip all of these out of the runtime image in stage 2;
# the only runtime artifact that survives is the prebuilt
# /app/skygate binary, dynamically linked against musl +
# sqlite-libs (which is in the runtime image).
RUN apk add --no-cache \
        gcc \
        git \
        musl-dev \
        sqlite-dev

WORKDIR /src

# Copy go.mod / go.sum first so the module download layer is cached
# separately from the source. The bind-mount workflow still works
# (operator's `make rebuild-deploy` runs `docker compose build` which
# re-uses the cache when go.mod/go.sum haven't changed).
COPY go.mod go.sum ./
RUN go mod download

# Now copy the source. The .dockerignore file excludes the bind-mount
# noise (`data/`, `data/ts/`, etc.) so the build context is small.
COPY . .

# 2026-07-31: v0.32.8 — fail loud on missing .git (the bind-mount
# workflow always has .git; the CI build from a tarball doesn't
# and falls back to "dev").
RUN git config --global --add safe.directory /src
ARG GIT_VER=dev
ARG GIT_COMMIT=unknown
ARG BUILD_TIME=unknown

# -buildvcs=false skips the embed of VCS info (we set our own via
# -ldflags). -trimpath strips absolute paths from the binary (cleaner
# stack traces + reproducible builds). -ldflags '-s -w' strips the
# symbol table + DWARF debug info (~30% smaller binary).
#
# 2026-07-31: v0.32.12 — REVERT CGO_ENABLED=0 → CGO_ENABLED=1.
# go-sqlite3 (the only DB driver in this project) is a pure-CGO
# driver. CGO_ENABLED=0 makes the import resolve to a stub that
# returns "Binary was compiled with 'CGO_ENABLED=0', go-sqlite3
# requires cgo to work. This is a stub" on every call. The
# v0.32.8 CGO regression is documented in RELEASE-NOTES.md v0.32.12
# — the build produced a 41MB binary that looked healthy but
# crashed on db.Ping() at startup, leaving port 8080 unbound
# and serving 504s from the upstream proxy (NPM/Caddy/openresty).
#
# With CGO_ENABLED=1 the binary is dynamically linked against
# musl (alpine's libc) + sqlite-libs (already in the runtime
# image). The 6MB glibc-free alpine-3.20 runtime image is still
# the deploy surface — we don't need glibc to be a 1:1 match
# because musl IS alpine's libc.
ENV CGO_ENABLED=1
RUN go build -buildvcs=false -trimpath \
    -ldflags "-s -w -X main.version=${GIT_VER} -X main.commit=${GIT_COMMIT} -X main.buildTime=${BUILD_TIME}" \
    -o /out/skygate ./cmd/skygate

# Stage 2: minimal runtime image.
FROM alpine:3.20

# Tailscale binaries from stage 1.
COPY --from=tailscale /usr/local/bin/tailscale /usr/local/bin/tailscale
COPY --from=tailscale /usr/local/bin/tailscaled /usr/local/bin/tailscaled
RUN chmod +x /usr/local/bin/tailscale /usr/local/bin/tailscaled

# Runtime deps: iptables (tailscaled needs it on Linux), libcap,
# ca-certificates, sqlite-libs. tailscale pulls iptables as a hard
# dep on Linux (netfilter-mode=on); without ip6tables tailscaled
# refuses to start on Alpine. libcap is the capability lib tailscale
# uses to drop privileges after creating the tun device.
# 2026-07-27: v0.29.0 — docker-cli-compose is the v0.29.0 self-update
# orchestrator's only way to run `docker compose` from inside the
# skygate container. Without it the orchestrator's
# `docker compose build skygate` step errors with
# "docker: unknown command: docker compose".
RUN apk add --no-cache \
    ca-certificates \
    docker-cli \
    docker-cli-compose \
    iptables \
    ip6tables \
    libcap \
    sqlite-libs \
    tzdata

# Tailscale state + control socket paths. Bind-mounted from the
# host in docker-compose.yml so the state survives container
# restarts.
RUN mkdir -p /var/lib/tailscale /var/run/tailscale && \
    chmod 700 /var/lib/tailscale /var/run/tailscale

# Workdir for the bind-mount of the source tree. The runtime image
# doesn't actually USE the source (the binary is prebuilt), but
# the v0.29.0 self-update orchestrator needs the source visible
# at /app for `git checkout` + rebuilds.
WORKDIR /app

# Prebuilt binary from stage 1.5.
#
# 2026-07-31: v0.32.12 — install to BOTH /usr/local/bin/skygate
# (the actual entrypoint path) AND /app/skygate (preserved for
# back-compat with the v0.29.0 self-update orchestrator).
#
# Why two paths? The runtime image's /app directory is the
# bind-mount target for the source code (see docker-compose.yml's
# `${SKYGATE_HOST_REPO_PATH:-/home/admin/skygate}/:/app`).
# A bind-mount REPLACES the directory contents: anything that
# Dockerfile COPYs into /app at build time is hidden by the
# host's source tree when the container starts. If we COPYed
# the binary only to /app/skygate, the running container would
# fall back to whatever is on the host's /app/skygate path —
# which is either a stale v0.32.5-era binary (the old
# entrypoint.sh wrote it there) or nothing.
#
# The v0.32.5 → v0.32.8 deploy was a silent outage because of
# exactly this: the v0.32.8 build put a fresh binary at
# /app/skygate in the image, but the host's bind-mount had
# a v0.32.5 binary at /home/admin/skygate/skygate, and
# that won. The new binary was never executed.
#
# The fix: install to /usr/local/bin/skygate (not bind-mounted)
# for the entrypoint, and ALSO install to /app/skygate for
# back-compat with the autoupdate orchestrator's
# `docker run --rm --volumes-from skygate skygate-skygate:latest
# /app/skygate --migrate-only` command (which runs WITHOUT the
# source bind-mount, so the image's /app/skygate IS visible).
COPY --from=skygate-build /out/skygate /usr/local/bin/skygate
RUN chmod +x /usr/local/bin/skygate
COPY --from=skygate-build /out/skygate /app/skygate
RUN chmod +x /app/skygate

# Entrypoint simplified: just tailscale setup + exec the prebuilt
# binary. No more `go mod download` / `go build` at startup.
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
