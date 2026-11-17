package migrate

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"

	"skygate/internal/db"
)

// fakeRunner records the invocation and returns a canned result.
type fakeRunner struct {
	gotName  string
	gotArgs  []string
	gotStdin string
	gotEnv   []string
	calls    int
	out      string
	err      error
}

func (f *fakeRunner) run(_ context.Context, name string, args []string, stdin string, env []string) (string, error) {
	f.calls++
	f.gotName, f.gotArgs, f.gotStdin, f.gotEnv = name, args, stdin, env
	return f.out, f.err
}

func runOf(kind string) *db.HostMigration {
	return &db.HostMigration{
		ID: 1, Mode: ModeCopy, TargetHost: "198.51.100.10", TargetPort: 2222,
		TargetUser: "root", AuthKind: kind, AuthRef: "/root/.ssh/id_ed25519",
	}
}

func argvContains(argv []string, want string) bool {
	for _, a := range argv {
		if a == want {
			return true
		}
	}
	return false
}

func argvPair(argv []string, flag string) (string, bool) {
	for i := 0; i < len(argv)-1; i++ {
		if argv[i] == flag {
			return argv[i+1], true
		}
	}
	return "", false
}

// TestRemoteInvocation_KeyPath — the default path: batch mode, the operator's key, non-standard
// port and the command after `--`.
func TestRemoteInvocation_KeyPath(t *testing.T) {
	f := &fakeRunner{}
	deps := Deps{Run: runOf(AuthKeyPath), Command: f.run}
	argv, env, cleanup, err := remoteInvocation(deps, "bash -s")
	defer cleanup()
	if err != nil {
		t.Fatalf("key_path must build an invocation: %v", err)
	}
	if !argvContains(argv, "-i") || !argvContains(argv, "/root/.ssh/id_ed25519") {
		t.Fatalf("argv must carry the key path: %v", argv)
	}
	if !argvContains(argv, "BatchMode=yes") {
		t.Fatalf("key auth must run in batch mode: %v", argv)
	}
	if p, _ := argvPair(argv, "-p"); p != "2222" {
		t.Fatalf("argv must carry the port, got %q in %v", p, argv)
	}
	if argv[len(argv)-2] != "root@198.51.100.10" || argv[len(argv)-1] != "bash -s" {
		t.Fatalf("argv must end with the target and the command: %v", argv)
	}
	if len(env) != 0 {
		t.Fatalf("key auth needs no environment, got %v", env)
	}
	if cleanup == nil {
		t.Fatal("cleanup must never be nil")
	}
}

// TestRemoteInvocation_PastedKeyUsesAFile — a pasted key becomes a 0600 file, and the file is gone
// after cleanup. It must never appear in argv itself.
func TestRemoteInvocation_PastedKeyUsesAFile(t *testing.T) {
	deps := Deps{Run: runOf(AuthKeyPaste), Secret: "-----BEGIN OPENSSH PRIVATE KEY-----\nx\n"}
	argv, _, cleanup, err := remoteInvocation(deps, "true")
	if err != nil {
		t.Fatalf("key_paste must build an invocation: %v", err)
	}
	keyFile, ok := argvPair(argv, "-i")
	if !ok {
		t.Fatalf("argv must carry -i: %v", argv)
	}
	if strings.Contains(keyFile, "OPENSSH") {
		t.Fatalf("the key material must not be in argv: %v", argv)
	}
	if !strings.Contains(keyFile, "skygate-migrate-") {
		t.Fatalf("the key must live in a private temp dir, got %q", keyFile)
	}
	cleanup()
	if _, err := os.Stat(keyFile); err == nil {
		t.Fatalf("cleanup must remove the key file %s", keyFile)
	}
}

