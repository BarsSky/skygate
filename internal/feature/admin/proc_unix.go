//go:build !windows

package admin

import (
	"os/exec"
	"syscall"
)

// detachProcess puts the child into its own session so it survives the parent:
// the /admin/tailscale "Start" button must leave tailscaled running after the
// HTTP handler (and, on the next container start, the entrypoint) returns.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
