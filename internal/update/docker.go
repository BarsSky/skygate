package update

// docker.go — v0.29.0 self-update orchestrator for the Docker
// install kind. This is the path the operator's VM uses.
//
// State machine (mirrors docs/plans/self-update-v0.29.md):
//
//   pending → backup → pull_build → migrate → swap → verify → done
//                ↓         ↓           ↓       ↓       ↓
//                └─────────┴───────────┴───────┴───────┴─→ failed
//
// Each phase:
//   1. Logs a starting line ("starting <phase>")
//   2. Runs the phase's shell command(s) via exec.Command
//   3. On success, sets the new phase + logs a done line
//   4. On failure, calls rollback() and transitions to failed
//
// The rollback restores the previous version (via a git tag
// captured at the backup phase) and re-builds + restarts the
// container. The orchestrator always re-checks the rolled-back
// state at the end and reports whether rollback itself
// succeeded — if it didn't, the operator gets a clear "manual
// intervention required, here's what to do" hint.
//
// The orchestrator runs in a goroutine, NOT a synchronous
// HTTP handler, because the full update takes 3-5 minutes
// (git pull + docker compose build + container recreate +
// healthz poll). The HTTP handler returns http.StatusSeeOther to the page
// within 100ms; the page auto-refreshes every 5s while
// a job is in progress.
//
// The orchestrator's parent (the App) keeps a reference
// to the in-flight *State via the StateStore. The page reads
// from the same store on every request. This is simpler
// than SSE (no streaming infra needed) and fine for the
// single-operator surface.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// DockerUpgrader runs the Docker install kind update.
type DockerUpgrader struct {
	// SwapLogPath is the path to the swap subprocess log
	// file. Defaults to /data/skygate-update-swap.log (the
	// in-container /data bind-mount target). Tests override
	// it to a temp dir.
	SwapLogPath string
	// RepoPath is the path to the skygate source repo.
	//
	// When running inside the skygate container (the
	// orchestrator IS a goroutine inside the skygate HTTP
	// server, started by the container's entrypoint),
	// RepoPath is the IN-CONTAINER path of the bind-mount
	// of the source dir — typically /app (per
	// docker-compose.yml's `./:/app`). The host's
	// /home/admin/skygate is unreachable from inside
	// the container; passing that path would make
	// `cmd.Dir` fail with "no such file or directory".
	//
	// On a bare/systemd host (orchestrator running as
	// skygate's system service), RepoPath is the host
	// path — typically /home/admin/skygate.
	RepoPath string
	// ComposeCmd is the docker compose invocation. Defaults
	// to "docker compose". Operators with podman-compose
	// or docker-compose v1 can override.
	ComposeCmd string
	// ComposeProject is the project name passed to
	// `docker compose -p <name>`. Defaults to "skygate"
	// (the operator's standard). Must match the project
	// name the named volumes were created under —
	// otherwise `docker compose up` refuses to use them
	// with "volume X already exists but was created
	// for project Y (expected Z)".
	//
	// Why this matters: docker compose computes the
	// project name from the basename of the working
	// directory by default. Inside the container the
	// orchestrator runs from /app (basename "app") while
	// the host's compose runs from /home/admin/skygate
	// (basename "skygate"). The named volume
	// `skygate-data` was created with project label
	// "skygate" the first time the host started the
	// stack, and the in-container `docker compose up`
	// looks for it under "app" — mismatch → refuse.
	//
	// Override via SKYGATE_COMPOSE_PROJECT env var
	// (e.g. for an operator who renamed their project
	// to "sky" or moved the source dir to /opt/sky).
	ComposeProject string
	// State is the shared state store. The orchestrator
	// updates phase + log on every transition; the page
	// reads the latest state on every reload.
	State *StateStore
	// CurrentVersion is the build label at job start. The
	// orchestrator uses it for the "starting from" line
	// and the audit log.
	CurrentVersion string
	// hostOwner is the uid:gid of files on the host
	// (e.g. "1000:1000" for admin on a standard
	// Ubuntu VM). The orchestrator captures it once
	// at job start, then uses it to chown the bind-mount
	// back to the host owner after every `git` mutation.
	// Without this, files written by the orchestrator
	// (running as root inside the container) end up
	// root:root on the host, breaking the operator's
	// `git pull` + `make test` from the host shell.
	//
	// Set via SKYGATE_HOST_OWNER env var, or auto-
	// detected on the first chown call via
	// `stat -c '%u:%g' <RepoPath>/.git/HEAD` (a file
	// the host's git created, so its owner is the
	// host owner).
	hostOwner string
}

