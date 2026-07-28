//go:build linux

package update

import (
	"os/exec"
	"syscall"
)

// applySysProcAttr configures the subprocess for v0.29.3's
// detached-swap pattern. On Linux, it sets `Setsid: true`
// so the subprocess gets its own session — SIGTERM to
// skygate's process group (which is what `docker compose up
// --force-recreate` sends) does NOT cascade to the
// subprocess.
//
// The Setsid field of syscall.SysProcAttr is Linux-only;
// the build tag on this file ensures it compiles only on
// Linux. See setsid_other.go for the non-Linux fallback
// (no-op — the orchestrator is Docker-only, so non-Linux
// is dev/test only).
func applySysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
