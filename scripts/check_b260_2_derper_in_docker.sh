#!/usr/bin/env bash
# check_b260_2_derper_in_docker.sh
#
# 2026-09-17 (B260.2) — regression check for the derper-in-docker
# migration. Operator 2026-09-17 follow-up after B260/B260.1
# shipped: "учти что derp на VM должен крутиться в docker а не
# в процесе systemd у VM" — derper should run in docker, not
# as a systemd process on the VM.
#
# The pre-B260.2 state had:
#   1. /etc/systemd/system/derper.service running derper as a
#      host process (bound to :443/:80/:3478)
#   2. deploy/templates/derper-compose.yml.tmpl referenced
#      ghcr.io/tailscale/derper:latest — but the agent VM blocks
#      ghcr.io (verified `docker pull` returns "denied")
#   3. No migration script — operator had to either manually
#      copy the systemd unit to a docker run command, OR give
#      up on the docker path entirely
#
# B260.2 fix:
#   - deploy/docker/derper/Dockerfile: build skygate-derper:latest
#     LOCALLY from /usr/local/bin/derper (debian-slim base for glibc)
#   - deploy/templates/derper-compose.yml.tmpl: ${DERP_IMAGE},
#     ${DERP_CERTMODE}, ${DERP_DERP_PORT}, ${DERP_HTTP_PORT} — all
#     operator-configurable via .env
#   - deploy/lib/env.sh: DERP_DERP_PORT=443, DERP_HOSTNAME,
#     DERP_CERTMODE=manual, DERP_CERT_DIR, DERP_CONFIG_DIR,
#     DERP_VERIFY_CLIENTS_URL, DERP_IMAGE — with sane defaults
#   - scripts/migrate_derper_to_docker.sh: 5-step migration
#     helper (pre-flight → build → stop systemd → start docker
#     → verify), idempotent
#   - deploy/docker/derper/README.md: operator-facing rationale
#
# What this checks:
#   A) Dockerfile exists at deploy/docker/derper/Dockerfile
#   B) Dockerfile uses debian-slim base (glibc compat for the
#      host's derper binary)
#   C) Dockerfile COPYs the derper binary + sets ENTRYPOINT
#   D) derper-compose template uses ${DERP_IMAGE} placeholder
#      (not hardcoded ghcr.io URL)
#   E) derper-compose template uses ${DERP_CERTMODE} (not
#      hardcoded letsencrypt — operator's systemd unit uses
#      manual certs)
#   F) derper-compose template uses ${DERP_DERP_PORT} for --a=
#      (not hardcoded :443 only — manual certs can use other
#      ports with --certmode=manual)
#   G) derper-compose template uses ${DERP_HTTP_PORT} for
#      --http-port=
#   H) env.sh defines the new DERP_DERP_PORT, DERP_HOSTNAME,
#      DERP_CERTMODE, DERP_CERT_DIR, DERP_CONFIG_DIR,
#      DERP_VERIFY_CLIENTS_URL, DERP_IMAGE vars with sane defaults
#   I) env.sh exports those new vars
#   J) migrate_derper_to_docker.sh exists + is executable
#   K) migrate script has stop-systemd step (B260.2's whole point)
#   L) migrate script has docker compose up step
#   M) migrate script has verify step (HTTPS + STUN check)
#   N) README.md exists for the local-image build
#   O) live-state prompt: ssh + check `docker ps --filter
#      name=derper` shows the container + /admin/derp shows
#      "DERPER-SERVICE: running"
#
# Run order: A-N as source-contract gates (run on every commit),
# O after the operator runs scripts/migrate_derper_to_docker.sh.

set -euo pipefail
# Disable bash pathname expansion so patterns like "/data/*" in
# grep invocations don't get glob-expanded into the list of
# matching files.
set -f