// NewDockerUpgrader returns a DockerUpgrader with sensible
// defaults for the operator's VM shape.
func NewDockerUpgrader(repoPath string, state *StateStore, currentVersion string) *DockerUpgrader {
	project := os.Getenv("SKYGATE_COMPOSE_PROJECT")
	if project == "" {
		project = "skygate"
	}
	return &DockerUpgrader{
		RepoPath:        repoPath,
		ComposeCmd:      "docker compose",
		ComposeProject:  project,
		State:           state,
		CurrentVersion:  currentVersion,
	}
}

// Run executes the full update sequence. ctx carries the
// HTTP request's context; if the client disconnects, ctx
// is cancelled and the orchestrator aborts the in-flight
// shell command (the in-progress phase fails → triggers
// rollback → state ends at "failed" with the rollback log).
//
// Run is intended to be called from a goroutine. The HTTP
// handler kicks it off and returns http.StatusSeeOther to the page.
func (u *DockerUpgrader) Run(ctx context.Context, target string) {
	u.State.Log(LogInfo, fmt.Sprintf("starting update %s → %s", u.CurrentVersion, target))

	// Capture the host owner BEFORE any git mutation.
	// Once `git` runs as root inside the container, every
	// file in the bind-mount (including .git/HEAD) becomes
	// root:root, so we can no longer recover the original
	// host owner via stat. The captured value is used by
	// chownToHostOwner() to restore ownership at the end of
	// the build phase (so the operator's next `git pull` /
	// `make test` from the host shell still works).
	if _, err := u.detectHostOwner(ctx); err != nil {
		// Non-fatal: the build will still work, the
		// orchestrator just won't be able to chown the
		// host files back. Log a warning and continue.
		u.State.Log(LogWarn, "host owner detection failed: "+err.Error()+
			" — files on the host may end up root:root after the update")
	} else {
		u.State.Log(LogDebug, fmt.Sprintf("host owner captured: %s", u.hostOwner))
	}

	// Phase 1: backup. Tag the current commit so the
	// rollback can checkout it later. Cheap, fast.
	u.State.SetPhase(PhaseBackup, "tagging current commit as skygate-pre-update")
	backupTag := fmt.Sprintf("skygate-pre-update-%s", shortSHA(u.CurrentVersion))
	if err := u.runGit(ctx, "tag", "-f", backupTag); err != nil {
		u.failWithRollback(ctx, fmt.Errorf("backup tag: %w", err), backupTag)
		return
	}
	u.State.Log(LogInfo, "backup tag created: "+backupTag)

	// Phase 2: pull + build. `git fetch --tags --prune --force`
	// + `git checkout <target>` + `docker compose build skygate`.
	//
	// 2026-07-30: v0.32.6 — added `--force` to the fetch.
	// Without it, `git fetch --tags` refuses to overwrite a
	// local tag whose SHA diverges from the remote's tag with
	// the same name (the "would clobber existing tag" reject).
	// This happens whenever:
	//   - the operator's local VM has a stale tag (e.g. from
	//     a `git fetch` before the tag was force-pushed on
	//     origin to point at a different commit)
	//   - the orchestrator runs in a freshly-cloned repo that
	//     had different local SHAs at clone time
	// The result was a hard exit-status-1, the orchestrator
	// treated it as a fetch failure, and triggered an automatic
	// rollback (see /data/skygate-update-swap.log for the
	// 2026-07-28 ROLLBACK storm).
	//
	// `--force` only affects remote-tracking refs and tags
	// (NOT local branches), and only overwrites refs whose
	// NAME matches the remote — it's the standard fix for
	// stale local tags pointing at orphaned commits. The
	// local commits are still in the object database (until
	// GC); only the tag POINTER gets corrected.
	u.State.SetPhase(PhasePullBuild, "fetching target and rebuilding image")
	if err := u.runGit(ctx, "fetch", "--tags", "--prune", "--force"); err != nil {
		u.failWithRollback(ctx, fmt.Errorf("git fetch: %w", err), backupTag)
		return
	}
	if err := u.runGit(ctx, "checkout", target); err != nil {
		u.failWithRollback(ctx, fmt.Errorf("git checkout: %w", err), backupTag)
		return
	}
	// Chown the data/ts/ directory before the build. The
	// build step copies the source into the image; the
	// container will then write its own tailscale state
	// into the bind-mounted dir on first start. The
	// chownToHostOwner helper uses the captured host
	// owner (see top of Run) — it works whether we're
	// inside the skygate container (chown directly) or
	// on a bare host (the captured uid:gid is still
	// valid; chown doesn't need sudo as root).
	if err := u.chownToHostOwner(ctx, u.RepoPath+"/data/ts"); err != nil {
		u.failWithRollback(ctx, fmt.Errorf("chown data/ts: %w", err), backupTag)
		return
	}
	if out, err := u.runCompose(ctx, "build", "skygate"); err != nil {
		u.failWithRollback(ctx, fmt.Errorf("docker compose build: %w (output: %s)", err, truncateOutput(out, 200)), backupTag)
		return
	}
	// After the build, restore the source tree to the
	// host owner. `docker compose build` reads /app as
	// root (we are root inside the container); it doesn't
	// modify source files, but a parallel `git checkout`
	// earlier in the phase did — those files are now
	// root:root on the host bind-mount. Without this
	// chown, the operator's next `git pull` from the host
	// shell would fail with "Permission denied".
	if u.hostOwner != "" {
		if err := u.chownToHostOwner(ctx, u.RepoPath); err != nil {
			u.State.Log(LogWarn, "chown repo back to host owner failed: "+err.Error()+
				" — host user may need to run 'sudo chown -R $USER $REPO'")
		}
	}
	u.State.Log(LogInfo, "image rebuilt")

	// Phase 3: migrate. Run the new image as a one-shot
	// container with --migrate-only. This applies any
	// pending DB migrations BEFORE the new container
	// starts accepting traffic. Migrations are forward-
	// only + idempotent (per the v0.28.5 catalog's B5/R20
	// checks), so re-running on the new container boot is
	// a no-op.
	//
	// v0.33.1.21 — three pre-existing bugs in this step
	// were fixed:
	//
	//   1. `bash` was never in the alpine base image's
	//      PATH (v0.32.13 switched FROM a non-existent
	//      bash-via-glibc base TO golang:1.25-alpine which
	//      only has busybox `sh`). The orchestrator was
	//      silently failing at this step since v0.32.13,
	//      and the auto-rollback hid the bug from the
	//      operator (the previous tag was always
	//      restored; the migration was simply never
	//      applied to the new image before the swap). The
	//      fix: use `sh` (busybox ash) instead.
	//
	//   2. `--volumes-from skygate` referenced a container
	//      that hasn't existed since v0.29.2 (which
	//      removed `container_name: skygate` to fix a
	//      `--force-recreate` race). The compose-generated
	//      name is `skygate-skygate-1` (or
	//      `skygate-skygate-N` after multiple recreates).
	//      We resolve the actual container ID by label
	//      (same lookup `verify_post_deploy.sh` uses) so
	//      the orchestrator works regardless of how many
	//      times the container has been recreated.
	//
	//   3. `--migrate-only` was referenced in the docker
	//      run command but was never implemented in
	//      cmd/skygate/main.go. The orchestrator was
	//      silently failing with
	//      "unknown command migrate-only" until
	//      v0.33.1.21 added the subcommand.
	//
	// All three were masked by auto-rollback, so the
	// operator saw "the update worked" (the previous
	// image is restored) but actually the new image was
	// never properly migrated. v0.33.1.21 makes the
	// happy path actually work.
	u.State.SetPhase(PhaseMigrate, "running migrations on the new image")
	// Resolve the running skygate container ID by label.
	// `docker ps -a --filter label=com.docker.compose.service=skygate`
	// returns the live container id (or, if the container
	// is in a recreated state, the most recent one with
	// the skygate compose label). We pass --volumes-from
	// <id> so the one-shot container inherits the same
	// data volumes (the DB, /data/ts, etc.) the live
	// skygate is using.
	psOut, err := u.runShellCapture(ctx, "docker", "ps", "-a",
		"--filter", "label=com.docker.compose.service=skygate",
		"--format", "{{.ID}}")
	if err != nil {
		u.failWithRollback(ctx, fmt.Errorf("migrate: resolve skygate container id: %w (output: %s)", err, truncateOutput(psOut, 200)), backupTag)
		return
	}
	skygateContainerID := strings.TrimSpace(psOut)
	if skygateContainerID == "" {
		u.failWithRollback(ctx, fmt.Errorf("migrate: no skygate container found by label (com.docker.compose.service=skygate) — is the service running?"), backupTag)
		return
	}
	u.State.Log(LogDebug, fmt.Sprintf("migrate: using skygate container id=%s for --volumes-from", skygateContainerID))
	// `sh -c "..."` instead of `bash -c "..."` — alpine
	// has busybox sh, not bash. The pipe `2>&1 | tail
	// -20` is portable across POSIX shells.
	migrateCmd := fmt.Sprintf("docker run --rm --volumes-from %s skygate-skygate:latest /app/skygate migrate-only 2>&1 | tail -20", skygateContainerID)
	if out, err := u.runShellCapture(ctx, "sh", "-c", migrateCmd); err != nil {
		u.failWithRollback(ctx, fmt.Errorf("migrate: %w (output: %s)", err, truncateOutput(out, 200)), backupTag)
		return
	}
	u.State.Log(LogInfo, "migrations applied")

	// v0.29.3: Phase 4 (swap) + Phase 5 (verify) run as a
	// DETACHED subprocess (see runShellDetached for the
	// Setsid rationale). The orchestrator (running inside
	// skygate, which is the very container the subprocess
	// is about to recreate) spawns the swap-and-verify
	// sequence as a separate process in a new session,
	// then returns. The subprocess:
	//
	//   1. sleeps 2s so the orchestrator's "build_done"
	//      state write fully flushes to /data;
	//   2. `cd /app && docker compose -p skygate up -d
	//      --force-recreate --no-deps skygate` — this
	//      sends SIGTERM to the orchestrator's parent
	//      (skygate) and starts the new container. The
	//      detached subprocess survives because of Setsid;
	//   3. polls http://localhost:8080/healthz for up to
	//      60s;
	//   4. updates /data/skygate-update-status.json via
	//      `sed` — flips the phase to "done" on success or
	//      "failed" with a final log line on healthz timeout.
	//
	// The operator no longer has to run anything manually;
	// the /admin/update page shows live progress as the
	// subprocess updates the state file every few seconds.
	// The 5s auto-refresh on the page picks up the new
	// phase without a reload.
	u.State.SetPhase(PhaseBuildDone, "image rebuilt, spawning detached swap subprocess")
	u.State.Log(LogInfo, "build done — spawning detached swap subprocess")
	if err := u.spawnSwapSubprocess(); err != nil {
		u.State.Log(LogError, "spawn swap subprocess: "+err.Error())
		u.State.Fail(fmt.Errorf("spawn swap subprocess: %w", err))
		return
	}
	u.State.Log(LogInfo, "swap subprocess spawned; orchestrator exiting. The subprocess will update phase=done/failed.")
	// Note: do NOT call u.State.Complete() here. The phase
	// is "build_done" and the detached subprocess flips it
	// to "done" or "failed" after the swap + verify. The
	// orchestrator's job is done; it returns from Run() and
	// the Go runtime cleans up the goroutine. The HTTP
	// server keeps running until skygate's main returns
	// (which happens when docker compose up sends SIGTERM
	// to the skygate process).

	// Phase 5: verify. Poll /healthz for up to 60s. If
	// the new binary boots and the DB is reachable, the
	// /healthz returns 200 with the new build label.
	u.State.SetPhase(PhaseVerify, "polling /healthz for the new build")
	if err := u.pollHealthz(ctx, 60*time.Second); err != nil {
		u.failWithRollback(ctx, fmt.Errorf("healthz: %w", err), backupTag)
		return
	}
	u.State.Log(LogInfo, "healthz returned 200 with the new build label")

	u.State.Complete()
	u.State.Log(LogInfo, fmt.Sprintf("update complete: %s → %s", u.CurrentVersion, target))
}

