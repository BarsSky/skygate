package update

// docker.go — v0.29.0 self-update orchestrator for the Docker
// install kind. This is the path the operator's VM uses.
//
// State machine (mirrors docs/ROADMAP.md):
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
	"fmt"
	"os"
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
	// SettingsFn reads a global_settings value by key, so the git mirror URL
	// can be configured from the DB (the admin saves `update.git_url` through
	// /admin/update) instead of requiring an env change + restart. Optional;
	// nil means "env only" (SKYGATE_UPDATE_GIT_URL).
	SettingsFn func(key string) string
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
		RepoPath:       repoPath,
		ComposeCmd:     "docker compose",
		ComposeProject: project,
		State:          state,
		CurrentVersion: currentVersion,
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
	if err := u.fetchTarget(ctx); err != nil {
		u.failWithRollback(ctx, fmt.Errorf("git fetch: %w", err), backupTag)
		return
	}
	// 2026-09-04 (B237.10): the `target` arg may carry
	// the BuildVersion's "+<commit>" suffix (e.g.
	// "ve2d0b9e+e2d0b9e" for an untagged-commit deploy
	// where `git describe --tags --always` returns just
	// the short SHA). The `+` is invalid in a git
	// pathspec, so `git checkout` would error with
	// "pathspec did not match any file(s) known to
	// git". GitRefForBuildLabel strips the "+<commit>"
	// (and any stale "v" prefix added by
	// normalizeUpdateTarget) so the ref is always a
	// valid git pathspec. Callers in feature/admin/update.go
	// already pre-process, but we re-process here as
	// defense-in-depth — `git checkout` failing for an
	// invalid pathspec is the LAST thing we want to
	// debug from a state file.
	gitRef := GitRefForBuildLabel(target)
	if err := u.checkoutRef(ctx, gitRef); err != nil {
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
	if err := u.spawnSwapSubprocess(ctx); err != nil {
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
	//
	// B393: this poll runs in the OLD container, which is
	// usually killed before it can finish — the verdict that
	// matters is resolved by the NEW process (ResolvePendingSwap
	// at boot, confirmPendingSwap on the page). It is kept
	// because a swap that does not recreate the container at
	// all (e.g. compose up rc=0 but no recreate) leaves this
	// process alive to report it.
	u.State.SetPhase(PhaseVerify, "polling /healthz for the new build")
	if err := u.pollHealthz(ctx, 60*time.Second, target); err != nil {
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
	//
	// B393: the body is a package-level constant now, rendered with the
	// SAME resolved /data mount as the success path — the two scripts used
	// to carry separate hardcoded mounts, which is how the wrong volume
	// survived in both of them.
	rollbackDataMount, rollbackMountKind := u.resolveDataMount(context.Background())
	rollbackScript := renderSwapScript(rollbackSwapScriptTemplate, rollbackDataMount)
	u.State.Log(LogInfo, fmt.Sprintf("rollback: /data mount = %s (%s)", rollbackDataMount, rollbackMountKind))
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
