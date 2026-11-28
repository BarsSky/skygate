// docker_swap.go — B532 (2026-11-27): the the three embedded /bin/sh swap scripts and their launcher, split out of docker.go.
//
// docker.go was 1 515 lines: the Docker upgrader's job runner AND its transport, its shell
// wrappers, its health poller, its ownership helpers and three embedded /bin/sh scripts.
// The audit's complaint was that a reader cannot find what a 1 500-line file does; each
// section keeps its code VERBATIM and its own doc comments, and the package is unchanged —
// no identifier moved, so no caller and no contract had to learn a new name.
package update

import (
	"context"
	"errors"
	"fmt"
	"os"
)

// swapHelperScript is the shell script body that runs
// INSIDE the v0.29.3.1 helper container. It's rendered
// to /data/skygate-swap-helper.sh by BOTH the success
// path and the rollback path's outer scripts, so the
// helper container can read it via a bind-mount and
// do the actual swap work in the HOST's PID namespace
// (which survives the OLD skygate container's removal).
//
// The helper does:
//  1. sleep 3s (orchestrator's state-file flush)
//  2. pick an alpine image (alpine:3.20 → alpine:latest)
//  3. `docker compose -p skygate -f $HOST_REPO/docker-compose.yml
//     up -d --force-recreate --no-deps skygate`
//  4. poll up to 60s for the new container; if it's
//     stuck in Created (rare compose race), call
//     `docker start <id>`
//  5. do a final healthz check via
//     `docker exec $NEW_ID wget -qO- http://localhost:8080/healthz`
//     (helper has --net=host so localhost:8080 IS the
//     new container's healthz)
//  6. exit
//
// The helper does NOT touch the state file. Promotion
// from "build_done" / "rolled_back" to "done" is the
// new orchestrator's job (via confirmPendingSwap on
// the next /admin/update page load). The helper's
// healthz check is defense in depth.
const swapHelperScript = `#!/bin/sh
set -e
LOG=/data/skygate-update-swap.log
STATE=/data/skygate-update-status.json
COMPOSE_PROJECT=skygate
SERVICE=skygate

log() { echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] HELPER $*" >> "$LOG"; }

log "helper container started (pid=$$), waiting 3s for orchestrator to flush"
sleep 3

# Install docker CLI in the alpine helper. The base
# alpine image has no docker binary; we need docker
# + docker compose (subcommand) to drive the swap.
# 'apk add --no-cache' requires network access to
# the alpine package repo. This adds ~5s to the
# swap total but is a clean fix; alternative is to
# bake a custom image (skygate-swap-helper:VERSION)
# with docker pre-installed, but that's more
# infrastructure to maintain.
log "installing docker-cli + docker-cli-compose in helper"
apk add --no-cache docker-cli docker-cli-compose 2>&1 | tail -3 >> "$LOG"
if ! command -v docker > /dev/null; then
  log "FATAL: docker not found after apk add"
  exit 1
fi
log "docker $(docker --version) installed"

# The orchestrator passes SKYGATE_HOST_REPO_PATH (the
# host path the skygate service is bind-mounted from)
# via env. Inside the helper container, that path is
# bind-mounted at /host_repo (read-only). The helper
# uses the in-container mountpoint, NOT the host path,
# because the host path doesn't exist in the helper's
# filesystem.
HELPER_HOST_REPO="/host_repo"
log "helper repo mountpoint: $HELPER_HOST_REPO"

log "running: docker compose -p $COMPOSE_PROJECT -f $HELPER_HOST_REPO/docker-compose.yml up -d --force-recreate --no-deps $SERVICE"
# (single-quoted in this comment; the shell doesn't care,
# the Go raw string concat does)
cd "$HELPER_HOST_REPO"
docker compose -p "$COMPOSE_PROJECT" up -d --force-recreate --no-deps "$SERVICE" >> "$LOG" 2>&1
RC=$?
log "docker compose up returned rc=$RC"
if [ $RC -ne 0 ]; then
  log "compose up FAILED — leaving state at build_done for operator"
  exit $RC
fi

# Even on rc=0, the new container can be in Created state
# (race with --no-deps + old container's PID 1 SIGTERM).
# Poll for up to 60s and start the new container if stuck.
log "polling for new container to reach Running state"
for i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30; do
  NEW_ID=$(docker ps -a --filter "label=com.docker.compose.service=$SERVICE" --filter "label=com.docker.compose.project=$COMPOSE_PROJECT" --format "{{.ID}}" 2>/dev/null | head -1)
  if [ -n "$NEW_ID" ]; then
    STATUS=$(docker inspect "$NEW_ID" --format "{{.State.Status}}" 2>/dev/null)
    log "  attempt $i: new=$NEW_ID status=$STATUS"
    if [ "$STATUS" = "created" ]; then
      log "  starting stuck container $NEW_ID"
      docker start "$NEW_ID" >> "$LOG" 2>&1
      log "  start rc=$?"
    elif [ "$STATUS" = "running" ]; then
      log "  container is running, success"
      break
    fi
  else
    log "  attempt $i: no new container found yet"
  fi
  sleep 2
done

# Final healthz check (defense in depth). The new
# orchestrator's confirmPendingSwap is the primary
# /healthz poller, but the helper does a final check
# to log whether the swap is fully verified before
# it exits.
NEW_ID=$(docker ps -a --filter "label=com.docker.compose.service=$SERVICE" --filter "label=com.docker.compose.project=$COMPOSE_PROJECT" --format "{{.ID}}" 2>/dev/null | head -1)
if [ -n "$NEW_ID" ]; then
  STATUS=$(docker inspect "$NEW_ID" --format "{{.State.Status}}" 2>/dev/null)
  log "final status: $STATUS"
  if [ "$STATUS" = "running" ]; then
    log "healthz check via docker exec"
    HEALTHZ=$(docker exec "$NEW_ID" wget -qO- http://localhost:8080/healthz 2>/dev/null || true)
    log "  healthz response: $HEALTHZ"
  fi
fi

log "helper container exiting"
exit 0
`