// TestRemoteInvocation_PasswordUsesAskpass — a password travels through SSH_ASKPASS (never argv),
// ssh runs under setsid so it has no terminal, and batch mode must be OFF or ssh would never ask.
func TestRemoteInvocation_PasswordUsesAskpass(t *testing.T) {
	deps := Deps{Run: runOf(AuthPassword), Secret: "s3cr3t"}
	argv, env, cleanup, err := remoteInvocation(deps, "true")
	defer cleanup()
	if err != nil {
		t.Fatalf("password auth must build an invocation: %v", err)
	}
	if strings.Join(argv, " ") != argv[0]+" "+strings.Join(argv[1:], " ") {
		t.Fatalf("argv assembled incorrectly: %v", argv)
	}
	if argv[0] != "setsid" {
		t.Fatalf("ssh must run under setsid so it has no tty: %v", argv)
	}
	if argvContains(argv, "s3cr3t") {
		t.Fatalf("the password must never be in argv: %v", argv)
	}
	if !argvContains(argv, "-w") {
		t.Fatalf("setsid must wait for the child: %v", argv)
	}
	if !argvContains(argv, "BatchMode=no") {
		t.Fatalf("password auth needs BatchMode=no: %v", argv)
	}
	if !argvContains(argv, "PubkeyAuthentication=no") {
		t.Fatalf("password auth must not offer keys first: %v", argv)
	}
	joined := strings.Join(env, " ")
	if !strings.Contains(joined, "SSH_ASKPASS=") || !strings.Contains(joined, "SSH_ASKPASS_REQUIRE=force") {
		t.Fatalf("the askpass mechanism must be configured: %v", env)
	}
	if strings.Contains(joined, "s3cr3t") {
		t.Fatalf("the password must not be exported: %v", env)
	}
}

// TestRemoteInvocation_RefusesWithoutSecret — a run whose credential was cleared must fail loudly
// instead of connecting as whoever the panel runs as.
func TestRemoteInvocation_RefusesWithoutSecret(t *testing.T) {
	for _, kind := range []string{AuthKeyPaste, AuthPassword} {
		deps := Deps{Run: runOf(kind), Secret: ""}
		if _, _, _, err := remoteInvocation(deps, "true"); err == nil {
			t.Fatalf("%s without a stored secret must be refused", kind)
		}
	}
}

// TestRemoteInvocation_KeyPathRequired — key_path auth without a path is a broken answer.
func TestRemoteInvocation_KeyPathRequired(t *testing.T) {
	r := runOf(AuthKeyPath)
	r.AuthRef = ""
	if _, _, _, err := remoteInvocation(Deps{Run: r}, "true"); err == nil {
		t.Fatal("key_path without a path must be refused")
	}
}

// TestExecute_LocalCapturesOutput — a local step runs bash and returns its output.
func TestExecute_LocalCapturesOutput(t *testing.T) {
	f := &fakeRunner{out: "hello"}
	step := &db.HostMigrationStep{StepKey: "p0.inventory", Where: WhereLocal, Command: "echo hello"}
	out, err := execute(context.Background(), Deps{Command: f.run}, step, step.Command)
	if err != nil {
		t.Fatalf("local execute: %v", err)
	}
	if out != "hello" || f.gotName != "bash" {
		t.Fatalf("want bash + hello, got %q via %q", out, f.gotName)
	}
	if !argvContains(f.gotArgs, "-lc") {
		t.Fatalf("local steps must run through a login shell: %v", f.gotArgs)
	}
}

// TestExecute_RemoteSendsTheCommandOnStdin — the script travels on stdin (so nothing about the
// host's quoting can mangle it) with `set -eu` in front.
func TestExecute_RemoteSendsTheCommandOnStdin(t *testing.T) {
	f := &fakeRunner{}
	deps := Deps{Run: runOf(AuthKeyPath), Command: f.run}
	step := &db.HostMigrationStep{StepKey: "p1.base", Where: WhereRemote, Command: "apt-get update"}
	if _, err := execute(context.Background(), deps, step, step.Command); err != nil {
		t.Fatalf("remote execute: %v", err)
	}
	if f.gotName != "ssh" {
		t.Fatalf("remote steps must run ssh, got %q", f.gotName)
	}
	if !strings.Contains(f.gotStdin, "set -eu") || !strings.Contains(f.gotStdin, "apt-get update") {
		t.Fatalf("stdin must carry the command, got %q", f.gotStdin)
	}
}

// TestExecute_CopyNeedsAPair — a copy step whose command is not "<src> <dst>" must fail instead of
// silently copying nothing.
func TestExecute_CopyNeedsAPair(t *testing.T) {
	step := &db.HostMigrationStep{StepKey: "p3.env", Where: WhereCopy, Command: "only-one-path"}
	if _, err := execute(context.Background(), Deps{Command: (&fakeRunner{}).run}, step, step.Command); err == nil {
		t.Fatal("a malformed copy step must be refused")
	}
}

