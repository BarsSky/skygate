// exec.go — the executor of a migration plan (B482).
//
// # WHAT IT IS
//
// One function, `RunStep`, executes exactly ONE stored step of a plan: the step's own `Where`
// decides the transport, its `Command` is the work, its `Verify` is the proof, and the step row is
// updated before and after. Keeping it per-step (instead of one long script) is what makes a
// half-finished cutover survivable: the operator can re-run step 12 on a host that is now
// reachable without replaying steps 1-11, and the record shows which one failed.
//
// # WHY SHELL OUT TO ssh INSTEAD OF A LIBRARY
//
// The whole tree reaches other hosts by running `ssh` with `BatchMode=yes`
// (internal/exitnode/provision_b412.go, internal/headscale/routes.go, internal/dbmigrate/
// ssh_transport.go), and `golang.org/x/crypto/ssh` is a deliberately absent dependency (see the
// comment in internal/feature/admin/derp_init.go). This executor follows the same road: the
// password case uses OpenSSH's own `SSH_ASKPASS` mechanism, so no new module dependency and no
// `sshpass` binary appears in the project.
//
// # THE SECRET NEVER REACHES argv
//
// A pasted private key is written to a 0600 file inside a 0700 temp directory and passed as `-i`;
// a password is written to a 0600 askpass helper and reaches ssh through an environment variable,
// never through a command line (where `ps` would show it) and never through the audit log. Both
// files are removed when the step returns, and the executor refuses to run when the panel has no
// `SKYGATE_SECRET_KEY`, because an unencrypted credential at rest is worse than a failed step.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"skygate/internal/db"
)

// ErrExecutorPending marks a step this build cannot execute yet. It is a distinct error (not a
// failure) because a step that was never implemented must not be reported as "the host refused".
var ErrExecutorPending = errors.New("migrate: this step is not implemented in this build")

// ErrManualStep is returned when someone tries to execute a manual step.
var ErrManualStep = errors.New("migrate: this step is manual — the wizard only shows its instruction")

// Target is the new host's SSH endpoint.
type Target struct {
	Host string
	Port int
	User string
	// Auth is AuthKeyPath, AuthKeyPaste or AuthPassword.
	Auth string
	// KeyPath is used when Auth == AuthKeyPath.
	KeyPath string
}

// InternalFunc is one panel action an internal step can name: a headscale API query, an ACL
// comparison, a DERP probe, a DB row write. It returns a human-readable report (stored as the
// step's output) and an error when the action's own verdict is negative.
type InternalFunc func(ctx context.Context) (string, error)

// Deps is everything RunStep needs.
//
// Secret carries the decrypted credential (a password or a private key, depending on the run's
// auth_kind). Log receives one line per action for the panel's log; it may be nil.
type Deps struct {
	DB     *sql.DB
	Run    *db.HostMigration
	Secret string
	Log    func(string)

	// Command is the process runner; nil means the real one. Tests replace it to assert the argv
	// without touching a host — the executor's contract is the argv it builds, so this is the seam
	// that makes the dangerous half testable.
	Command func(ctx context.Context, name string, args []string, stdin string, env []string) (string, error)

	// Internal resolves an internal step's action (or its Verify) by the name the plan carries,
	// e.g. "internal_nodes_online". A missing entry is reported as ErrExecutorPending — that is
	// what keeps "this build cannot do it yet" from looking like "the host refused".
	Internal map[string]InternalFunc
}

func (d Deps) runner() func(context.Context, string, []string, string, []string) (string, error) {
	if d.Command != nil {
		return d.Command
	}
	return realCommand
}