// failWithRollback is the unified failure path: transitions
// to failed, runs the rollback, and updates the final state
// to "rolled_back" (success) or stays at "failed" (rollback
// itself errored → operator must intervene manually).
func (u *DockerUpgrader) failWithRollback(ctx context.Context, cause error, backupTag string) {
	u.State.Log(LogError, "phase failed: "+cause.Error())
	u.State.Fail(cause)

	u.State.Log(LogWarn, "attempting automatic rollback to "+backupTag)

	// Checkout the backup tag. This restores the source
	// to the previous version.
	if err := u.runGit(ctx, "checkout", backupTag); err != nil {
		u.State.Log(LogError, "rollback checkout failed: "+err.Error()+
			" — manual intervention required")
		// Stay at "failed". The UI surfaces the manual
		// steps so the operator can fix it by hand.
		return
	}

	// Rebuild + recreate. Same pattern as the upgrade
	// itself; the previous build was just tagged, so
	// `docker compose build` produces the old image.
	// Use chownToHostOwner (no sudo) — see the comment
	// at the top of Run for why.
	if err := u.chownToHostOwner(ctx, u.RepoPath+"/data/ts"); err != nil {
		u.State.Log(LogError, "rollback chown failed: "+err.Error())
		return
	}
	// v0.29.1: rollback's `docker compose build` is
	// intentional — it ensures the old build is up-to-date
	// after the git checkout (a code-level rollback without
	// a rebuild would still run the failed/broken image).
	// v0.29.3: rollback `docker compose up --force-recreate`
	// runs as a DETACHED subprocess (spawnSwapSubprocess) so
	// the orchestrator's SIGTERM from the up doesn't kill the
	// rollback swap. The subprocess uses the same script as
	// the main path, just flips the phase to "rolled_back"
	// instead of "done" on success.
	if out, err := u.runCompose(ctx, "build", "skygate"); err != nil {
		u.State.Log(LogError, "rollback build failed: "+err.Error()+
			" (output: "+truncateOutput(out, 200)+")")
		return
	}
	if u.hostOwner != "" {
		if err := u.chownToHostOwner(ctx, u.RepoPath); err != nil {
			u.State.Log(LogWarn, "rollback chown repo back to host owner failed: "+err.Error())
		}
	}
	// v0.29.3: same detached-subprocess swap as the main
	// path. The state phase stays at "rolled_back" (we set
	// it via State.Fail at the top of failWithRollback) and
	// the subprocess flips the JSON phase to "rolled_back"
	// on swap success. We DON'T use spawnSwapSubprocess as-is
	// because the script flips "build_done" → "done" /
	// "failed" — we want a separate rolled_back flip.
	//
	// For simplicity we just spawn a small one-off script
	// that runs the same helper-container approach as the
	// success path. The state phase stays at "rolled_back"
	// (set by State.Fail at the top of failWithRollback);
	// the new orchestrator's /admin/update renderUpdatePage
	// is the final arbiter. The script is rendered to
	// /data/skygate-rollback-swap.sh.
	//
	// v0.29.3.1: same helper-container approach as the
	// success path. The rollback uses the SAME helper script
	// (/data/skygate-swap-helper.sh) — the difference is
	// only the marker in the log ("ROLLBACK" vs nothing).
	//
	// The helper script is written inline (cat << EOF)
	// so the rollback path is self-contained even if the
	// Go-level writeSwapHelperScript was somehow skipped
	// (e.g. if failWithRollback is called before spawnSwapSubprocess).
	rollbackScript := `#!/bin/sh
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

SKYGATE_HOST_REPO_PATH="${SKYGATE_HOST_REPO_PATH:-/home/admin/skygate}"
log "spawning helper container"
docker run --rm \
  --pid=host \
  --net=host \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$SKYGATE_HOST_REPO_PATH:/host_repo:ro" \
  -v skygate-data:/data \
  -e SKYGATE_HOST_REPO_PATH="$SKYGATE_HOST_REPO_PATH" \
  --name skygate-rollback-helper \
  alpine:3.20 \
  /bin/sh /data/skygate-swap-helper.sh >> "$LOG" 2>&1 &
HELPER_PID=$!
disown $HELPER_PID 2>/dev/null || true
log "helper container spawned (pid=$HELPER_PID), parent script exiting"
exit 0
`
	rollbackScriptPath := "/data/skygate-rollback-swap.sh"
	if err := os.WriteFile(rollbackScriptPath, []byte(rollbackScript), 0755); err != nil {
		u.State.Log(LogError, "write rollback swap script: "+err.Error())
		return
	}
	u.State.Log(LogInfo, "spawning detached rollback swap subprocess")
	if err := u.runShellDetached(context.Background(), "/bin/sh", rollbackScriptPath); err != nil {
		u.State.Log(LogError, "spawn rollback swap subprocess: "+err.Error())
		return
	}
	u.State.Log(LogInfo, "rollback swap subprocess spawned; orchestrator exiting")
	// The orchestrator's goroutine returns here. The
	// detached subprocess handles the actual swap and
	// healthz verify. The state file is left at
	// phase=rolled_back (set by State.Fail at the top
	// of failWithRollback) and the subprocess's log
	// shows the swap outcome. The page's 5s auto-refresh
	// picks up the new healthz state once the new
	// container is up.

	// Mark the rolled-back state.
	u.State.mu.Lock()
	if u.State.state != nil {
		u.State.state.Phase = PhaseRolledBack
		u.State.state.FinishedAt = time.Now().UTC()
		u.State.state.Log = appendLog(u.State.state.Log, LogInfo, "rollback succeeded — previous version is running")
		_ = u.State.persistLocked()
	}
	u.State.mu.Unlock()
}