// TestExecute_CopyStreamsTar — the copy half builds a local tar and ships it over the same ssh
// invocation as every other remote step.
func TestExecute_CopyStreamsTar(t *testing.T) {
	f := &fakeRunner{out: "tarball"}
	deps := Deps{Run: runOf(AuthKeyPath), Command: f.run}
	step := &db.HostMigrationStep{StepKey: "p3.env", Where: WhereCopy,
		Command: "/etc/skygate/skygate.env /etc/skygate/skygate.env"}
	if _, err := execute(context.Background(), deps, step, step.Command); err != nil {
		t.Fatalf("copy execute: %v", err)
	}
	if f.gotName != "ssh" {
		t.Fatalf("the far half of a copy runs over ssh, got %q", f.gotName)
	}
	if !strings.Contains(f.gotStdin, "tarball") {
		t.Fatalf("the tar stream must be piped into the step's stdin, got %q", f.gotStdin)
	}
}

// TestSplitCopy — the pair grammar is two whitespace-separated paths, nothing else.
func TestSplitCopy(t *testing.T) {
	if src, dst, ok := splitCopy("/a/b /c/d"); !ok || src != "/a/b" || dst != "/c/d" {
		t.Fatalf("valid pair rejected: %q %q %v", src, dst, ok)
	}
	for _, bad := range []string{"", "one", "a b c"} {
		if _, _, ok := splitCopy(bad); ok {
			t.Fatalf("malformed copy command %q must be rejected", bad)
		}
	}
}

// TestTruncateOutput_KeepsTheTail — the failure is at the end of the output, so the tail is what
// survives truncation.
func TestTruncateOutput_KeepsTheTail(t *testing.T) {
	long := strings.Repeat("head\n", 100) + "THE FAILURE\n"
	got := truncateOutput(long, 64)
	if !strings.Contains(got, "THE FAILURE") {
		t.Fatalf("the tail must survive: %q", got)
	}
	if !strings.HasPrefix(got, "…(truncated)") {
		t.Fatalf("a truncated output must say so: %q", got)
	}
	if short := truncateOutput("ok", 64); short != "ok" {
		t.Fatalf("short output must pass through, got %q", short)
	}
}

// TestStepTimeout — a copy is allowed to take longer than a command, and nothing is unbounded.
func TestStepTimeout(t *testing.T) {
	if got := stepTimeout(&db.HostMigrationStep{Where: WhereCopy}); got <= stepTimeout(&db.HostMigrationStep{Where: WhereLocal}) {
		t.Fatalf("a copy must be allowed more time than a local command: %v", got)
	}
	for _, w := range []string{WhereLocal, WhereRemote, WhereCopy, WhereInternal} {
		if got := stepTimeout(&db.HostMigrationStep{Where: w}); got <= 0 {
			t.Fatalf("%s must have a bounded timeout, got %v", w, got)
		}
	}
}

// TestRunStep_RefusesManualAndInternal — the two step kinds that must never touch a host.
func TestRunStep_RefusesManualAndInternal(t *testing.T) {
	deps := Deps{Run: runOf(AuthKeyPath)}
	manual := &db.HostMigrationStep{StepKey: "p2.reregister", Kind: KindManual, Where: WhereManual}
	if err := RunStep(context.Background(), deps, manual); !errors.Is(err, ErrManualStep) {
		t.Fatalf("a manual step must be refused with ErrManualStep, got %v", err)
	}
	internal := &db.HostMigrationStep{StepKey: "p6.nodes", Kind: KindAuto, Where: WhereInternal}
	if err := RunStep(context.Background(), deps, internal); !errors.Is(err, ErrExecutorPending) {
		t.Fatalf("an internal step must report the pending executor, got %v", err)
	}
}

// TestRunStep_RefusesWithoutDatabase — every executing path must be recorded, so a nil DB is a
// programming error, not a "run anyway".
func TestRunStep_RefusesWithoutDatabase(t *testing.T) {
	step := &db.HostMigrationStep{StepKey: "p0.reach", Kind: KindAuto, Where: WhereRemote}
	if err := RunStep(context.Background(), Deps{Run: runOf(AuthKeyPath)}, step); err == nil {
		t.Fatal("a nil database must be refused")
	}
}

// TestWriteTempSecret_Permissions — the credential file is 0600 inside a 0700 directory.
func TestWriteTempSecret_Permissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not report POSIX file modes; the 0600 contract is asserted on Linux")
	}
	path, dir, err := writeTempSecret("key", "material", 0600)
	if err != nil {
		t.Fatalf("writeTempSecret: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0600 {
		t.Fatalf("the secret file must be 0600, got %v", st.Mode().Perm())
	}
	dst, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dst.Mode().Perm() != 0700 {
		t.Fatalf("the temp dir must be 0700, got %v", dst.Mode().Perm())
	}
	if _, _, err := writeTempSecret("key", "   ", 0600); err == nil {
		t.Fatal("an empty secret must be refused")
	}
}