// (writeSwapHelperScript was removed in v0.34 — the v0.29.3.1
// swap pipeline writes the helper script via a different path:
// the swapSubprocessScript below shells out to a HELPER
// CONTAINER with the script inlined, so the Go-level wrapper
// was never called.)

// swapSubprocessScript is the shell script that the v0.29.3
// detached subprocess runs. It is rendered to
// /data/skygate-swap.sh at job start and invoked via
// `/bin/sh /data/skygate-swap.sh` in a Setsid-detached
// session.
//
// v0.29.3.1: the script's first version ran `docker compose
// up --force-recreate` from inside the OLD skygate
// container. That worked in principle but had a fatal
// race: the subprocess lives in the OLD container's PID
// namespace, and when compose sends SIGTERM to PID 1 of
// the OLD container, that signal propagates to all
// processes in the same namespace, killing the subprocess
// mid-way through `docker compose up`. The new container
// would end up in `Created` state forever and the operator
// had to `docker start <id>` by hand. Live-verified on the
// VM at 2026-07-28 10:45 UTC: the swap log showed only
// "Recreate" before the subprocess died, and the new
// container `fb9547ead806` was stuck in `Created`.
//
// v0.29.3.1 fix: instead of running the swap directly,
// the script writes /data/skygate-swap-helper.sh (via
// writeSwapHelperScript in Go) and spawns a HELPER
// CONTAINER via `docker run --rm --pid=host --net=host ...`.
// The helper uses the HOST's PID namespace, so it survives
// the OLD skygate container's removal (its process is
// not in OLD's namespace). The helper does the actual
// `docker compose up` + start-stuck-container + healthz
// verify. The old orchestrator's script just spawns
// the helper and exits — the helper owns the rest of
// the lifecycle.
//
// Cost: ~1 extra container per update. The helper
// auto-removes itself (`--rm`). Image: `alpine:3.20`
// (already on the host for `app-skygate` and other
// internal use). Falls back to `alpine:latest` if
// 3.20 isn't pulled.
//
// The script does NOT do healthz polling or state-file
// updates itself — the helper does those. The state file
// is left at "build_done" until the helper finishes (and
// until the new orchestrator's renderUpdatePage promotes
// it to "done" via confirmPendingSwap).
//
// The script does try to log its progress to
// /data/skygate-update-swap.log so the operator can debug
// a stuck swap post-hoc.
const swapSubprocessScript = `#!/bin/sh
STATE=/data/skygate-update-status.json
LOG=/data/skygate-update-swap.log
COMPOSE_PROJECT=skygate

log() { echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*" >> "$LOG"; }

log "swap subprocess started (pid=$$) — spawning helper container"

# Helper script was written by Go (writeSwapHelperScript)
# at job start. Re-write defensively in case the
# rollback path got here first and the file was lost.
cat > /data/skygate-swap-helper.sh << 'HELPER_EOF'
` + swapHelperScript + `HELPER_EOF
chmod +x /data/skygate-swap-helper.sh

# B393: the helper MUST mount the same /data this script just wrote to.
# Go resolves it from the live container's own /data mount (a named volume
# -> the volume name, a bind -> the absolute host path) and substitutes it
# for the placeholder below. Before B393 this line was the literal
# "skygate-data", a legacy volume that is empty on a bind-mounted install:
# the helper mounted the wrong filesystem, could not read the script, and
# the failure stayed buried in this log while the panel reported success.
DATA_MOUNT=__SKYGATE_DATA_MOUNT__
HELPER_IMAGE="${SKYGATE_SWAP_HELPER_IMAGE:-alpine:3.20}"
log "resolved data mount: $DATA_MOUNT (helper image $HELPER_IMAGE)"

# Pre-flight (B393 F2/F5): prove the helper can SEE the script through the
# resolved mount BEFORE spawning it. A wrong mount is now a loud, immediate
# failure with the mount value named, not a silent 127 two seconds later.
if ! docker image inspect "$HELPER_IMAGE" > /dev/null 2>&1; then
  log "helper image $HELPER_IMAGE not present locally — pulling"
  docker pull "$HELPER_IMAGE" >> "$LOG" 2>&1 || log "WARN: docker pull $HELPER_IMAGE failed; the spawn below will retry"
fi
if ! docker run --rm -v "$DATA_MOUNT:/data:ro" "$HELPER_IMAGE" /bin/sh -c 'test -s /data/skygate-swap-helper.sh' >> "$LOG" 2>&1; then
  log "FAILED: the helper cannot see /data/skygate-swap-helper.sh through the resolved mount '$DATA_MOUNT' — nothing was swapped and the panel is still running the old build"
  log "FAILED: fix the /data mount (or set SKYGATE_DATA_MOUNT) and apply the update again; break-glass on the host: docker compose up -d --force-recreate --no-deps skygate"
  exit 1
fi
log "pre-flight ok: the helper sees the script through $DATA_MOUNT"

# Spawn helper container in HOST PID namespace. Uses
# --net=host so localhost:8080 inside the helper IS
# the new skygate container's healthz. Uses bind-mount
# for the swap helper script + state file. Uses
# --rm so it self-cleans. Runs in background (we exit
# immediately, helper keeps going).
SKYGATE_HOST_REPO_PATH="${SKYGATE_HOST_REPO_PATH:-/home/admin/skygate}"
log "spawning helper container"
docker run --rm \
  --pid=host \
  --net=host \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$SKYGATE_HOST_REPO_PATH:/host_repo:ro" \
  -v "$DATA_MOUNT:/data" \
  -e SKYGATE_HOST_REPO_PATH="$SKYGATE_HOST_REPO_PATH" \
  --name skygate-swap-helper \
  "$HELPER_IMAGE" \
  /bin/sh /data/skygate-swap-helper.sh >> "$LOG" 2>&1 &
HELPER_PID=$!
disown $HELPER_PID 2>/dev/null || true
log "helper container spawned (pid=$HELPER_PID), parent script exiting"
exit 0
`

