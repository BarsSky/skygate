#!/usr/bin/env bash
#===============================================================================
# Skygate Dependency Upgrade
#
# Workflow:
#   1. Create upgrade/<ts> branch from main
#   2. go get -u ./...
#   3. go mod tidy
#   4. go test ./internal/...    (must pass — bumps exit 1 on FAIL)
#   5. go build -o /tmp/skygate-upgrade  (sanity build)
#   6. On success: commit, fast-forward merge into main, docker restart skygate
#   7. On any failure: leave branch in place, notify admin with --severity=fail
#   8. On success: also notify admin with --severity=ok
#
# Usage:
#   ./upgrade.sh                 — upgrade everything (default)
#   ./upgrade.sh --dry-run       — go through the motions, do not merge/restart
#   ./upgrade.sh <pkg> [<pkg>…]  — upgrade only the named module(s)
#===============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
SKYGATE_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
NOTIFY_SH="${SCRIPT_DIR}/notify.sh"
TODAY="$(date -u +%Y%m%d-%H%M%S)"
BRANCH="upgrade/${TODAY}"
DRY_RUN=0

if [[ "${1:-}" == "--dry-run" ]]; then
  DRY_RUN=1; shift
fi

PKGS=("$@")
if [[ ${#PKGS[@]} -eq 0 ]]; then
  PKGS=(./...)
fi

cd "${SKYGATE_DIR}"

# Verify clean tree (configurable: skip if SKYGATE_UPGRADE_ALLOW_DIRTY=1).
if [[ "${SKYGATE_UPGRADE_ALLOW_DIRTY:-0}" != "1" ]]; then
  if ! git diff --quiet --ignore-submodules HEAD 2>/dev/null; then
    echo "FATAL: working tree has unstaged changes. Commit/stash or set SKYGATE_UPGRADE_ALLOW_DIRTY=1" >&2
    git status --short
    exit 3
  fi
  if ! git diff --cached --quiet --ignore-submodules HEAD 2>/dev/null; then
    echo "FATAL: working tree has staged but uncommitted changes." >&2
    git status --short
    exit 3
  fi
fi

# Save head sha to compare at end.
HEAD_SHA=$(git rev-parse HEAD)
HEAD_SUBJECT=$(git log -1 --pretty=%s)

notify() {
  local severity="$1"; shift
  if [[ -x "${NOTIFY_SH}" ]]; then
    "${NOTIFY_SH}" --severity="${severity}" "$@" || true
  fi
}

cleanup() {
  local rc=$?
  if [[ "${DRY_RUN}" -eq 1 ]]; then
    echo "(dry-run) branch ${BRANCH} retained for inspection"
    exit "${rc}"
  fi
  if (( rc != 0 )); then
    notify "fail" "skygate upgrade FAIL (${BRANCH})" \
      "branch preserved: ${BRANCH}
head_before=${HEAD_SHA} (${HEAD_SUBJECT})
exit=${rc}
inspect: cd ${SKYGATE_DIR} && git log ${BRANCH}"
    echo "FAIL: branch ${BRANCH} preserved, see notify log"
  fi
  exit "${rc}"
}
trap cleanup ERR

# -----------------------------------------------------------------------------
echo "=== skygate upgrade $(date -u +%FT%TZ) ==="
echo "branch=${BRANCH}"
echo "pkgs=${PKGS[*]}"
echo "head_before=${HEAD_SHA} (${HEAD_SUBJECT})"
echo "dry_run=${DRY_RUN}"

# 1. Branch
git checkout -b "${BRANCH}" main

# 2. Upgrade
echo
echo "--- go get -u ${PKGS[*]} ---"
GOTOOLCHAIN=local go get -u "${PKGS[@]}" 2>&1 | tail -30

echo
echo "--- go mod tidy ---"
GOTOOLCHAIN=local go mod tidy 2>&1 | tail -10

# Show diff summary
echo
echo "--- changes in go.mod / go.sum ---"
git diff --stat go.mod go.sum

# 3. Test
echo
echo "--- go test ./internal/... ---"
GOTOOLCHAIN=local go test -count=1 ./internal/... 2>&1 | tail -40

# 4. Build sanity (does not affect deployed image)
echo
echo "--- build sanity ---"
GOTOOLCHAIN=local go build -o /tmp/skygate-upgrade ./cmd/skygate 2>&1 | tail -10
ls -la /tmp/skygate-upgrade 2>&1 | head -1

# 5. Commit
echo
echo "--- commit upgrade ---"
git add go.mod go.sum
git -c user.email=upgrade@skygate -c user.name=skygate-upgrade \
    commit -m "chore(deps): upgrade on ${TODAY}

go get -u ${PKGS[*]}
go mod tidy
all tests pass" --quiet
NEW_SHA=$(git rev-parse HEAD)

if [[ "${DRY_RUN}" -eq 1 ]]; then
  echo "(dry-run) stopping before merge/restart. branch=${BRANCH}"
  exit 0
fi

# 6. Merge to main
echo
echo "--- merge into main ---"
git checkout main
git merge --ff-only "${BRANCH}"
MERGED_SHA=$(git rev-parse HEAD)

# 7. Restart skygate container so it picks up new deps (it rebuilds via entrypoint)
echo
echo "--- docker restart skygate ---"
docker restart 0c8931e2a82a >/dev/null 2>&1 &
RESTART_PID=$!
sleep 10
if docker ps --format '{{.Names}}' | grep -q '^skygate$'; then
  UP=$(docker inspect --format '{{.State.Status}}' 0c8931e2a82a 2>/dev/null || echo "down")
  echo "  container status: ${UP}"
else
  echo "  WARN: skygate container not running"
fi

# 8. Verify HTTP
echo
echo "--- HTTP check ---"
sleep 5
HTTP=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 10 http://localhost:8080/login || echo 000)
echo "  /login → ${HTTP}"

if [[ "${HTTP}" == "200" ]]; then
  echo
  echo "=== UPGRADE OK ==="
  echo "branch=${BRANCH} merged_to=${MERGED_SHA}"
  notify "ok" "skygate upgrade OK (${TODAY})" \
    "branch=${BRANCH}
merged_to=${MERGED_SHA}
pkgs=${PKGS[*]}
http=/login→${HTTP}"
  exit 0
fi

echo
echo "=== UPGRADE POST-MERGE WARNING: HTTP ${HTTP} ==="
notify "fail" "skygate upgrade post-merge HTTP ${HTTP}" \
  "branch=${BRANCH}
merged_to=${MERGED_SHA}
post-merge HTTP check failed: /login→${HTTP}
container logs: docker logs skygate --tail=50"
exit 4
