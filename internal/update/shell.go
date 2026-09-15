// internal/update/shell.go — minimal os/exec wrapper shared by
// the git-based (docker.go) and image-pull (image.go) upgraders.
//
// Extracted into its own file so tests can stub it via the
// var-shellCmdExec pattern without polluting the main upgrader
// files. B249 (image-pull strategy) introduced this — the
// existing DockerUpgrader had its own runShell/runShellCapture
// that depend on the struct; the image upgrader needed its own
// free-function version for testability.
//
// We use a package-level var instead of an interface because
// the only thing tests need to override is "what command does
// `docker pull` execute". A var is simpler than defining an
// interface + fake implementation.

package update

import (
	"bytes"
	"context"
	"os"
	"os/exec"
)

// shellExec is the function used to actually run subprocesses.
// Default: realShellExec. Tests override to return canned output
// without spawning a process.
var shellExec = realShellExec

// realShellExec runs the command via os/exec.CommandContext.
// Returns combined stdout+stderr as the captured output. The
// error is non-nil if the command exits non-zero.
func realShellExec(ctx context.Context, name string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Include stderr in the returned output so callers can
		// surface "docker pull: access denied to ..." etc.
		out := stdout.String()
		if e := stderr.String(); e != "" {
			out += "\n" + e
		}
		return out, err
	}
	return stdout.String(), nil
}

// runShellCmd is the package-internal wrapper around shellExec.
// Both DockerUpgrader (docker.go) and ImagePullStrategy (image.go)
// call this; it's the single chokepoint tests can stub.
func runShellCmd(ctx context.Context, name string, args ...string) (string, error) {
	return shellExec(ctx, name, args...)
}

// getEnv wraps os.Getenv for symmetry with shellExec.
var getEnv = os.Getenv