// rollbackSwapScriptTemplate is the rollback sibling of
// swapSubprocessScript: same helper container, same resolved /data mount
// (B393), different log prefix and helper name.
//
// The mount is the placeholder __SKYGATE_DATA_MOUNT__, substituted by
// renderSwapScript() from the live container's own /data mount — the two
// scripts deliberately share one resolver so a future change cannot fix one
// path and leave the other mounting the wrong filesystem.
const rollbackSwapScriptTemplate = `#!/bin/sh
STATE=/data/skygate-update-status.json
LOG=/data/skygate-update-swap.log
COMPOSE_PROJECT=skygate

log() { echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] ROLLBACK $*" >> "$LOG"; }

log "rollback swap subprocess started (pid=$$) — spawning helper container"

# Helper script — same body as the success path. Inline
# here so the rollback works even if the Go-level
# writeSwapHelperScript never ran.
cat > /data/skygate-swap-helper.sh << 'HELPER_EOF'
` + swapHelperScript + `HELPER_EOF
chmod +x /data/skygate-swap-helper.sh

DATA_MOUNT=__SKYGATE_DATA_MOUNT__
HELPER_IMAGE="${SKYGATE_SWAP_HELPER_IMAGE:-alpine:3.20}"
log "resolved data mount: $DATA_MOUNT (helper image $HELPER_IMAGE)"

# Same pre-flight as the success path (B393 F2/F5): a rollback that cannot
# even start the helper must say so, because the operator is by definition
# already in a bad state.
if ! docker image inspect "$HELPER_IMAGE" > /dev/null 2>&1; then
  log "helper image $HELPER_IMAGE not present locally — pulling"
  docker pull "$HELPER_IMAGE" >> "$LOG" 2>&1 || log "WARN: docker pull $HELPER_IMAGE failed; the spawn below will retry"
fi
if ! docker run --rm -v "$DATA_MOUNT:/data:ro" "$HELPER_IMAGE" /bin/sh -c 'test -s /data/skygate-swap-helper.sh' >> "$LOG" 2>&1; then
  log "FAILED: the rollback helper cannot see /data/skygate-swap-helper.sh through the resolved mount '$DATA_MOUNT' — the previous build was NOT restored"
  log "FAILED: restore it by hand on the host: git checkout skygate-pre-update-<sha> && docker compose up -d --force-recreate --no-deps skygate"
  exit 1
fi
log "pre-flight ok: the helper sees the script through $DATA_MOUNT"

SKYGATE_HOST_REPO_PATH="${SKYGATE_HOST_REPO_PATH:-/home/admin/skygate}"
log "spawning helper container"
docker run --rm \
  --pid=host \
  --net=host \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$SKYGATE_HOST_REPO_PATH:/host_repo:ro" \
  -v "$DATA_MOUNT:/data" \
  -e SKYGATE_HOST_REPO_PATH="$SKYGATE_HOST_REPO_PATH" \
  --name skygate-rollback-helper \
  "$HELPER_IMAGE" \
  /bin/sh /data/skygate-swap-helper.sh >> "$LOG" 2>&1 &
HELPER_PID=$!
disown $HELPER_PID 2>/dev/null || true
log "helper container spawned (pid=$HELPER_PID), parent script exiting"
exit 0
`