// TestWriteAskpass_ScriptPrintsThePassword — the helper is executable and reads the stored file.
func TestWriteAskpass_ScriptPrintsThePassword(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not report POSIX file modes; the executable bit is asserted on Linux")
	}
	path, dir, err := writeAskpass("pw")
	if err != nil {
		t.Fatalf("writeAskpass: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm()&0100 == 0 {
		t.Fatalf("the askpass helper must be executable, got %v", st.Mode().Perm())
	}
	if _, _, err := writeAskpass(""); err == nil {
		t.Fatal("an empty password must be refused")
	}
}

// TestExecute_InternalUsesTheSuppliedAction — an internal step's name is resolved through Deps, and
// the action's own report becomes the step's output.
func TestExecute_InternalUsesTheSuppliedAction(t *testing.T) {
	step := &db.HostMigrationStep{StepKey: "p6.nodes", Where: WhereInternal, Command: "internal_nodes_online"}
	deps := Deps{Internal: map[string]InternalFunc{
		"internal_nodes_online": func(context.Context) (string, error) { return "nodes=16 online=6", nil },
	}}
	out, err := execute(context.Background(), deps, step, step.Command)
	if err != nil {
		t.Fatalf("a supplied action must run: %v", err)
	}
	if !strings.Contains(out, "online=6") {
		t.Fatalf("the action's report must be the output, got %q", out)
	}
}

// TestExecute_InternalMissingActionIsPending — a name this build does not know is PENDING, never a
// failure: "not implemented" and "the host refused" are different actions for the operator.
func TestExecute_InternalMissingActionIsPending(t *testing.T) {
	step := &db.HostMigrationStep{StepKey: "p6.routes", Where: WhereInternal, Command: "internal_routes_converged"}
	_, err := execute(context.Background(), Deps{}, step, step.Command)
	if !errors.Is(err, ErrExecutorPending) {
		t.Fatalf("an unknown action must be pending, got %v", err)
	}
	var nilMap Deps
	_, err = execute(context.Background(), nilMap, step, step.Command)
	if !errors.Is(err, ErrExecutorPending) {
		t.Fatalf("a nil map must not panic and must be pending, got %v", err)
	}
}

// TestRunStep_InternalActionFailureFailsTheStep — the action's error is the step's verdict.
func TestRunStep_InternalActionFailureFailsTheStep(t *testing.T) {
	// A nil database would be refused before the action, so this test asserts the contract on the
	// dispatcher itself: an action error surfaces as one.
	step := &db.HostMigrationStep{StepKey: "p6.policy", Where: WhereInternal, Command: "internal_policy_equivalent"}
	deps := Deps{Internal: map[string]InternalFunc{
		"internal_policy_equivalent": func(context.Context) (string, error) {
			return "differs", errors.New("live policy differs from the generated one")
		},
	}}
	if _, err := execute(context.Background(), deps, step, step.Command); err == nil {
		t.Fatal("an action error must be returned")
	}
}

// TestExecute_InternalVerifyUsesTheSameMap — a verify name is resolved the same way as a command,
// so an internal step can prove itself without a shell.
func TestExecute_InternalVerifyUsesTheSameMap(t *testing.T) {
	step := &db.HostMigrationStep{StepKey: "p4.relay_row", Where: WhereInternal,
		Command: "internal_derp_relay_upsert", Verify: "external_derp_probe_101"}
	calls := 0
	deps := Deps{Internal: map[string]InternalFunc{
		"internal_derp_relay_upsert": func(context.Context) (string, error) { calls++; return "row upserted", nil },
		"external_derp_probe_101":    func(context.Context) (string, error) { calls++; return "101", nil },
	}}
	out, verify, err := runStepBody(context.Background(), deps, step)
	if err != nil {
		t.Fatalf("a wired internal step must pass: %v (out=%q verify=%q)", err, out, verify)
	}
	if calls != 2 {
		t.Fatalf("both the action and its verify must run, got %d call(s)", calls)
	}
	if !strings.Contains(out, "101") {
		t.Fatalf("the verify output must be appended, got %q", out)
	}
}