// runGit runs `git <args>` from RepoPath. Output is captured
// and stored in the state log on success or failure.
func (u *DockerUpgrader) runGit(ctx context.Context, args ...string) error {
	return u.runShell(ctx, "git", args...)
}

// runCompose runs `<ComposeCmd> -p <project> <args>`. The
// compose command reads docker-compose.yml from RepoPath
// (the cmd.Dir set by runShellCapture). The explicit
// -p <project> overrides the basename-based default —
// critical when the orchestrator runs from inside the
// container (where RepoPath is /app, basename "app") and
// the host's compose was launched from
// /home/admin/skygate (basename "skygate"). Without
// the override, named volumes created on first host-side
// startup (under project "skygate") are rejected by
// the in-container `docker compose up` because the
// volume's stored project label doesn't match.
func (u *DockerUpgrader) runCompose(ctx context.Context, args ...string) (string, error) {
	prefix := append([]string{}, strings.Split(u.ComposeCmd, " ")...)
	prefix = append(prefix, "-p", u.ComposeProject)
	prefix = append(prefix, args...)
	return u.runShellCapture(ctx, prefix[0], prefix[1:]...)
}

// runShell runs a command via exec.CommandContext. No output
// capture (just exit code); the caller doesn't need the
// output for `chown`, `git tag`, etc.
func (u *DockerUpgrader) runShell(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = u.RepoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		u.State.Log(LogDebug, fmt.Sprintf("$ %s %s → %s",
			name, strings.Join(args, " "), truncateOutput(string(out), 200)))
		return err
	}
	u.State.Log(LogDebug, fmt.Sprintf("$ %s %s → OK",
		name, strings.Join(args, " ")))
	return nil
}