ok()  { printf '  \033[32m✓\033[0m %s\n' "$*"; }
fail(){ printf '  \033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }
hdr() { printf '\n\033[1m%s\033[0m\n' "$*"; }

cd "$(dirname "$0")/.."
ROOT="$(pwd)"

DOCKERFILE="deploy/docker/derper/Dockerfile"
README="deploy/docker/derper/README.md"
COMPOSE_TMPL="deploy/templates/derper-compose.yml.tmpl"
ENV_SH="deploy/lib/env.sh"
MIGRATE_SH="scripts/migrate_derper_to_docker.sh"

hdr "B260.2 — derper-in-docker migration"

# --- A: Dockerfile exists ---
if [ -f "$DOCKERFILE" ]; then
  ok "A: $DOCKERFILE exists"
else
  fail "A: $DOCKERFILE missing — B260.2 has no local-image build recipe"
fi

# --- B: debian-slim base (glibc compat) ---
# The host's /usr/local/bin/derper is glibc-linked; Alpine's musl
# can't run it without libc6-compat shims that miss symbol
# versions. The fix is debian-slim (which has glibc by default).
# We accept any debian:* image (slim, bookworm-slim, bullseye-slim,
# etc.) — the glibc property is what matters.
if grep -E '^FROM debian:' "$DOCKERFILE" >/dev/null 2>&1; then
  ok "B: Dockerfile uses debian:* base (glibc compat for host derper binary)"
else
  fail "B: Dockerfile does NOT use debian:* base — Alpine/musl will fail to run glibc derper"
fi

# --- C: COPY derper + ENTRYPOINT ---
# The build context must include the derper binary at the root
# (the migrate script copies it from /usr/local/bin/derper before
# `docker build`). ENTRYPOINT (not just CMD) so the compose
# `command:` fully overrides argv without ambiguity.
if grep -E '^COPY derper /usr/local/bin/derper' "$DOCKERFILE" >/dev/null 2>&1 \
   && grep -E '^ENTRYPOINT \["/usr/local/bin/derper"\]' "$DOCKERFILE" >/dev/null 2>&1; then
  ok "C: Dockerfile COPYs derper binary + sets ENTRYPOINT"
else
  fail "C: Dockerfile missing COPY derper /usr/local/bin/derper or ENTRYPOINT — binary won't be in image"
fi

# --- D: compose template uses ${DERP_IMAGE} placeholder ---
# Pre-B260.2 had `image: ghcr.io/tailscale/derper:latest` hardcoded
# which fails on the agent VM (ghcr.io blocked). The fix is to
# use a placeholder that defaults to the locally-built image but
# can be overridden to ghcr.io for operators with ghcr.io access.
if grep -E 'image: \$\{DERP_IMAGE' "$COMPOSE_TMPL" >/dev/null 2>&1; then
  ok "D: derper-compose uses \${DERP_IMAGE} placeholder (defaults to local skygate-derper:latest)"
else
  fail "D: derper-compose still hardcodes ghcr.io/tailscale/derper:latest — pull fails on VM"
fi

# --- E: ${DERP_CERTMODE} ---
# Pre-B260.2 had --certmode=letsencrypt hardcoded; the operator's
# systemd unit uses --certmode=manual (B-derper-cert). Make this
# configurable so the same template serves both.
if grep -E '\$\{DERP_CERTMODE\}' "$COMPOSE_TMPL" >/dev/null 2>&1; then
  ok "E: derper-compose uses \${DERP_CERTMODE} (operator can switch manual ↔ letsencrypt)"
else
  fail "E: derper-compose hardcodes certmode — operator's manual-cert setup won't match"
fi

# --- F: ${DERP_DERP_PORT} for --a= ---
# Manual cert mode requires :443 per derper --help, but the port
# should be operator-configurable (some operators front derper with
# a reverse proxy and bind derper to a non-privileged port).
if grep -E '\$\{DERP_DERP_PORT\}' "$COMPOSE_TMPL" >/dev/null 2>&1; then
  ok "F: derper-compose uses \${DERP_DERP_PORT} for --a= (operator-configurable listen port)"
else
  fail "F: derper-compose hardcodes --a= — operator can't override listen port"
fi

# --- G: ${DERP_HTTP_PORT} for --http-port ---
if grep -E '\$\{DERP_HTTP_PORT\}' "$COMPOSE_TMPL" >/dev/null 2>&1; then
  ok "G: derper-compose uses \${DERP_HTTP_PORT} for --http-port"
else
  fail "G: derper-compose hardcodes --http-port — operator can't override HTTP→HTTPS redirect port"
fi

# --- H: env.sh defines the new vars ---
# Each var must have a sane default (no `set -u` failures).
missing_defaults=()
for var in DERP_DERP_PORT DERP_HOSTNAME DERP_CERTMODE DERP_CERT_DIR DERP_CONFIG_DIR DERP_VERIFY_CLIENTS_URL DERP_IMAGE; do
  if ! grep -E "^${var}=\"\\\$\\{${var}:-" "$ENV_SH" >/dev/null 2>&1; then
    missing_defaults+=("$var")
  fi
done
if [ "${#missing_defaults[@]}" -eq 0 ]; then
  ok "H: env.sh defines all 7 new DERP_* vars with defaults"
else
  fail "H: env.sh missing defaults for: ${missing_defaults[*]}"
fi

# --- I: env.sh exports those new vars ---
# Without export, the render_template step in deploy.sh won't see
# the var, and the compose file would render with literal ${VAR}
# strings.
missing_exports=()
for var in DERP_DERP_PORT DERP_HOSTNAME DERP_CERTMODE DERP_CERT_DIR DERP_CONFIG_DIR DERP_VERIFY_CLIENTS_URL DERP_IMAGE; do
  # Match the var in the export line; tolerates spaces.
  if ! grep -E "^export .*\\b${var}\\b" "$ENV_SH" >/dev/null 2>&1; then
    missing_exports+=("$var")
  fi
done
if [ "${#missing_exports[@]}" -eq 0 ]; then
  ok "I: env.sh exports all 7 new DERP_* vars (deploy.sh render_template will see them)"
else
  fail "I: env.sh missing export for: ${missing_exports[*]} — render_template won't substitute them"
fi

# --- J: migrate script exists + executable ---
if [ -x "$MIGRATE_SH" ]; then
  ok "J: $MIGRATE_SH exists and is executable"
elif [ -f "$MIGRATE_SH" ]; then
  fail "J: $MIGRATE_SH exists but is NOT executable (run: chmod +x $MIGRATE_SH)"
else
  fail "J: $MIGRATE_SH missing — operator has no migration helper"
fi

# --- K: migrate script stops systemd ---
if grep -E 'systemctl stop derper\.service' "$MIGRATE_SH" >/dev/null 2>&1 \
   && grep -E 'systemctl disable derper\.service' "$MIGRATE_SH" >/dev/null 2>&1; then
  ok "K: migrate script stops + disables systemd derper (the whole point of B260.2)"
else
  fail "K: migrate script missing systemctl stop/disable derper — port :443 will stay bound by systemd"
fi

# --- L: migrate script starts docker derper ---
if grep -E 'docker compose.* up -d.*derper' "$MIGRATE_SH" >/dev/null 2>&1; then
  ok "L: migrate script starts docker derper via docker compose"
else
  fail "L: migrate script missing 'docker compose up -d derper' — docker derper never starts"
fi

# --- M: migrate script verifies ---
# We need at least an HTTPS GET check + a port-bind check. The STUN
# check is a nice-to-have but not required (UDP probes from bash are
# awkward).
if grep -F 'https://127.0.0.1' "$MIGRATE_SH" >/dev/null 2>&1 \
   && grep -F 'sport = :' "$MIGRATE_SH" >/dev/null 2>&1; then
  ok "M: migrate script verifies (HTTPS GET + port-bind check)"
else
  fail "M: migrate script missing verification — silent failure on bad certs / wrong port"
fi

# --- N: README exists for operator ---
if [ -f "$README" ]; then
  ok "N: $README exists (operator-facing rationale)"
else
  fail "N: $README missing — operator has no rationale for the local-image choice"
fi

# --- O: live-state prompt ---
hdr "O: live-state (operator-side, post-migration)"
cat <<'NOTE'
  After running scripts/migrate_derper_to_docker.sh on the
  agent VM (192.168.13.69), confirm:
    1. `sudo docker ps --filter name=derper` shows the
       `derper` container, status `Up`, image `skygate-derper:latest`.
    2. `sudo systemctl status derper` shows `inactive (dead)`
       (systemd unit stopped + disabled by the migrate script).
    3. `sudo ss -tlnp | grep ':443 '` shows the listener owned
       by `derper` PID (docker derper, NOT systemd PID).
    4. `sudo ss -ulnp | grep ':3478 '` shows STUN UDP listener
       owned by `derper` PID.
    5. /admin/derp renders "DERPER-SERVICE: running" (not stopped).
    6. `sudo curl -sS -k https://127.0.0.1:443/` returns 200 with
       the standard "This is a Tailscale DERP server" HTML.
    7. `sudo curl -sS -k https://127.0.0.1/derp` returns 101
       Switching Protocols (the WebSocket probe B260.1 added).

  Rollback (if any check fails):
    sudo systemctl enable --now derper
    sudo docker compose -f /home/skyadmin/headscale/derper-compose.yml stop derper
  The systemd unit is left in place by the migrate script so
  rollback is fast (<2s).
NOTE