// realCommand runs a command and returns its combined output. stdin is written before waiting, so
// a `bash -s` script or a tar stream can travel the same way.
func realCommand(ctx context.Context, name string, args []string, stdin string, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// RunStep executes one step and records the verdict.
//
// Order matters: the row is marked `running` BEFORE anything happens, so a process that is killed
// mid-step leaves a visible "running" row instead of a silent "pending" one.
func RunStep(ctx context.Context, deps Deps, step *db.HostMigrationStep) error {
	if deps.Run == nil {
		return errors.New("migrate: RunStep: no run")
	}
	if step == nil {
		return errors.New("migrate: RunStep: no step")
	}
	// A manual step never executes and never touches the row, so it is refused before anything
	// else — including the database, which a manual step does not need.
	if step.Kind == KindManual || step.Where == WhereManual {
		return ErrManualStep
	}
	// An internal step is a panel action (a DB row, a headscale API call, an ACL comparison) rather
	// than a shell command. When this build has not wired the named action, saying so is honest and
	// keeps the row `pending` instead of recording a host failure that never happened.
	if step.Where == WhereInternal {
		if _, ok := deps.Internal[step.Command]; !ok {
			return ErrExecutorPending
		}
	}
	if deps.DB == nil {
		return errors.New("migrate: RunStep: no database")
	}

	now := time.Now().Unix()
	step.Status = db.HostStepRunning
	step.StartedAt = now
	step.FinishedAt = 0
	if err := db.UpdateHostMigrationStep(deps.DB, step); err != nil {
		return err
	}

	out, verifyNote, runErr := runStepBody(ctx, deps, step)

	step.Output = truncateOutput(out, 8*1024)
	step.Verify = verifyNote
	step.FinishedAt = time.Now().Unix()
	if runErr != nil {
		step.Status = db.HostStepFailed
		if errors.Is(runErr, ErrExecutorPending) {
			// Not a host failure: the row says "not in this build", which is a different action
			// for the operator (update the panel) than "the command failed".
			step.Status = db.HostStepPending
			step.Verify = "not implemented in this build: " + runErr.Error()
		}
	} else {
		step.Status = db.HostStepOK
	}
	if err := db.UpdateHostMigrationStep(deps.DB, step); err != nil {
		return err
	}
	return runErr
}

// runStepBody dispatches on the step's Where and then runs its Verify.
func runStepBody(ctx context.Context, deps Deps, step *db.HostMigrationStep) (out, verifyNote string, err error) {
	ctx, cancel := context.WithTimeout(ctx, stepTimeout(step))
	defer cancel()

	switch step.Where {
	case WhereLocal, WhereRemote, WhereCopy, WhereInternal, WhereScript:
	case WhereManual:
		return "", "", ErrManualStep
	default:
		return "", "", fmt.Errorf("migrate: unknown step location %q", step.Where)
	}

	first, err := execute(ctx, deps, step, step.Command)
	all := first
	if err != nil {
		return all, "command failed: " + err.Error(), err
	}
	if strings.TrimSpace(step.Verify) == "" {
		return all, "ok", nil
	}
	vout, verr := execute(ctx, deps, step, step.Verify)
	all += "\n--- verify ---\n" + vout
	if verr != nil {
		return all, "verify failed: " + verr.Error(), verr
	}
	return all, "ok", nil
}

// execute runs one shell command according to the step's location.
func execute(ctx context.Context, deps Deps, step *db.HostMigrationStep, command string) (string, error) {
	switch step.Where {
	case WhereLocal:
		return deps.runner()(ctx, "bash", []string{"-lc", command}, "", nil)
	case WhereInternal:
		// A panel action has no shell: the plan carries its NAME, and the host supplies the
		// implementation. A name this build does not know is pending, not failed.
		fn, ok := deps.Internal[command]
		if !ok {
			return "", fmt.Errorf("%w: %s", ErrExecutorPending, command)
		}
		return fn(ctx)
	case WhereScript:
		// B487: an embedded helper travels IN THE BINARY and is streamed to the new host. Nothing has
		// to be installed there (a release-tarball install has no /opt/skygate checkout), and the body
		// never has to survive shell quoting — it arrives on stdin, like every other remote step.
		name, args, serr := splitScriptCommand(step.Command)
		if serr != nil {
			return "", serr
		}
		body, serr := ScriptSource(name)
		if serr != nil {
			return "", serr
		}
		remote := "bash -s --"
		for _, a := range args {
			remote += " " + shellQuote(a)
		}
		argv, env, cleanup, err := remoteInvocation(deps, remote)
		if err != nil {
			return "", err
		}
		defer cleanup()
		return deps.runner()(ctx, "ssh", argv, body, env)
	case WhereRemote:
		argv, env, cleanup, err := remoteInvocation(deps, "bash -s")
		if err != nil {
			return "", err
		}
		defer cleanup()
		return deps.runner()(ctx, "ssh", argv, "set -eu\n"+command+"\n", env)
	case WhereCopy:
		src, dst, ok := splitCopy(step.Command)
		if !ok {
			return "", fmt.Errorf("migrate: copy step %q has no '<src> <dst>' pair", step.StepKey)
		}
		// The tar stream is built locally and consumed remotely by the same ssh invocation the
		// other steps use, so a copy step cannot bypass the credential handling.
		argv, env, cleanup, err := remoteInvocation(deps, "tar xzf - -C "+shellQuote(dst))
		if err != nil {
			return "", err
		}
		defer cleanup()
		stream, err := localTar(ctx, deps, src)
		if err != nil {
			return "", err
		}
		return deps.runner()(ctx, "ssh", argv, stream, env)
	}
	return "", fmt.Errorf("migrate: unknown step location %q", step.Where)
}

// localTar packs one absolute path into a tar stream (kept as a separate function so the copy
// step's local half is testable through Deps.Command).
func localTar(ctx context.Context, deps Deps, src string) (string, error) {
	dir := filepath.Dir(src)
	base := filepath.Base(src)
	return deps.runner()(ctx, "tar", []string{"-czf", "-", "-C", dir, base}, "", nil)
}

// remoteInvocation builds the ssh argv (and the environment) for the run's target.
//
// Cleanup removes the temporary credential files; it is always safe to call.
func remoteInvocation(deps Deps, remoteCommand string) (argv, env []string, cleanup func(), err error) {
	cleanup = func() {}
	run := deps.Run
	if run == nil {
		return nil, nil, cleanup, errors.New("migrate: no run")
	}
	t := Target{Host: run.TargetHost, Port: run.TargetPort, User: run.TargetUser,
		Auth: run.AuthKind, KeyPath: run.AuthRef}
	if t.Host == "" || t.User == "" {
		return nil, nil, cleanup, errors.New("migrate: the run has no target host/user")
	}
	if t.Port == 0 {
		t.Port = 22
	}

	base := []string{
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=15",
		"-p", fmt.Sprint(t.Port),
	}

	switch t.Auth {
	case AuthKeyPath:
		if t.KeyPath == "" {
			return nil, nil, cleanup, errors.New("migrate: auth_kind=key_path but no key path is stored")
		}
		base = append([]string{"-o", "BatchMode=yes", "-i", t.KeyPath}, base...)
	case AuthKeyPaste:
		keyFile, dir, cerr := writeTempSecret("id_ed25519", deps.Secret, 0600)
		if cerr != nil {
			return nil, nil, cleanup, cerr
		}
		cleanup = func() { _ = os.RemoveAll(dir) }
		base = append([]string{"-o", "BatchMode=yes", "-i", keyFile}, base...)
	case AuthPassword:
		askpass, dir, cerr := writeAskpass(deps.Secret)
		if cerr != nil {
			return nil, nil, cleanup, cerr
		}
		cleanup = func() { _ = os.RemoveAll(dir) }
		// A password cannot be answered in batch mode, and ssh only asks the helper when it has no
		// terminal — `setsid -w` provides that without an interactive prompt.
		base = append([]string{
			"-o", "BatchMode=no",
			"-o", "PubkeyAuthentication=no",
			"-o", "PreferredAuthentications=password,keyboard-interactive",
			"-o", "NumberOfPasswordPrompts=1",
		}, base...)
		env = []string{"SSH_ASKPASS=" + askpass, "SSH_ASKPASS_REQUIRE=force", "DISPLAY=:0"}
		argv = append([]string{"-w"}, append(base, "--", t.User+"@"+t.Host, remoteCommand)...)
		argv = append([]string{"setsid"}, argv...)
		return argv, env, cleanup, nil
	default:
		return nil, nil, cleanup, fmt.Errorf("migrate: unknown auth kind %q", t.Auth)
	}
	argv = append(base, "--", t.User+"@"+t.Host, remoteCommand)
	return argv, env, cleanup, nil
}

// writeTempSecret stores a secret in a fresh 0700 directory, 0600 file.
func writeTempSecret(name, body string, mode os.FileMode) (path, dir string, err error) {
	if strings.TrimSpace(body) == "" {
		return "", "", errors.New("migrate: the run has no stored credential")
	}
	dir, err = os.MkdirTemp("", "skygate-migrate-")
	if err != nil {
		return "", "", fmt.Errorf("migrate: temp dir: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", fmt.Errorf("migrate: temp dir mode: %w", err)
	}
	path = filepath.Join(dir, name)
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", fmt.Errorf("migrate: write secret: %w", err)
	}
	return path, dir, nil
}

// writeAskpass writes the OpenSSH password helper: it prints the password once and nothing else.
func writeAskpass(password string) (path, dir string, err error) {
	if password == "" {
		return "", "", errors.New("migrate: the run has no stored password")
	}
	internal, dir, err := writeTempSecret("password", password, 0600)
	if err != nil {
		return "", "", err
	}
	script := "#!/bin/sh\ncat " + shellQuote(internal) + "\n"
	askpass := filepath.Join(dir, "askpass.sh")
	if err := os.WriteFile(askpass, []byte(script), 0700); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", fmt.Errorf("migrate: write askpass: %w", err)
	}
	return askpass, dir, nil
}

// splitCopy reads the "<src> <dst>" pair a copy step carries.
func splitCopy(command string) (src, dst string, ok bool) {
	fields := strings.Fields(command)
	if len(fields) != 2 {
		return "", "", false
	}
	return fields[0], fields[1], true
}

// stepTimeout bounds one step: a copy of a 200 MB database over a slow link needs minutes, but an
// unbounded step is how a wizard hangs forever on a host that answers SSH and then stalls.
func stepTimeout(s *db.HostMigrationStep) time.Duration {
	switch s.Where {
	case WhereCopy:
		return 30 * time.Minute
	default:
		return 10 * time.Minute
	}
}

// truncateOutput keeps the TAIL of the output: the failure is at the end.
func truncateOutput(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := len(s) - limit
	if i := strings.IndexByte(s[cut:], '\n'); i >= 0 {
		cut += i + 1
	}
	return "…(truncated)\n" + s[cut:]
}