// runShellCapture is runShell with the output returned. Used
// for the build / up / migrate commands whose output might
// be useful in the failure log.
func (u *DockerUpgrader) runShellCapture(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = u.RepoPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

// runShellDetached is runShell with `Setsid: true` and
// `Start()` instead of `Run()` — the subprocess is launched
// in its own session and the caller does not wait for it.
// Used by v0.29.3's auto-swap path: the orchestrator
// (running inside skygate, which is the very container the
// subprocess is about to recreate) spawns the swap as a
// detached subprocess. The subprocess's new session
// insulates it from the SIGTERM that `docker compose up
// --force-recreate` sends to the skygate container.
//
// Why this is safe:
//   - The subprocess is in a new session (no controlling tty,
//     no process group shared with the orchestrator), so
//     SIGTERM to skygate's process group does NOT reach
//     the subprocess.
//   - The subprocess inherits the orchestrator's CWD (/app)
//     and the /data named volume (skygate-data) bind-mount,
//     so it can read/write /data/skygate-update-status.json
//     and exec `docker compose` against /app/docker-compose.yml.
//   - The subprocess is fire-and-forget: the orchestrator
//     returns immediately after Start() returns. Any error
//     in the subprocess is reflected in
//     /data/skygate-update-status.json (which the
//     subprocess updates) — the page reads it on the next
//     5s auto-refresh.
//
// What the subprocess does:
//   1. Sleep 2s (let the orchestrator's "build_done" state
//      write flush).
//   2. `cd /app && docker compose -p skygate up -d
//      --force-recreate --no-deps skygate` — kills the old
//      skygate, starts the new one. This is where the
//      orchestrator's process dies; the subprocess survives.
//   3. Poll http://localhost:8080/healthz for up to 60s.
//   4. Use sed to update /data/skygate-update-status.json:
//      "phase": "build_done" → "phase": "done" on success,
//      "phase": "failed" with a final log line on failure.
//
// The subprocess's stdout/stderr are redirected to a log
// file under /data so the operator can `tail` it later if
// something goes wrong.
func (u *DockerUpgrader) runShellDetached(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = u.RepoPath
	// Setsid: true puts the subprocess in a new session.
	// This is the critical bit — without it, SIGTERM to
	// skygate's process group would cascade to the
	// subprocess and kill the swap mid-execution.
	//
	// The SysProcAttr.Setsid field is Linux-only. On
	// other platforms the applySysProcAttr helper in
	// setsid_other.go is a no-op (the subprocess shares
	// the orchestrator's process group, which is fine for
	// tests but means a real swap on a non-Linux host
	// would still die with the orchestrator — the
	// orchestrator is Docker-only, so this is not a
	// production concern).
	applySysProcAttr(cmd)
	// Detach stdio. The subprocess writes its own log to
	// /data/skygate-update-swap.log so the operator can
	// inspect what happened. Tests override SwapLogPath
	// to a temp dir.
	logFile := u.SwapLogPath
	if logFile == "" {
		logFile = "/data/skygate-update-swap.log"
	}
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("open swap log: %w", err)
	}
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.Stdin = nil
	if err := cmd.Start(); err != nil {
		f.Close()
		return fmt.Errorf("start detached subprocess: %w", err)
	}
	// Release the subprocess so it doesn't become a zombie
	// when the orchestrator's goroutine returns. The
	// subprocess has Setsid: true so it lives independently
	// of the orchestrator's process group.
	go func() {
		_ = cmd.Wait()
		f.Close()
	}()
	return nil
}

