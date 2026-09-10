// Package tailscale — Tailscale skygate module (B-mod-tailscale,
// 2026-09-10).
//
// Implements module.Module for Tailscale, the opt-in communication
// channel that powers most other skygate features (cluster HA mesh,
// Telegram API relay, DERP relay, exit-node).
//
// The module is *intentionally* the most complex Module in skygate
// because Tailscale itself is the most complex dependency — three
// install modes (os_level / in_container / attach), four opt-in
// sub-features (cluster / telegram / derp / exit), and an external
// daemon (tailscaled) that needs to be running for Start() to
// succeed.
//
// Design constraint: every method that touches the host OS (apt
// install, systemctl, docker run) goes through a CmdRunner
// interface. This lets the unit tests substitute a mock runner
// and assert the exact command sequence without root or Docker.
package tailscale

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// CmdRunner is the abstraction over "run a shell command on the
// host". The real implementation (execRunner) calls os/exec. The
// mock implementation (in tailscale_test.go) records calls and
// returns canned output.
//
// Every Module method that touches the host goes through this.
// Tests substitute a mock via NewModuleWithRunner.
type CmdRunner interface {
	// Run executes `name args...` with a 30s timeout. Returns
	// stdout, stderr, exit code, and any exec error. A non-zero
	// exit code is NOT treated as an error by the runner — the
	// caller inspects the exit code and decides what to do.
	// (apt failing is recoverable; tailscale up failing is
	// also recoverable; only ctx.DeadlineExceeded is fatal.)
	Run(ctx context.Context, name string, args ...string) (stdout, stderr string, exitCode int, err error)

	// LookPath returns the absolute path of name (or "" if not
	// on PATH). Used by Health() to check whether tailscale /
	// tailscaled / docker / systemctl is installed.
	LookPath(name string) string
}

// execRunner is the production CmdRunner. It uses os/exec under
// the hood. Each Run has a 30s default timeout (overridable via
// context) — the install path is the worst case (apt + apt update
// + 2x systemctl) and that comfortably fits in 30s.
type execRunner struct{}

// NewExecRunner returns the production CmdRunner.
func NewExecRunner() CmdRunner { return execRunner{} }

func (execRunner) Run(ctx context.Context, name string, args ...string) (string, string, int, error) {
	// 30s default timeout. Long enough for apt install tailscale
	// + tailscale up, short enough that a stuck command surfaces
	// as an error within a /admin/modules/{name}/install round-trip.
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	return stdout.String(), stderr.String(), exitCode, err
}

func (execRunner) LookPath(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// errFromExit is a small helper that converts a non-zero exit code
// into a readable error. Modules wrap it with their own context
// (e.g. "install: tailscale up: exit 1: ...") so the audit log
// shows the exact failing step.
func errFromExit(stdout, stderr string, exitCode int) error {
	if exitCode == 0 {
		return nil
	}
	msg := stderr
	if msg == "" {
		msg = stdout
	}
	return fmt.Errorf("exit %d: %s", exitCode, msg)
}
