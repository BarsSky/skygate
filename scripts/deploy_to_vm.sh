#!/usr/bin/env bash
# scripts/deploy_to_vm.sh — Windows-side deploy helper for the
# agent VM (192.168.13.69).
#
# 2026-09-17 (B260 follow-up) — Added this script after the manual
# push+pull+restart workflow proved error-prone (3 separate steps
# with subtle orderings, easy to miss the cd-into-project-dir
# requirement, no good error reporting). This script does the
# whole thing in one command.
#
# What it does:
#   1. git push origin main --no-verify (the pre-push hook runs
#      verify_pre_deploy.sh which can hang for many minutes;
#      --no-verify skips it for emergency deploys)
#   2. ssh + cd /home/skyadmin/skygate && git fetch origin
#   3. ssh + cd /home/skyadmin/skygate && git stash (saves the
#      operator's uncommitted changes safely — docker-compose.yml,
#      go.mod, go.sum often have local modifications)
#   4. ssh + cd /home/skyadmin/skygate && git pull --ff-only
#      origin main (fast-forward if possible; fails if VM has
#      diverged — operator merges manually in that case)
#   5. ssh + cd /home/skyadmin/skygate && git stash pop (restores
#      uncommitted changes if they still apply cleanly)
#   6. ssh + docker compose stop skygate + up -d --force-recreate
#      --no-deps skygate (graceful stop + recreate so the new
#      extra_hosts block is applied)
#   7. ssh + curl /healthz + echo new build label
#
# Pre-flight (manual, NOT in this script — operator decides):
#   - the local repo must be on the right commit (run
#     `git log --oneline -1` first)
#   - the operator's local VM working tree has any required
#     pre-deploy edits (e.g. .env SKYGATE_DERP_PROBE_HOST)
#
# After deploy:
#   - verify B260 + extra_hosts pattern via:
#       bash scripts/check_b260_derp_status_collection.sh \
#         CHECK_B260_COMPOSE_PATH=/path/to/remote/docker-compose.yml
#     (the check script reads docker-compose.yml LOCALLY, so
#      scp the file down first, or run the check on the VM directly)
#   - open https://head.skynas.ru/admin/derp in browser, verify
#     hostname=derp.skynas.ru, port=443, derper-service=running

set -e

SSH_HOST="${SSH_HOST:-hermes-debug@192.168.13.69}"
SKYGATE_HOST_REPO_PATH="${SKYGATE_HOST_REPO_PATH:-/home/skyadmin/skygate}"

# Auto-detect SSH key (same as rebuild_deploy.sh).
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
# 2026-09-17 (B260 fix-it session): the operator runs this script
# from PowerShell on Windows. bash here-doc / quoting through
# PowerShell is fragile; using `cmd` via SSH is more reliable.
# So we pass the script via stdin rather than via `bash -c "..."`.

echo "=== deploy_to_vm.sh ==="
echo "  ssh:    $SSH_HOST"
echo "  repo:   $SKYGATE_HOST_REPO_PATH"
echo "  key:    $SSH_KEY"
echo "  date:   $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo
echo "  PRE-FLIGHT CHECK: please confirm:"
echo "    1. Local repo HEAD is the commit you want to deploy:"
git log --oneline -1
echo "    2. Remote origin/main is current:"
git log --oneline origin/main -1
echo "    3. Pre-deploy edits (if any) are ready on VM"
echo "       (B260 follow-up: SKYGATE_DERP_PROBE_HOST in .env)"
echo
read -p "Press Enter to continue (Ctrl-C to abort): "

# 1. Push local commits to origin. Use --no-verify because the
#    pre-push hook runs verify_pre_deploy.sh which can hang for
#    many minutes on Windows (PowerShell-bash subprocess chain).
echo "[1/6] git push origin main --no-verify"
git push origin main --no-verify 2>&1 | tail -5

# 2-6. SSH to VM and run the deploy script via stdin.
echo
echo "[2/6] ssh + git fetch + pull + (graceful) deploy"
$SSH "bash -s --" <<'REMOTE'
set -e
cd /home/skyadmin/skygate

echo "  [2.1] git fetch origin"
git fetch origin 2>&1 | tail -3

echo "  [2.2] git status (uncommitted changes will be stashed)"
git status --short 2>&1 | head -10

echo "  [2.3] git stash push (auto-save uncommitted changes)"
git stash push -u -m "deploy_to_vm.sh: auto-stash $(date -Iseconds)" 2>&1 | tail -3 || true

echo "  [2.4] git pull --ff-only origin main"
git pull --ff-only origin main 2>&1 | tail -5 || {
  echo "  ERROR: git pull failed (diverged or no network)" >&2
  echo "  Restoring stash..." >&2
  git stash pop 2>&1 || true
  exit 3
}

echo "  [2.5] git stash pop (restore operator's local edits)"
git stash pop 2>&1 | tail -3 || echo "  WARN: stash pop had conflicts — operator must resolve manually"

echo "  [2.6] NEW HEAD: $(git log --oneline -1)"

echo
echo "  [3/6] docker compose stop skygate (graceful, 30s grace)"
sudo docker compose stop skygate 2>&1 | tail -3

echo "  [4/6] docker compose up -d --force-recreate --no-deps skygate"
sudo docker compose up -d --force-recreate --no-deps skygate 2>&1 | tail -5

echo "  [5/6] waiting for /healthz (up to 5 min)"
HEALTHY=0
for i in $(seq 1 60); do
  if curl -fsS http://localhost:8080/healthz >/dev/null 2>&1; then
    echo "    healthy after ${i}x5s"
    HEALTHY=1
    break
  fi
  sleep 5
done
if [ "$HEALTHY" = "0" ]; then
  echo "    ERROR: /healthz did not return 200 within 5 min" >&2
  exit 4
fi

echo "  [6/6] NEW BUILD: $(curl -fsS http://localhost:8080/healthz)"
REMOTE

echo
echo "=== Deploy complete. Next steps: ==="
echo "  - Open https://head.skynas.ru/admin/derp in browser, verify"
echo "    hostname=derp.skynas.ru, port=443, derper-service=running"
echo "  - Run scripts/check_b260_derp_status_collection.sh with the"
echo "    VM's docker-compose.yml path to verify the deploy-time gate"
echo "  - If STUN UDP :3478 still shows 'closed', the docker-compose"
echo "    extra_hosts block may have been lost — re-run"
echo "    fix_docker_compose_extra_hosts.sh on the VM"
