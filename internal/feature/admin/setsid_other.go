//go:build !linux

package admin

import "os/exec"

// applySysProcAttr is a no-op on non-Linux platforms.
// The real subprocess would die with the parent on
// SIGTERM, which is fine for dev/test (the restart
// button only matters on a real Linux deployment).
//
// The Linux implementation in setsid_linux.go sets
// `Setsid: true` so SIGTERM to skygate's process group
// does NOT cascade to the subprocess.
func applySysProcAttr(cmd *exec.Cmd) {
	// no-op
	_ = cmd
}
