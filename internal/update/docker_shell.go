// docker_shell.go — B532 (2026-11-27): the shell and compose runners, split out of docker.go.
//
// docker.go was 1 515 lines: the Docker upgrader's job runner AND its transport, its shell
// wrappers, its health poller, its ownership helpers and three embedded /bin/sh scripts.
// The audit's complaint was that a reader cannot find what a 1 500-line file does; each
// section keeps its code VERBATIM and its own doc comments, and the package is unchanged —
// no identifier moved, so no caller and no contract had to learn a new name.
package update

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

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
//   - The subprocess inherits the orchestrator's CWD (/app) and the
//     orchestrator's own /data mount, so it can read/write
//     /data/skygate-update-status.json and exec `docker compose` against
//     /app/docker-compose.yml. (B393: the /data that the HELPER container
//     then mounts is resolved from this container's own mount by
//     resolveDataMount — it is not assumed to be a named volume.)
//   - The subprocess is fire-and-forget: the orchestrator
//     returns immediately after Start() returns. Any error
//     in the subprocess is reflected in
//     /data/skygate-update-status.json (which the
//     subprocess updates) — the page reads it on the next
//     5s auto-refresh.
//
// What the subprocess does:
//  1. Sleep 2s (let the orchestrator's "build_done" state
//     write flush).
//  2. `cd /app && docker compose -p skygate up -d
//     --force-recreate --no-deps skygate` — kills the old
//     skygate, starts the new one. This is where the
//     orchestrator's process dies; the subprocess survives.
//  3. Poll http://localhost:8080/healthz for up to 60s.
//  4. Use sed to update /data/skygate-update-status.json:
//     "phase": "build_done" → "phase": "done" on success,
//     "phase": "failed" with a final log line on failure.
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
