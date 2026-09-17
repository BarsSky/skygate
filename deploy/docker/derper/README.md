# skygate-derper — local DERP relay image

**B260.2 (2026-09-17)**: build a Tailscale DERP (`derper`) image locally from
the host's `/usr/local/bin/derper` binary, since `ghcr.io/tailscale/derper`
is unreachable from the agent VM (outbound network restriction).

## Why a local build

The pre-B260.2 `deploy/templates/derper-compose.yml.tmpl` referenced
`ghcr.io/tailscale/derper:latest` for the `image:`. The agent VM cannot
reach `ghcr.io` (`docker pull` returns "denied"), so the compose-based
deploy path would have failed on first start with no actionable error.

The local build uses the SAME binary the operator has been running via
systemd (`/usr/local/bin/derper`) so there is no version drift between
the legacy systemd unit and the new docker derper.

## Why debian-slim, not alpine

The host's derper is a glibc-linked ELF binary
(`file /usr/local/bin/derper` → "dynamically linked, interpreter
/lib64/ld-linux-x86-64.so.2"). Alpine uses musl, which can't run glibc
binaries without `libc6-compat` shims that miss some symbol versions.
`debian:bookworm-slim` has glibc by default and is the safe base.

## Build

The build context must contain a `derper` file at the root. The
migration script does this automatically; for a manual build:

```bash
cp /usr/local/bin/derper deploy/docker/derper/derper
cd deploy/docker/derper
sudo docker build -t skygate-derper:latest .
rm deploy/docker/derper/derper    # don't commit the binary
```

## Runtime

The image runs as root and exposes nothing to the docker bridge — the
compose file uses `network_mode: host` so derper binds directly to the
host's :443 (TCP), :80 (TCP), and :3478 (UDP). Cert files come from the
host's `/var/lib/derper/certs/` via a read-only bind mount.

See `deploy/templates/derper-compose.yml.tmpl` for the full service
definition (rendered by `deploy/deploy.sh` to `derper-compose.yml` on
the headscale server directory at deploy time).

## Image size

~85 MB on disk (debian-slim base + ca-certificates + derper binary).
Comparable to the official `ghcr.io/tailscale/derper` (~75 MB) — not
worth a multi-stage build for a 10MB delta.

## Testing the image without disrupting systemd derper

To smoke-test the image without touching the running systemd derper,
override the listen ports:

```bash
sudo docker run --rm --network host skygate-derper:latest \
    --hostname=test.derp.skynas.ru \
    --certmode=letsencrypt \
    --a=:1443 \
    --http-port=18080 \
    --stun
```

This binds :1443/:18080 instead of :443/:80 — no port conflict with the
live systemd derper. Once verified, stop the systemd derper and start the
docker derper with the production compose file.
