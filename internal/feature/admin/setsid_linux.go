//go:build linux

package admin

import (
	"os/exec"
	"syscall"
)

// applySysProcAttr configures the subprocess for v0.33.1.16's
// detached-restart pattern. On Linux, it sets `Setsid: true`
// so the subprocess gets its own session — the SIGTERM that
// `docker compose restart skygate` sends to the parent
// skygate's process group (PID 1 = entrypoint.sh) does NOT
// cascade to the restart subprocess.
//
// The Setsid field of syscall.SysProcAttr is Linux-only;
// the build tag on this file ensures it compiles only on
// Linux. See setsid_other.go for the non-Linux fallback
// (no-op — the restart button is only meaningful on a real
// Linux deployment; the non-Linux case is dev/test where
// the container lifecycle is emulated).
func applySysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