// spawnSwapSubprocess writes the swap script to /data/skygate-swap.sh, marks
// it executable, and launches it via runShellDetached. The subprocess runs the
// swap and verify (see swapSubprocessScript) and updates the state file
// accordingly. The orchestrator returns immediately after Start() — its
// goroutine is done, the subprocess continues independently.
//
// B393: the script is rendered with the /data mount resolved from the live
// container (resolveDataMount), and the id of the container that is about to
// be recreated is persisted first — the new process needs it to prove that a
// swap actually happened.
func (u *DockerUpgrader) spawnSwapSubprocess(ctx context.Context) error {
	scriptPath := "/data/skygate-swap.sh"
	dataMount, mountKind := u.resolveDataMount(ctx)
	u.State.Log(LogInfo, fmt.Sprintf("swap: /data mount = %s (%s)", dataMount, mountKind))
	if prev := CurrentContainerID(ctx); prev != "" {
		u.State.SetSwapFromContainer(prev)
		u.State.Log(LogInfo, "swap: pre-update container id = "+shortID(prev))
	} else {
		u.State.Log(LogWarn, "swap: could not record the pre-update container id (docker unavailable) — the build label will be the only witness")
	}
	if err := os.WriteFile(scriptPath, []byte(renderSwapScript(swapSubprocessScript, dataMount)), 0755); err != nil {
		return fmt.Errorf("write swap script: %w", err)
	}
	u.State.Log(LogDebug, "wrote swap script to "+scriptPath)
	// Launch via `/bin/sh` (Alpine's default ash) rather
	// than exec'ing the script directly so the shebang
	// is not required (the bind-mounted /data may not
	// be executable on every host despite our WriteFile
	// mode). /bin/sh is the standard shell on every Unix
	// — it's in every skygate image variant (alpine,
	// debian-slim, distroless) and is the same shell the
	// skygate entrypoint.sh uses.
	return u.runShellDetached(context.Background(), "/bin/sh", scriptPath)
}

// ErrAlreadyInProgress is returned by the handler when a
// previous update job is still running. The page surfaces
// this with a "wait for the in-flight job to finish" hint.
var ErrAlreadyInProgress = errors.New("update: another job is already in progress")
