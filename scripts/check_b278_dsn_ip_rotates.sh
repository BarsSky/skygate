#!/usr/bin/env bash
# check_b278_dsn_ip_rotates.sh
#
# 2026-09-22 (B278) — launch_skigate.sh must NOT bake a docker bridge IP
# into SKYGATE_DB / SKYGATE_DB_DSN.
#
# Background (operator-visible incident on 192.168.13.69, 2026-09-22):
# The pre-B278 launch_skigate.sh captured the postgres container's current
# IPv4 (`docker inspect ... .NetworkSettings.Networks.headscale_default.IPAddress`)
# and wrote it into the .env DSN. Postgres container IPs on the headscale_default
# bridge are NOT stable — every recreate (image upgrade, `docker run` after a VM
# reboot, even a healthy stop+start cycle on some Docker versions) hands out a
# fresh IP from 172.18.0.0/16. The stale DSN then crashed skygate on every
# restart with "DB pre-flight: <old-ip>:5432 UNREACHABLE". Worse, the
# `--restart=on-failure:5` policy exhausted its 5 retries after a few minutes
# and skygate stopped permanently. Downstream headscale then crash-looped on
# "creating OIDC provider from issuer config: 502 Bad Gateway" because its OIDC
# discovery target (skygate) was down. Result: 8h of stale state where the
# operator thought OIDC was broken, when really just the docker bridge IP had
# rotated under a stale DSN.
#
# Fix:
#  - launch_skigate.sh now sets PG_HOST="$PG_CONTAINER_NAME" (the docker DNS
#    name `skygate-pg-local`), so docker's embedded DNS (127.0.0.11:53)
#    resolves it to whatever IP postgres currently has.
#  - launch_skigate.sh now uses --restart=unless-stopped instead of
#    on-failure:5, so a transient DB outage during a cold boot doesn't
#    kill the container permanently.
#  - Explicit `--pg-host=<ip>` is still honoured as an escape hatch for
#    operators running postgres on a non-default host (external Patroni,
#    deploy/pg-ha, etc.).
#
# CONTRACTS
#   A  launch_skigate.sh defines PG_CONTAINER_NAME (the docker DNS name)
#   B  launch_skigate.sh auto-detect path assigns PG_HOST="$PG_CONTAINER_NAME"
#      (NOT `docker inspect ... .IPAddress`)
#   C  launch_skigate.sh has NO `.IPAddress` substring in the auto-detect block
#   D  launch_skigate.sh uses --restart=unless-stopped (NOT on-failure:N)
#   E  --pg-host=<ip> override still wins (escape hatch for external PG)
#   F  the script itself is tracked by git (AGENTS.md trap #11)

set -uo pipefail
if [ -f "$(dirname "$0")/../cmd/skygate/main.go" ]; then
  cd "$(dirname "$0")/.." || exit 1
elif [ -n "${SKYGATE_REPO:-}" ] && [ -f "$SKYGATE_REPO/cmd/skygate/main.go" ]; then
  cd "$SKYGATE_REPO" || exit 1
fi
[ -f scripts/launch_skigate.sh ] || {
  printf 'B278: cannot locate scripts/launch_skigate.sh from %s\n' "$PWD" >&2
  exit 2
}

