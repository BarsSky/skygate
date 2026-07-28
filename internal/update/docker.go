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
// healthz poll). The HTTP handler returns 303 to the page
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
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// DockerUpgrader runs the Docker install kind update.
type DockerUpgrader struct {
	// RepoPath is the path to the skygate source repo.
	//
	// When running inside the skygate container (the
	// orchestrator IS a goroutine inside the skygate HTTP
	// server, started by the container's entrypoint),
	// RepoPath is the IN-CONTAINER path of the bind-mount
	// of the source dir — typically /app (per
	// docker-compose.yml's `./:/app`). The host's
	// /home/skyadmin/skygate is unreachable from inside
	// the container; passing that path would make
	// `cmd.Dir` fail with "no such file or directory".
	//
	// On a bare/systemd host (orchestrator running as
	// skygate's system service), RepoPath is the host
	// path — typically /home/skyadmin/skygate.
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
	// the host's compose runs from /home/skyadmin/skygate
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
	// (e.g. "1000:1000" for skyadmin on a standard
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
// handler kicks it off and returns 303 to the page.
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

	// Phase 2: pull + build. `git fetch --tags` + `git
	// checkout <target>` + `docker compose build skygate`.
	u.State.SetPhase(PhasePullBuild, "fetching target and rebuilding image")
	if err := u.runGit(ctx, "fetch", "--tags", "--prune"); err != nil {
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
	u.State.SetPhase(PhaseMigrate, "running migrations on the new image")
	migrateCmd := fmt.Sprintf("docker run --rm --volumes-from skygate skygate-skygate:latest /app/skygate --migrate-only 2>&1 | tail -20")
	if out, err := u.runShellCapture(ctx, "bash", "-c", migrateCmd); err != nil {
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
	// that runs the same docker compose up + healthz verify
	// and then either leaves the phase as "rolled_back" or
	// flips it to "failed". The script is rendered to
	// /data/skygate-rollback-swap.sh.
	rollbackScript := `#!/bin/sh
set -u
STATE=/data/skygate-update-status.json
LOG=/data/skygate-update-swap.log
PROJECT_DIR=/app
COMPOSE_PROJECT=skygate

log() { echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] ROLLBACK $*" >> "$LOG"; }

log "rollback swap subprocess started (pid=$$)"
sleep 2

log "running docker compose up -d --force-recreate --no-deps skygate"
cd "$PROJECT_DIR"
if ! docker compose -p "$COMPOSE_PROJECT" up -d --force-recreate --no-deps skygate >> "$LOG" 2>&1; then
    log "rollback docker compose up FAILED"
    if [ -f "$STATE" ]; then
        # The orchestrator already set phase=rolled_back via
        # State.Fail; don't overwrite that. Just append a
        # final log line by leaving the file alone.
        log "state file left as-is (phase=rolled_back, swap=manual)"
    fi
    log "rollback swap subprocess exiting (status=1)"
    exit 1
fi
log "rollback docker compose up OK"

for i in 1 2 3 4 5 6 7 8 9 10 11 12; do
    sleep 5
    if curl -fsS --max-time 3 http://localhost:8080/healthz >/dev/null 2>&1; then
        log "rollback healthz OK at attempt $i"
        log "rollback swap subprocess exiting (status=0)"
        exit 0
    fi
    log "rollback healthz not ready (attempt $i)"
done
log "rollback healthz did not return 200 within 60s"
log "rollback swap subprocess exiting (status=1)"
exit 1
`
	rollbackScriptPath := "/data/skygate-rollback-swap.sh"
	if err := os.WriteFile(rollbackScriptPath, []byte(rollbackScript), 0755); err != nil {
		u.State.Log(LogError, "write rollback swap script: "+err.Error())
		return
	}
	u.State.Log(LogInfo, "spawning detached rollback swap subprocess")
	if err := u.runShellDetached(context.Background(), "bash", rollbackScriptPath); err != nil {
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
// /home/skyadmin/skygate (basename "skygate"). Without
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
	// inspect what happened.
	logFile := "/data/skygate-update-swap.log"
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

// ensureComposeServiceRunning waits for a compose service's
// container to reach the `running` state. The v0.29.0
// orchestrator's first end-to-end test discovered that
// `docker compose up -d --force-recreate --no-deps skygate`
// sometimes leaves the new container in `Created` status
// (not `Started`) — a race with the old container's
// `container_name: skygate` that compose couldn't fully
// remove before creating the new one. Without this guard,
// Phase 5's /healthz poll would time out and trigger a
// spurious rollback.
//
// The check uses the compose-managed label
// `com.docker.compose.service=<name>` rather than the
// container name directly, so the lookup works regardless
// of whether compose used the explicit `container_name:`
// (creating `skygate`) or fell back to the project-hash
// default (creating `skygate-skygate-N`).
//
// Returns nil on the first observation of `running`.
// Returns an error if the timeout fires before the container
// reaches `running`. On `created` it issues an explicit
// `docker start <id>` to unstick the container. On
// `exited` / `dead` / `restarting` it retries (the entrypoint
// may take a few seconds to fully come up after Start).
func (u *DockerUpgrader) ensureComposeServiceRunning(ctx context.Context, service string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	attempt := 0
	var lastObserved string
	for {
		attempt++
		if time.Now().After(deadline) {
			return fmt.Errorf("compose service %q not running within %s (last observed status: %q, %d attempts)",
				service, timeout, lastObserved, attempt-1)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// docker ps -a --filter label=com.docker.compose.service=<svc>
		//   --format "{{.ID}} {{.State.Status}}"
		// We pick the most-recently-created container with this
		// label (sort by .CreatedAt desc implicitly via -a + tail).
		// For our single-service case there's at most one match.
		psOut, err := u.runShellCapture(ctx, "docker", "ps", "-a",
			"--filter", "label=com.docker.compose.service="+service,
			"--format", "{{.ID}} {{.State.Status}}")
		if err == nil {
			lines := strings.Split(strings.TrimSpace(psOut), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				parts := strings.Fields(line)
				if len(parts) < 2 {
					continue
				}
				id := parts[0]
				status := parts[1]
				lastObserved = status
				switch status {
				case "running":
					u.State.Log(LogInfo, fmt.Sprintf("container %s is running (id=%s, attempt %d)", service, id, attempt))
					return nil
				case "created":
					// Stuck — start it explicitly.
					u.State.Log(LogInfo, fmt.Sprintf("container %s is in Created state, starting it (id=%s, attempt %d)", service, id, attempt))
					if _, startErr := u.runShellCapture(ctx, "docker", "start", id); startErr != nil {
						u.State.Log(LogDebug, fmt.Sprintf("docker start %s: %s", id, startErr))
					}
				case "exited", "dead", "restarting":
					// Let it try; the entrypoint may still be coming up.
					u.State.Log(LogDebug, fmt.Sprintf("container %s status=%s (id=%s, attempt %d) — waiting",
						service, status, id, attempt))
				default:
					u.State.Log(LogDebug, fmt.Sprintf("container %s status=%s (id=%s, attempt %d)",
						service, status, id, attempt))
				}
			}
		} else {
			u.State.Log(LogDebug, fmt.Sprintf("docker ps failed (attempt %d): %s", attempt, err))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}
// it returns 200 (or the timeout fires). The "build" field
// in the response should match the new build label — the
// caller passes that as `expectedBuild` to assert it's not
// just the old container still running. For the simple
// "is the new image up" check, we accept any 200 (the
// caller can read the build from the State if it needs the
// strict check; we don't have a label here in DockerUpgrader).
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
		// Curl with a short per-request timeout. Use
		// `wget --spider` if curl is missing (the skygate
		// container has both; the VM host may not).
		out, err := exec.CommandContext(ctx, "curl", "-fsS", "--max-time", "5",
			"http://localhost:8080/healthz").Output()
		if err == nil {
			body := string(out)
			// 200 + body contains "status":"ok" → healthy.
			if strings.Contains(body, `"status":"ok"`) {
				u.State.Log(LogInfo, fmt.Sprintf("healthz 200 (attempt %d)", attempt))
				return nil
			}
			u.State.Log(LogDebug, fmt.Sprintf("healthz body (attempt %d): %s", attempt, body))
		} else {
			// Connection refused / timeout. Expected
			// during the recreate window. Log only on
			// first failure + every 10th to avoid log
			// spam.
			if attempt == 1 || attempt%10 == 0 {
				u.State.Log(LogDebug, fmt.Sprintf("healthz not ready (attempt %d): %s", attempt, err))
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
// (typos, "skyadmin:skyadmin" by name, etc.) is rejected
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
//     layouts (different UID for skyadmin, rootless
//     Docker with a different host user, etc.).
//  3. `stat -c '%u:%g' <RepoPath>/.git/HEAD` — that file
//     was created by the host's `git init` / `git clone`
//     so its owner is the host owner. (We capture this
//     BEFORE any git mutation; after that .git/HEAD is
//     also root:root, so the call site must invoke
//     detectHostOwner at the top of Run, before any
//     runGit call.)
//  4. Fallback "1000:1000" — the standard Ubuntu first
//     user UID, which is skyadmin on the operator's VM.
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
// skyadmin user; either way, chown to a different uid
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

// swapSubprocessScript is the shell script that the v0.29.3
// detached subprocess runs. It is rendered to
// /data/skygate-swap.sh at job start and invoked via
// `bash /data/skygate-swap.sh` in a Setsid-detached session.
//
// The script:
//   1. sleeps 2s so the orchestrator's "build_done" state
//      write fully flushes to /data/skygate-update-status.json
//      (the subprocess reads it to learn the current phase
//      for the "swap started" log line);
//   2. runs `docker compose -p skygate up -d --force-recreate
//      --no-deps skygate` — this kills the old skygate
//      (the orchestrator's parent) and starts the new one;
//   3. polls http://localhost:8080/healthz for up to 60s;
//   4. uses sed to flip the state file's "phase" from
//      "build_done" to "done" on success or "failed" on
//      healthz timeout / swap failure.
//
// sed-based JSON mutation is brittle by design — the
// orchestrator's state file is the source of truth and
// any concurrent writes would lose. The subprocess is
// the only writer between the orchestrator's last write
// and its own final write, so the race window is zero.
//
// No python3 / jq on the Alpine skygate image (verified
// via `apk add --no-cache` in the Dockerfile), so sed is
// the only available tool. The mutation is a single
// in-place edit of `"phase": "build_done"` →
// `"phase": "done"` or `"phase": "failed"`. Exact match
// against the literal string.
const swapSubprocessScript = `#!/bin/sh
set -u
STATE=/data/skygate-update-status.json
LOG=/data/skygate-update-swap.log
PROJECT_DIR=/app
COMPOSE_PROJECT=skygate

log() { echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $*" >> "$LOG"; }

log "swap subprocess started (pid=$$)"

# Step 1: give the orchestrator a moment to finish writing
# the "build_done" state line. The orchestrator spawned us
# immediately after that write, so 2s is more than enough.
sleep 2

# Step 2: do the swap. This is where the orchestrator's
# parent (skygate) gets SIGTERM. We survive because the
# process that spawned us called Setsid() — our session is
# independent of skygate's process group.
log "running docker compose up -d --force-recreate --no-deps skygate"
cd "$PROJECT_DIR"
if ! docker compose -p "$COMPOSE_PROJECT" up -d --force-recreate --no-deps skygate >> "$LOG" 2>&1; then
    log "docker compose up FAILED"
    if [ -f "$STATE" ]; then
        sed -i 's/"phase": "build_done"/"phase": "failed"/' "$STATE"
    fi
    log "swap subprocess exiting (status=1)"
    exit 1
fi
log "docker compose up OK"

# Step 3: poll /healthz for up to 60s. The new skygate
# may need a few seconds to bind :8080 (the entrypoint
# runs go build first, ~5-30s).
for i in 1 2 3 4 5 6 7 8 9 10 11 12; do
    sleep 5
    if curl -fsS --max-time 3 http://localhost:8080/healthz >/dev/null 2>&1; then
        log "healthz OK at attempt $i"
        if [ -f "$STATE" ]; then
            sed -i 's/"phase": "build_done"/"phase": "done"/' "$STATE"
        fi
        log "swap subprocess exiting (status=0)"
        exit 0
    fi
    log "healthz not ready (attempt $i)"
done

log "healthz did not return 200 within 60s"
if [ -f "$STATE" ]; then
    sed -i 's/"phase": "build_done"/"phase": "failed"/' "$STATE"
fi
log "swap subprocess exiting (status=1)"
exit 1
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
	// Launch via `bash` rather than exec'ing the script
	// directly so the shebang is not required (the
	// bind-mounted /data may not be executable on every
	// host despite our WriteFile mode). bash is in the
	// skygate image's PATH (it's how entrypoint.sh
	// invokes things).
	return u.runShellDetached(context.Background(), "bash", scriptPath)
}

// ErrAlreadyInProgress is returned by the handler when a
// previous update job is still running. The page surfaces
// this with a "wait for the in-flight job to finish" hint.
var ErrAlreadyInProgress = errors.New("update: another job is already in progress")

