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
#
# SSH key auto-detected from ~/.ssh/id_ed25519 or
# /c/Users/<user>/.ssh/id_ed25519 (Git Bash from Windows).
# Override with SSH_KEY=/path/to/key.
#
# 2026-07-30: extracted from the manual procedure in AGENTS.md.
# Earlier sessions did this inline (4 separate bash invocations).
# Now one command covers the whole flow.

set -e

SSH_HOST="${SSH_HOST:-admin@192.0.2.1}"

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
echo "  key:    $SSH_KEY"
echo "  date:   $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo

# 1. Fix root-owned tailscale dirs (the container tailscaled runs
#    as root; the bind-mount data/ts/ can get re-owned on restart).
$SSH 'sudo chown -R admin:admin /home/admin/skygate/data/ts/ 2>/dev/null || true' || true
echo "[1/5] chown data/ts/ (silent if no permission denied)"

# 2. git pull
echo "[2/5] git pull --ff-only"
$SSH 'cd /home/admin/skygate && git pull --ff-only' || {
  echo "  ERROR: git pull failed (probably diverged or no network)" >&2
  exit 3
}

# 3. Build new image
echo "[3/5] docker compose build skygate (3-5 min)"
$SSH 'cd /home/admin/skygate && sudo docker compose build skygate 2>&1 | tail -5'

# 4. Recreate container
echo "[4/5] docker compose up -d --force-recreate --no-deps skygate"
$SSH 'cd /home/admin/skygate && sudo docker compose up -d --force-recreate --no-deps skygate 2>&1 | tail -3'

# 5. Wait for /healthz
echo "[5/5] waiting for /healthz (up to 5 min)"
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
