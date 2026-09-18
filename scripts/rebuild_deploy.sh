#!/bin/bash
# scripts/rebuild_deploy.sh — rebuild skygate image + recreate
# container on the production VM.
#
# Canonical procedure (AGENTS.md "Updating the VM"):
#   1. chown data/ts/ (fix root-owned tailscale dirs)
#   2. git pull --ff-only
#   3. docker compose build skygate (3-5 min)
#   4. docker compose up -d --force-recreate --no-deps skygate
#   5. wait for /healthz (up to 5 min)
#   6. print new build label
#
# Usage (from operator workstation):
#   bash scripts/rebuild_deploy.sh
#   SSH_HOST=admin@1.2.3.4 bash scripts/rebuild_deploy.sh
#   SKYGATE_HOST_REPO_PATH=/home/skyadmin/skygate bash scripts/rebuild_deploy.sh
#
# SSH key auto-detected from ~/.ssh/id_ed25519 or
# /c/Users/<user>/.ssh/id_ed25519 (Git Bash from Windows).
# Override with SSH_KEY=/path/to/key.
#
# Env-var override:
#   SKYGATE_HOST_REPO_PATH — the host-side path of the skygate
#     repo (the docker-compose.yml's volume-mounts use this as the
#     source for bind-mounts). Default: /home/admin/skygate. Set
#     this when the operator's path differs (e.g. /home/skyadmin/
#     skygate on the agent VM at 192.168.13.69).
#     2026-09-17 (B260 deployment-time learning): docker-compose
#     interpolation is CWD-relative. When this script (or any
#     operator workflow) runs `docker compose -f /path/to/...yml`
#     from `/`, the env-var placeholder in extra_hosts (e.g.
#     "${SKYGATE_DERP_PROBE_HOST:-127.0.0.1}") silently resolves
#     to the default — the operator's .env is NOT auto-loaded.
#     The script's `cd /home/admin/skygate && sudo docker compose`
#     pattern below is REQUIRED (not just nice-to-have). Without
#     the cd, the B260 fix will be deployed but the runtime probe
#     will still resolve derp.skynas.ru to 127.0.0.1.
#
# 2026-07-30: extracted from the manual procedure in AGENTS.md.
# Earlier sessions did this inline (4 separate bash invocations).
# Now one command covers the whole flow.
#
# 2026-09-17 (B260 deployment follow-up): added SKYGATE_HOST_REPO_PATH
# env override + B260 env-var pre-flight check.

set -e

SSH_HOST="${SSH_HOST:-admin@192.0.2.1}"
SKYGATE_HOST_REPO_PATH="${SKYGATE_HOST_REPO_PATH:-/home/admin/skygate}"

# Auto-detect SSH key (same logic as scripts/verify_post_deploy.sh)
SSH_KEY="${SSH_KEY:-}"
for cand in \
  "$HOME/.ssh/id_ed25519" \
  "$HOME/.ssh/id_rsa" \
  "/mnt/c/Users/knaga/.ssh/id_ed25519" \
  "/c/Users/knaga/.ssh/id_ed25519"; do
  if [ -n "$cand" ] && [ -f "$cand" ]; then
    SSH_KEY="$cand"
    break
  fi
done

if [ -z "$SSH_KEY" ]; then
  echo "ERROR: no SSH key found (looked in ~/.ssh/, /mnt/c/Users/knaga/.ssh/, /c/Users/knaga/.ssh/)" >&2
  echo "       set SSH_KEY=/path/to/key" >&2
  exit 2
fi

SSH="ssh -i $SSH_KEY -o StrictHostKeyChecking=accept-new -o IdentitiesOnly=yes $SSH_HOST"

echo "=== rebuild_deploy.sh ==="
echo "  ssh:    $SSH_HOST"
echo "  repo:   $SKYGATE_HOST_REPO_PATH"
echo "  key:    $SSH_KEY"
echo "  date:   $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo

# 1. Fix root-owned tailscale dirs (the container tailscaled runs
#    as root; the bind-mount data/ts/ can get re-owned on restart).
$SSH "sudo chown -R admin:admin $SKYGATE_HOST_REPO_PATH/data/ts/ 2>/dev/null || true" || true
echo "[1/6] chown data/ts/ (silent if no permission denied)"

# 2. git pull
echo "[2/6] git pull --ff-only"
$SSH "cd $SKYGATE_HOST_REPO_PATH && git pull --ff-only" || {
  echo "  ERROR: git pull failed (probably diverged or no network)" >&2
  exit 3
}

