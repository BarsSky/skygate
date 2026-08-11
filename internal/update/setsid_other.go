//go:build !linux

package update

import "os/exec"

// applySysProcAttr is a no-op on non-Linux platforms.
// The real subprocess survives by virtue of running in
// the same process group as the orchestrator, which is
// fine for tests but means a real swap on a non-Linux
// host would still die with the orchestrator — the
// orchestrator is Docker-only, so non-Linux is dev/test
// only (the Docker container's kernel is always Linux).
//
// The Linux implementation in setsid_linux.go sets
// `Setsid: true` so SIGTERM to skygate's process group
// does NOT cascade to the subprocess.
func applySysProcAttr(cmd *exec.Cmd) {
	// no-op
	_ = cmd
}