PASS=0; FAIL=0; SKIP=0
ok()   { printf '  \033[32mPASS\033[0m %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2; FAIL=$((FAIL+1)); }
skip() { printf '  \033[33mSKIP\033[0m %s\n' "$*"; SKIP=$((SKIP+1)); }
hdr()  { printf '\n\033[1m%s\033[0m\n' "$*"; }

hdr "B278: launch_skigate.sh must not bake a docker bridge IP into the DSN"

# --- A: PG_CONTAINER_NAME is defined (the docker DNS name) ---
if grep -qE '^PG_CONTAINER_NAME="skygate-pg-local"' scripts/launch_skigate.sh; then
  ok "A  PG_CONTAINER_NAME=\"skygate-pg-local\" is defined"
else
  bad "A  PG_CONTAINER_NAME=\"skygate-pg-local\" is NOT defined (the variable the post-fix DSN relies on)"
fi

# --- B: auto-detect path assigns PG_HOST to the CONTAINER NAME, not an IP ---
# Use perl with a depth-counting state machine to extract the auto-detect block
# (the `if [ -z "$FORCE_PG_HOST" ]; then ... fi ... else ... fi` span). We
# match only the OUTER if/then block: a nested if/fi inside the block keeps
# us "in", a top-level fi at depth 1 closes it.
AUTODETECT_BLOCK=$(perl -0777 -ne '
  if (/^\s*if \[ -z "\$FORCE_PG_HOST" \]; then\n(.*?^\s*fi\b)/sm) {
    print $1;
  }
' scripts/launch_skigate.sh)
if printf '%s\n' "$AUTODETECT_BLOCK" | grep -qF 'PG_HOST="$PG_CONTAINER_NAME"'; then
  ok "B  auto-detect assigns PG_HOST=\"\$PG_CONTAINER_NAME\" (docker DNS name, NOT IP)"
else
  bad "B  auto-detect does NOT assign PG_HOST to the container name (still resolves an IP?)"
  printf '       auto-detect block was:\n%s\n' "$AUTODETECT_BLOCK" >&2
fi

# --- C: no .IPAddress substring survives in the auto-detect block ---
if printf '%s\n' "$AUTODETECT_BLOCK" | grep -qF '.IPAddress'; then
  bad "C  auto-detect block still references .IPAddress (stale-IP regression)"
else
  ok "C  auto-detect block has NO .IPAddress substring"
fi

# --- D: --restart=unless-stopped (NOT on-failure:N) ---
# We grep for a docker-run line specifically (starts with "    --restart="),
# not the prose in the explanatory comment block above it.
RESTART_LINE=$(grep -E '^[[:space:]]+--restart=' scripts/launch_skigate.sh || true)
if [ -z "$RESTART_LINE" ]; then
  bad "D  no docker-run --restart= line found at all"
elif printf '%s' "$RESTART_LINE" | grep -qF 'unless-stopped'; then
  ok "D  docker-run line uses --restart=unless-stopped"
else
  bad "D  docker-run line does NOT use --restart=unless-stopped (got: $RESTART_LINE)"
fi
if printf '%s' "$RESTART_LINE" | grep -qF 'on-failure'; then
  bad "D  docker-run line still uses --restart=on-failure (the B278 root-cause second-half)"
else
  ok "D  docker-run line has no on-failure policy (no permanent-stop risk)"
fi

# --- E: --pg-host=<ip> override is still honoured (escape hatch for external PG) ---
if awk '
  /--pg-host=\*\)[[:space:]]+FORCE_PG_HOST=/ { flag_form=1 }
  /--pg-host\)[[:space:]]+FORCE_PG_HOST=/ { bare_form=1 }
  /PG_HOST="\$FORCE_PG_HOST"/ { used=1 }
  END { exit !((flag_form || bare_form) && used) }
' scripts/launch_skigate.sh; then
  ok "E  --pg-host=<ip> override still flows into PG_HOST (external-PG escape hatch preserved)"
else
  bad "E  --pg-host=<ip> override is NOT honoured (regression: external Patroni setups cannot use this script)"
fi

# --- F: the script is tracked by git (AGENTS.md trap #11) ---
if git ls-files --error-unmatch scripts/launch_skigate.sh >/dev/null 2>&1; then
  ok "F  scripts/launch_skigate.sh is tracked by git"
else
  bad "F  scripts/launch_skigate.sh is NOT in git ls-files — a fresh clone would have nothing to run"
fi

hdr "B278 summary"
printf '  PASS=%d  FAIL=%d  SKIP=%d\n' "$PASS" "$FAIL" "$SKIP"
[ "$FAIL" -eq 0 ]