# 2.5. B260 deployment-time pre-flight: warn if SKYGATE_DERP_PROBE_HOST
#      is unset in .env (would leave /admin/derp in 'stopped' state).
#      Not a hard error — operator may intentionally skip derper.
#      Set CHECK_B260_STRICT=1 to make it a hard error.
echo "[2.5/6] B260 pre-flight: SKYGATE_DERP_PROBE_HOST in .env"
PROBE_HOST=$($SSH "grep '^SKYGATE_DERP_PROBE_HOST=' $SKYGATE_HOST_REPO_PATH/.env 2>/dev/null | tail -1" 2>/dev/null || true)
if [ -z "$PROBE_HOST" ]; then
  echo "  WARN: SKYGATE_DERP_PROBE_HOST is unset in $SKYGATE_HOST_REPO_PATH/.env"
  echo "        /admin/derp will show 'DERPER-SERVICE: stopped' even when derper is up."
  echo "        Set it to the host IP where derper runs (e.g. 192.168.13.69)."
  if [ "${CHECK_B260_STRICT:-0}" = "1" ]; then
    echo "  CHECK_B260_STRICT=1 → aborting deploy" >&2
    exit 5
  fi
else
  echo "  OK: $PROBE_HOST"
fi

# 3. Build new image
# 2026-07-31: v0.32.8 — pass version info to the Dockerfile so the
# prebuilt binary carries the right GIT_VER / GIT_COMMIT / BUILD_TIME.
# The build is now done in the Dockerfile (multi-stage, 5-30s with
# cache hit) instead of in the entrypoint (was 100s on first run).
echo "[3/6] docker compose build skygate (5-30s, was 3-5 min pre-v0.32.8)"
GIT_VER=$(git describe --tags --always 2>/dev/null || echo dev)
GIT_COMMIT=$(git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)
$SSH "cd $SKYGATE_HOST_REPO_PATH && \
    SKYGATE_GIT_VER='$GIT_VER' \
    SKYGATE_GIT_COMMIT='$GIT_COMMIT' \
    SKYGATE_BUILD_TIME='$BUILD_TIME' \
    sudo -E docker compose build skygate 2>&1 | tail -5"

# 4. Recreate container.
# 2026-07-30: v0.32.4 — graceful stop BEFORE --force-recreate.
# Previously we used `docker compose up -d --force-recreate`
# which is the equivalent of `docker kill` on the old container
# (SIGKILL after the default 10s). That doesn't give SQLite a
# chance to flush its WAL, which on 2026-07-30 left
# acl_snapshots + exit_rule_logs with btree page damage. The
# new docker-compose.yml (v0.32.4) sets stop_grace_period=30s
# + a /healthz-based healthcheck, but docker compose up
# --force-recreate IGNORES the healthcheck (it just sends
# SIGKILL). The fix is to do `docker compose stop` first —
# this respects both the grace period AND the healthcheck
# (it returns when the container exits the "healthy" state).
# Result: old container drains (Go's db.Close flushes WAL),
# then the new container starts.
#
# 2026-09-17 (B260): --force-recreate is REQUIRED here, not just
# nice-to-have — the extra_hosts block in docker-compose.yml is
# only applied at container creation time. A plain `restart`
# would re-load the new binary but keep the OLD extra_hosts,
# leaving the B260 probe broken even though the binary is up to
# date.
echo "[4/6] docker compose stop skygate (graceful, 30s grace), then up -d --force-recreate"
$SSH "cd $SKYGATE_HOST_REPO_PATH && sudo docker compose stop skygate 2>&1 | tail -3 && sudo docker compose up -d --force-recreate --no-deps skygate 2>&1 | tail -3"

# 5. Wait for /healthz
echo "[5/6] waiting for /healthz (up to 5 min)"
HEALTHY=0
for i in $(seq 1 60); do
  if $SSH 'curl -fsS http://localhost:8080/healthz >/dev/null 2>&1'; then
    echo "  healthy after ${i}x5s"
    HEALTHY=1
    break
  fi
  sleep 5
done
if [ "$HEALTHY" = "0" ]; then
  echo "  ERROR: /healthz did not return 200 within 5 min" >&2
  exit 4
fi

# 6. Print new build label
echo
echo "=== New build label ==="
$SSH 'curl -fsS http://localhost:8080/healthz'

# Suggest the next step
echo
echo "=== Done. Next: 'make verify-post' to confirm all 27 R-checks pass ==="
echo "        or 'make reconcile-snapshots' if R9 reports a policy divergence"
