//go:build windows

package admin

import "os/exec"

// detachProcess is a no-op on Windows: the in-container tailscaled flow is a
// Linux-container feature (the Windows build only has to compile).
func detachProcess(cmd *exec.Cmd) {}
