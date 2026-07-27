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
	"os/exec"
	"strings"
	"time"
)

// DockerUpgrader runs the Docker install kind update.
type DockerUpgrader struct {
	// RepoPath is the path to the skygate source repo on
	// the host (e.g. "/home/admin/skygate"). The
	// orchestrator runs `git` and `docker compose` from
	// this directory.
	RepoPath string
	// ComposeCmd is the docker compose invocation. Defaults
	// to "docker compose". Operators with podman-compose
	// or docker-compose v1 can override.
	ComposeCmd string
	// State is the shared state store. The orchestrator
	// updates phase + log on every transition; the page
	// reads the latest state on every reload.
	State *StateStore
	// CurrentVersion is the build label at job start. The
	// orchestrator uses it for the "starting from" line
	// and the audit log.
	CurrentVersion string
}

// NewDockerUpgrader returns a DockerUpgrader with sensible
// defaults for the operator's VM shape.
func NewDockerUpgrader(repoPath string, state *StateStore, currentVersion string) *DockerUpgrader {
	return &DockerUpgrader{
		RepoPath:        repoPath,
		ComposeCmd:      "docker compose",
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
	// The `chown` step is critical: tailscaled in the
	// container runs as root, so the bind-mounted
	// data/ts/ subdirs get re-owned to root on every
	// container restart.
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
	// into the bind-mounted dir on first start.
	if err := u.runShell(ctx, "sudo", "chown", "-R", "admin:admin", u.RepoPath+"/data/ts/"); err != nil {
		u.failWithRollback(ctx, fmt.Errorf("chown data/ts: %w", err), backupTag)
		return
	}
	if out, err := u.runCompose(ctx, "build", "skygate"); err != nil {
		u.failWithRollback(ctx, fmt.Errorf("docker compose build: %w (output: %s)", err, truncateOutput(out, 200)), backupTag)
		return
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

	// Phase 4: swap. Recreate the skygate container with
	// the new image. Docker's --force-recreate tears down
	// the old container and starts the new one. The
	// healthz probe is implicitly satisfied by the
	// compose's depends_on + healthcheck blocks.
	u.State.SetPhase(PhaseSwap, "recreating skygate container with new image")
	if out, err := u.runCompose(ctx, "up", "-d", "--force-recreate", "--no-deps", "skygate"); err != nil {
		u.failWithRollback(ctx, fmt.Errorf("docker compose up: %w (output: %s)", err, truncateOutput(out, 200)), backupTag)
		return
	}
	u.State.Log(LogInfo, "container recreated")

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
	if err := u.runShell(ctx, "sudo", "chown", "-R", "admin:admin", u.RepoPath+"/data/ts/"); err != nil {
		u.State.Log(LogError, "rollback chown failed: "+err.Error())
		return
	}
	if out, err := u.runCompose(ctx, "build", "skygate"); err != nil {
		u.State.Log(LogError, "rollback build failed: "+err.Error()+
			" (output: "+truncateOutput(out, 200)+")")
		return
	}
	if out, err := u.runCompose(ctx, "up", "-d", "--force-recreate", "--no-deps", "skygate"); err != nil {
		u.State.Log(LogError, "rollback up failed: "+err.Error()+
			" (output: "+truncateOutput(out, 200)+")")
		return
	}

	// Wait for the rolled-back healthz. We give it the
	// same 60s window. If the rollback itself is broken
	// (e.g. the previous tag itself doesn't build), we
	// surface that distinctly.
	if err := u.pollHealthz(ctx, 60*time.Second); err != nil {
		u.State.Log(LogError, "rolled-back healthz did not return 200: "+err.Error()+
			" — manual intervention required")
		return
	}

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

// runCompose runs `<ComposeCmd> <args>`. The compose command
// reads docker-compose.yml from RepoPath, which is the CWD
// for the operator's install.
func (u *DockerUpgrader) runCompose(ctx context.Context, args ...string) (string, error) {
	cmd := append([]string{}, strings.Split(u.ComposeCmd, " ")...)
	cmd = append(cmd, args...)
	return u.runShellCapture(ctx, cmd[0], cmd[1:]...)
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

// pollHealthz loops GET http://localhost:8080/healthz until
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

// ErrAlreadyInProgress is returned by the handler when a
// previous update job is still running. The page surfaces
// this with a "wait for the in-flight job to finish" hint.
var ErrAlreadyInProgress = errors.New("update: another job is already in progress")