// pollHealthz polls the post-deploy skygate /healthz endpoint
// until it returns 200 (or the timeout fires). The "build" field
// in the response should match the new build label — the
// caller passes that as `expectedBuild` to assert it's not
// just the old container still running. For the simple
// "is the new image up" check, we accept any 200 (the
// caller can read the build from the State if it needs the
// strict check; we don't have a label here in DockerUpgrader).
//
// 2026-07-30 (v0.34 cleanup): the v0.29.0
// `ensureComposeServiceRunning` helper that used to gate
// this poll on `docker ps` state is gone — the v0.29.2
// `container_name:` fix made the race a non-issue. /healthz
// alone is the canonical liveness signal.
func (u *DockerUpgrader) pollHealthz(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	attempt := 0
	for {
		attempt++
		if time.Now().After(deadline) {
			return fmt.Errorf("healthz did not return 200 within %s (%d attempts)", timeout, attempt-1)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Use Go's net/http instead of curl. The
		// skygate container's base image is alpine
		// (v0.32.13+), which doesn't ship curl —
		// exec.CommandContext("curl", ...) silently
		// fails with "executable file not found",
		// which the orchestrator was mistaking for
		// "container not yet healthy" (timed out
		// after 60s, then triggered auto-rollback
		// even though the new container was actually
		// fine). Using net/http here is the same
		// approach v0.32.22 took for the HTTP probes
		// elsewhere in the codebase — no shell
		// dependency, no path surprises across
		// host/container rebuilds.
		req, _ := http.NewRequestWithContext(ctx, "GET", "http://localhost:8080/healthz", nil)
		resp, httpErr := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if httpErr == nil {
			defer resp.Body.Close()
			bodyBytes, _ := io.ReadAll(resp.Body)
			body := string(bodyBytes)
			// 200 + body contains "status":"ok" → healthy.
			if resp.StatusCode == 200 && strings.Contains(body, `"status":"ok"`) {
				u.State.Log(LogInfo, fmt.Sprintf("healthz 200 (attempt %d)", attempt))
				return nil
			}
			u.State.Log(LogDebug, fmt.Sprintf("healthz body (attempt %d, status %d): %s", attempt, resp.StatusCode, body))
		} else {
			// Connection refused / timeout. Expected
			// during the recreate window. Log only on
			// first failure + every 10th to avoid log
			// spam.
			if attempt == 1 || attempt%10 == 0 {
				u.State.Log(LogDebug, fmt.Sprintf("healthz not ready (attempt %d): %s", attempt, httpErr))
			}
		}
		// Wait before retrying. 5s is short enough that
		// the operator sees frequent updates, long
		// enough that we don't hammer the server.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

// shortSHA returns the first 8 hex chars of a build label
// (e.g. "v0.28.6-21-ge3ce6f0+e3ce6f0" → "e3ce6f0"). Used
// for the backup tag name; the full label would also work
// but 8 chars is enough for `git show` to disambiguate.
func shortSHA(buildLabel string) string {
	// Format: vX.Y.Z-N-g<short>+<short> or vX.Y.Z+<short>
	// We want the LAST "+" segment (the build hash, not
	// the tag hash). For our case both are the same SHA
	// (the +commit part from -ldflags), so either works.
	idx := strings.LastIndex(buildLabel, "+")
	if idx < 0 || idx+1 >= len(buildLabel) {
		// Fallback: hash the whole label
		return "unknown"
	}
	hash := buildLabel[idx+1:]
	if len(hash) > 8 {
		hash = hash[:8]
	}
	return hash
}

// ownerPattern validates a "uid:gid" string. We accept
// only digits + colon, e.g. "1000:1000". Anything else
// (typos, "admin:admin" by name, etc.) is rejected
// so a misconfigured SKYGATE_HOST_OWNER env doesn't cause
// chown to fail with a confusing error.
var ownerPattern = regexp.MustCompile(`^[0-9]+:[0-9]+$`)

// detectHostOwner captures the host-side uid:gid for the
// source tree. Used by chownToHostOwner to restore file
// ownership after the orchestrator's git mutations.
//
// Resolution order:
//  1. Already-cached value (idempotent — first call wins).
//  2. SKYGATE_HOST_OWNER env var (e.g. "1000:1000"). This
//     is the operator's escape hatch for non-standard
//     layouts (different UID for admin, rootless
//     Docker with a different host user, etc.).
//  3. `stat -c '%u:%g' <RepoPath>/.git/HEAD` — that file
//     was created by the host's `git init` / `git clone`
//     so its owner is the host owner. (We capture this
//     BEFORE any git mutation; after that .git/HEAD is
//     also root:root, so the call site must invoke
//     detectHostOwner at the top of Run, before any
//     runGit call.)
//  4. Fallback "1000:1000" — the standard Ubuntu first
//     user UID, which is admin on the operator's VM.
//     For the 99% case this is correct; operators with a
//     non-standard layout use the env override.
func (u *DockerUpgrader) detectHostOwner(ctx context.Context) (string, error) {
	if u.hostOwner != "" {
		return u.hostOwner, nil
	}
	if v := os.Getenv("SKYGATE_HOST_OWNER"); v != "" {
		if !ownerPattern.MatchString(v) {
			return "", fmt.Errorf("SKYGATE_HOST_OWNER=%q is not a valid uid:gid", v)
		}
		u.hostOwner = v
		return u.hostOwner, nil
	}
	// Auto-detect via stat. The marker file is .git/HEAD
	// — present in any working git checkout, owned by the
	// host user (not by us, even inside the container,
	// UNTIL the first git mutation runs).
	marker := u.RepoPath + "/.git/HEAD"
	out, err := u.runShellCapture(ctx, "stat", "-c", "%u:%g", marker)
	if err == nil {
		owner := strings.TrimSpace(out)
		if ownerPattern.MatchString(owner) {
			u.hostOwner = owner
			return u.hostOwner, nil
		}
	}
	// Fallback: standard first user.
	u.hostOwner = "1000:1000"
	return u.hostOwner, nil
}

// chownToHostOwner runs `chown -R <host-owner> <paths...>`.
// Used after every git mutation phase to keep the bind-
// mount's file ownership in sync with the host user —
// otherwise the operator's next `git pull` from the host
// shell fails with "Permission denied".
//
// Why no sudo: inside the skygate container we run as
// root (the entrypoint does `chmod 777 /app`), so chown
// works directly. On a bare/systemd host the orchestrator
// runs as root too (the systemd unit's User=root or the
// admin user; either way, chown to a different uid
// requires CAP_FOWNER which root has). The previous
// version of this code prefixed `sudo`, which broke in
// the Alpine container (no sudo binary).
func (u *DockerUpgrader) chownToHostOwner(ctx context.Context, paths ...string) error {
	owner, err := u.detectHostOwner(ctx)
	if err != nil {
		return err
	}
	args := []string{"-R", owner}
	args = append(args, paths...)
	return u.runShell(ctx, "chown", args...)
}

// truncateOutput limits a captured stdout/stderr to max
// chars for the log buffer. Full output stays in the
// shell history on the host; we don't echo it into the
// status file (which the page renders inline).
func truncateOutput(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}

// swapHelperScript is the shell script body that runs
// INSIDE the v0.29.3.1 helper container. It's rendered
// to /data/skygate-swap-helper.sh by BOTH the success
// path and the rollback path's outer scripts, so the
// helper container can read it via a bind-mount and
// do the actual swap work in the HOST's PID namespace
// (which survives the OLD skygate container's removal).
//
// The helper does:
//   1. sleep 3s (orchestrator's state-file flush)
//   2. pick an alpine image (alpine:3.20 → alpine:latest)
//   3. `docker compose -p skygate -f $HOST_REPO/docker-compose.yml
//      up -d --force-recreate --no-deps skygate`
//   4. poll up to 60s for the new container; if it's
//      stuck in Created (rare compose race), call
//      `docker start <id>`
//   5. do a final healthz check via
//      `docker exec $NEW_ID wget -qO- http://localhost:8080/healthz`
//      (helper has --net=host so localhost:8080 IS the
//      new container's healthz)
//   6. exit
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

# Spawn helper container in HOST PID namespace. Uses
# --net=host so localhost:8080 inside the helper IS
# the new skygate container's healthz. Uses bind-mount
# for the swap helper script + state file. Uses
# --rm so it self-cleans. Runs in background (we exit
# immediately, helper keeps going).
#
# v0.29.3.1.1: mount the *named volume* 'skygate-data'
# (NOT the host path /data — the host /data is empty,
# the real volume mountpoint is
# /var/lib/docker/volumes/skygate-data/_data). The
# OLD skygate container mounts the same named volume
# to /data, so the helper sees the same files the
# swap script just wrote (helper script + state file
# + log).
SKYGATE_HOST_REPO_PATH="${SKYGATE_HOST_REPO_PATH:-/home/admin/skygate}"
log "spawning helper container"
docker run --rm \
  --pid=host \
  --net=host \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$SKYGATE_HOST_REPO_PATH:/host_repo:ro" \
  -v skygate-data:/data \
  -e SKYGATE_HOST_REPO_PATH="$SKYGATE_HOST_REPO_PATH" \
  --name skygate-swap-helper \
  alpine:3.20 \
  /bin/sh /data/skygate-swap-helper.sh >> "$LOG" 2>&1 &
HELPER_PID=$!
disown $HELPER_PID 2>/dev/null || true
log "helper container spawned (pid=$HELPER_PID), parent script exiting"
exit 0
`

// spawnSwapSubprocess writes the swap script to
// /data/skygate-swap.sh, marks it executable, and launches
// it via runShellDetached. The subprocess runs the swap
// and verify (see swapSubprocessScript) and updates the
// state file accordingly. The orchestrator returns
// immediately after Start() — its goroutine is done, the
// subprocess continues independently.
func (u *DockerUpgrader) spawnSwapSubprocess() error {
	scriptPath := "/data/skygate-swap.sh"
	if err := os.WriteFile(scriptPath, []byte(swapSubprocessScript), 0755); err != nil {
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

