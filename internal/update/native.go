package update

// native.go — v1.5.9 native (systemd / OpenRC / bare-binary) self-update.
//
// WHY THIS EXISTS
//
// The v0.29.0 self-updater only ever implemented the Docker install
// kind (internal/update/docker.go). On a native install the
// "Update now" / "Push update" buttons hit a `default:` branch that
// failed the job with "auto-updater for systemd not yet implemented",
// so the operator was left with the manual steps printed on
// /admin/update (plan docs/plans/2026-09-17-sqlite-pg-and-headscale-
// hardening.md §12.15).
//
// WHY THERE IS A PRIVILEGED HELPER (and not a pure-Go swap)
//
// The native install runs skygate as an UNPRIVILEGED service user:
//
//	install-common.sh: write_systemd_unit()
//	  User=skygate, Group=skygate,
//	  ProtectSystem=strict, PrivateTmp=true, ProtectHome=true,
//	  ReadWritePaths=<data_dir> <etc_dir>
//
// A binary that runs under that unit therefore CANNOT
//
//	1. write /usr/local/bin/skygate (ProtectSystem=strict → /usr is
//	   read-only; and the file is root-owned anyway), nor
//	2. run `systemctl restart skygate` (no polkit rule, no sudo —
//	   `sudo -n true` as the skygate user fails with "a password is
//	   required" on the reference VM).
//
// Restarting the unit from inside the unit is a second, independent
// problem: the orchestrator goroutine lives in the very cgroup that
// `systemctl restart` tears down, so a plain `setsid` child would be
// killed with it (cgroup membership, not process group, is what
// systemd kills).
//
// The fix implemented here keeps the security model intact instead of
// running skygate as root:
//
//	unprivileged skygate  → writes <update_dir>/request.props
//	skygate-update.path   → root-owned path unit, watches that file
//	skygate-update.service→ root-owned oneshot unit, runs
//	                        /usr/local/lib/skygate/skygate-apply-update.sh
//	helper (root)         → download official release + verify
//	                        SHA256SUMS + migrate + swap binary +
//	                        restart unit + poll /healthz (build string)
//	                        + roll back on failure
//	                        → writes <update_dir>/result.* + apply.log
//	skygate (new process) → folds result.* into the update state on
//	                        the next /admin/update page render
//
// The request file deliberately carries ONLY the job id, the target
// tag, the previous build label and the pid — every path (binary,
// service, data dir, env file, health URL, update dir, allowed
// owner/repo) comes from the root-owned helper config
// (/etc/skygate/update-helper.conf). A compromised skygate process
// can therefore ask for "install official release tag X" and nothing
// else; it cannot point the helper at an arbitrary binary path or an
// arbitrary download URL. See deploy/skygate-apply-update.sh.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// File names inside the update dir. Kept in one place because the
// shell helper (deploy/skygate-apply-update.sh) hardcodes the same
// names — the B-check contract pins both sides.
const (
	// NativeRequestFile is the update request the unprivileged
	// service writes. The root helper PARSES it as data and never
	// sources it (it is written by an unprivileged process — sourcing
	// it would hand any skygate-level compromise a root shell, which
	// would defeat the whole point of the helper split). The .props
	// name is deliberately not ".sh"/".env" for the same reason.
	NativeRequestFile = "request.props"
	// NativeResultStatusFile holds one word: done | rolled_back |
	// failed. Its appearance is what tells skygate "the helper is
	// finished, fold the result into the state".
	NativeResultStatusFile = "result.status"
	// NativeResultErrorFile holds the helper's one-line error (may
	// be absent on success).
	NativeResultErrorFile = "result.error"
	// NativeResultBuildFile holds the build string /healthz reported
	// after the swap (evidence for the operator that the new binary
	// actually serves traffic).
	NativeResultBuildFile = "result.build"
	// NativeApplyLogFile is the helper's append-only log. The Go side
	// copies it into the state buffer so the operator sees the
	// privileged part of the update on /admin/update instead of
	// having to ssh + tail a file.
	NativeApplyLogFile = "apply.log"
)

// Defaults. Every one of them is overridable through the environment
// (config.Load reads SKYGATE_UPDATE_DIR / SKYGATE_UPDATE_HELPER and
// passes the values in) so tests and the canary deployment can point
// at a temp dir / a test unit without touching the real install.
const (
	DefaultNativeHelperPath = "/usr/local/lib/skygate/skygate-apply-update.sh"
	DefaultNativeHealthURL  = "http://127.0.0.1:8080/healthz"
	DefaultNativeService    = "skygate"

	// nativeResultStaleAfter is how long the page keeps saying
	// "waiting for the helper" before it adds a warning. The helper
	// itself is bounded by TimeoutStartSec=900 in the unit; this is
	// an operator hint, not a hard failure.
	nativeResultStaleAfter = 5 * time.Minute
)

// nativeTagPattern is the strict shape of a target we let through to
// the privileged helper. Everything the helper does with the value
// (URL construction, a `git`-free tarball path) assumes no shell
// metacharacters can appear, and the request file is sourced by a
// root shell — so the validation is a security boundary, not a
// nicety.
var nativeTagPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// nativeDescribePattern matches git-describe build labels such as
// "v1.5.8-36-g50d4c2d". They are perfectly valid git refs (the Docker
// path checks them out) but GitHub publishes NO release asset for
// them, so the download-based native path must refuse them instead of
// 404-ing halfway through an update.
var nativeDescribePattern = regexp.MustCompile(`-g[0-9a-f]{7,}$`)

// NativeReleaseTagFor normalises a build label / form target into a
// downloadable release tag: drops the "+<commit>" suffix the build
// concatenates and rejects describe-style labels. Returns "" when the
// label has no release-tag form (the caller shows the manual steps /
// an actionable error instead of staging a doomed request).
func NativeReleaseTagFor(buildLabel string) string {
	s := GitRefForBuildLabel(strings.TrimSpace(buildLabel))
	if s == "" || !nativeTagPattern.MatchString(s) || nativeDescribePattern.MatchString(s) {
		return ""
	}
	// A bare commit SHA survives GitRefForBuildLabel's "v"-strip and
	// looks like a tag, but no release is ever published under a raw
	// SHA. Reject it so the operator gets "not a release tag" instead
	// of a 404 halfway through an update.
	if isAllHex(s) {
		return ""
	}
	return s
}

// NativeUpgrader runs the systemd / OpenRC / bare install-kind update by
// staging a request for the privileged helper and letting the helper
// own the stop → swap → restart → verify → rollback sequence.
//
// Run() returns as soon as the request is staged: for systemd the
// process is about to be restarted by the helper, for bare it is
// about to be killed by it. The outcome is picked up from the
// helper's result files on a later page render (ConfirmNativeSwap).
type NativeUpgrader struct {
	// Kind is InstallSystemd or InstallBare.
	Kind InstallKind
	// UpdateDir is the staging directory (writable by the service
	// user, watched by the path unit). Typically
	// <data_dir>/update.
	UpdateDir string
	// HelperPath is the installed helper script.
	HelperPath string
	// ServiceName is the systemd unit / process name to restart.
	ServiceName string
	// HealthURL is polled BY THE HELPER after the restart. Kept on
	// this struct so the page can show it and tests can point it at
	// a fake server.
	HealthURL string
	// State is the shared state store.
	State *StateStore
	// CurrentVersion is the build label at job start.
	CurrentVersion string
	// Spawn launches the helper in bare mode. Defaults to
	// `setsid sudo -n <helper>`; tests override it.
	Spawn func(ctx context.Context, name string, args ...string) error
	// LookPath is injectable for tests (systemctl detection).
	LookPath func(string) (string, error)
	// RunSystemctl is injectable for tests.
	RunSystemctl func(ctx context.Context, args ...string) (string, error)
}

// NewNativeUpgrader builds the upgrader for a native install kind.
// dataDir is the service data dir (the helper's config lives next to
// it); updateDir may be empty, in which case <dataDir>/update is
// used.
func NewNativeUpgrader(kind InstallKind, dataDir, updateDir, serviceName, healthURL string, state *StateStore, currentVersion string) *NativeUpgrader {
	if serviceName == "" {
		serviceName = getEnv("SKYGATE_UPDATE_SERVICE")
	}
	if serviceName == "" {
		serviceName = DefaultNativeService
	}
	if healthURL == "" {
		healthURL = getEnv("SKYGATE_UPDATE_HEALTH_URL")
	}
	if healthURL == "" {
		healthURL = DefaultNativeHealthURL
	}
	if updateDir == "" {
		updateDir = getEnv("SKYGATE_UPDATE_DIR")
	}
	if updateDir == "" {
		updateDir = filepath.Join(dataDir, "update")
	}
	helper := getEnv("SKYGATE_UPDATE_HELPER")
	if helper == "" {
		helper = DefaultNativeHelperPath
	}
	return &NativeUpgrader{
		Kind:           kind,
		UpdateDir:      updateDir,
		HelperPath:     helper,
		ServiceName:    serviceName,
		HealthURL:      healthURL,
		State:          state,
		CurrentVersion: currentVersion,
		LookPath:       exec.LookPath,
		RunSystemctl:   runSystemctlCapture,
	}
}

// Paths inside the update dir. requestPath and applyLogPath are used
// for log lines; the result files are read through ReadNativeResult /
// ConfirmNativeSwap (filepath.Join on the same constants), so there is
// no accessor for them — an unused one trips staticcheck's U1000,
// which the B237.20 contract pins at zero.
func (u *NativeUpgrader) requestPath() string {
	return filepath.Join(u.UpdateDir, NativeRequestFile)
}
func (u *NativeUpgrader) applyLogPath() string {
	return filepath.Join(u.UpdateDir, NativeApplyLogFile)
}

// HelperInstalled reports whether the privileged helper is present +
// executable. /admin/update uses it to decide between "one click" and
// "run the installer to get the helper"; Run() refuses to stage a
// request without it (otherwise the state would sit at "swap" forever
// with nothing to act on it).
func (u *NativeUpgrader) HelperInstalled() bool {
	st, err := os.Stat(u.HelperPath)
	if err != nil {
		return false
	}
	return hasExecBit(st)
}

// hasExecBit reports whether the path is a regular file with the POSIX
// execute bit. Windows dev hosts cannot represent that bit (Go reports
// 0666 for a writable file), so there a regular file counts — the
// production install path is Linux, where the check is meaningful.
func hasExecBit(st os.FileInfo) bool {
	if st.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return st.Mode()&0o111 != 0
}

// PathUnitActive reports whether the path unit that triggers the
// helper is enabled and running. systemctl is queryable without
// privileges, so this works from the unprivileged service.
func (u *NativeUpgrader) PathUnitActive(ctx context.Context) (bool, string) {
	if u.Kind != InstallSystemd {
		return true, ""
	}
	if _, err := u.LookPath("systemctl"); err != nil {
		return false, "systemctl not found on PATH (native systemd install expected)"
	}
	out, err := u.RunSystemctl(ctx, "is-active", "skygate-update.path")
	state := strings.TrimSpace(out)
	if err != nil && state == "" {
		return false, "systemctl is-active skygate-update.path: " + err.Error()
	}
	if state != "active" {
		return false, "skygate-update.path is " + state + " (expected active)"
	}
	return true, ""
}

// Run stages the update request for the privileged helper.
//
// The state is deliberately left at PhaseSwap: the helper restarts us
// (systemd) or kills us (bare), so this process cannot report the
// outcome itself. ConfirmNativeSwap finalizes the job from the
// helper's result files.
func (u *NativeUpgrader) Run(ctx context.Context, target string) {
	u.State.Log(LogInfo, fmt.Sprintf("starting native update %s → %s (kind=%s)", u.CurrentVersion, target, u.Kind))

	u.State.SetPhase(PhaseBackup, "verifying the privileged update helper")
	if !u.HelperInstalled() {
		u.State.Fail(fmt.Errorf("privileged update helper %s is not installed — re-run deploy/install-*.sh to install skygate-update.path + skygate-update.service, or use the manual steps below", u.HelperPath))
		u.State.Log(LogError, "helper missing; native self-update needs root for the binary swap + unit restart (the service runs as an unprivileged user)")
		return
	}
	if ok, why := u.PathUnitActive(ctx); !ok {
		u.State.Fail(fmt.Errorf("update trigger is not armed: %s — re-run deploy/install-*.sh (or `systemctl enable --now skygate-update.path`)", why))
		return
	}
	u.State.Log(LogInfo, "helper present: "+u.HelperPath)

	// Stage the request. Everything path-like is left out on purpose:
	// the helper reads those from its own root-owned config.
	body, err := u.renderRequest(target)
	if err != nil {
		u.State.Fail(err)
		return
	}
	if err := os.MkdirAll(u.UpdateDir, 0o750); err != nil {
		u.State.Fail(fmt.Errorf("create update dir %s: %w", u.UpdateDir, err))
		return
	}
	// A stale result from a previous job must not be mistaken for
	// this job's outcome.
	u.clearResultFiles()

	u.State.SetPhase(PhasePullBuild, "staging update request for the privileged helper")
	if err := writeFileAtomic(u.requestPath(), []byte(body), 0o600); err != nil {
		u.State.Fail(fmt.Errorf("write %s: %w", u.requestPath(), err))
		return
	}
	u.State.Log(LogInfo, "request staged: "+u.requestPath())

	// The trigger differs by kind: systemd has the root-owned path unit,
	// everything else (bare, OpenRC) goes through the narrowly-scoped
	// sudoers drop-in the installer writes, launched detached because the
	// helper is about to replace the process that spawned it.
	if u.Kind != InstallSystemd {
		u.State.SetPhase(PhaseSwap, "launching the privileged helper (setsid + sudo -n)")
		if err := u.launchBareHelper(ctx); err != nil {
			u.State.Fail(err)
			return
		}
		u.State.Log(LogInfo, "helper launched; it will stop this process, swap the binary and start it again")
		return
	}

	u.State.SetPhase(PhaseSwap, "waiting for skygate-update.path to trigger the privileged helper")
	u.State.Log(LogInfo, "the helper will stop, swap and restart "+u.ServiceName+", then poll "+u.HealthURL)
	u.State.Log(LogInfo, "progress log on the host: "+u.applyLogPath())
}

// renderRequest builds the request file. The helper parses it as
// data (never sources it), and the validators above guarantee no
// quote, newline or shell metacharacter can appear in any value, so a
// malicious build label cannot influence the privileged side.
func (u *NativeUpgrader) renderRequest(target string) (string, error) {
	target = strings.TrimSpace(target)
	if !nativeTagPattern.MatchString(target) {
		return "", fmt.Errorf("refusing to stage update: target %q is not a valid release tag", target)
	}
	if nativeDescribePattern.MatchString(target) {
		return "", fmt.Errorf("refusing to stage update: target %q is a git-describe build label, and GitHub publishes no release asset for it — pass a release tag (e.g. v1.5.9) or use the manual steps", target)
	}
	from := strings.TrimSpace(u.CurrentVersion)
	if from == "" {
		from = "unknown"
	}
	if !nativeTagPattern.MatchString(from) {
		// Build labels like "v1.5.8-36-g50d4c2d+50d4c2d" are fine,
		// but anything with a quote/space/& is not worth risking in
		// a file that root sources.
		return "", fmt.Errorf("refusing to stage update: current build label %q is not safe to hand to the helper", from)
	}
	var b strings.Builder
	b.WriteString("# skygate native self-update request — written by the unprivileged service.\n")
	b.WriteString("# PARSED AS DATA by " + u.HelperPath + " (root): never source this file.\n")
	b.WriteString("# Paths are NOT taken from here — they come from the root-owned helper config.\n")
	fmt.Fprintf(&b, "JOB_ID='%s'\n", GenerateJobID())
	fmt.Fprintf(&b, "TARGET='%s'\n", target)
	fmt.Fprintf(&b, "FROM_VERSION='%s'\n", from)
	fmt.Fprintf(&b, "INSTALL_KIND='%s'\n", u.Kind.String())
	fmt.Fprintf(&b, "RUNTIME_PID='%s'\n", strconv.Itoa(os.Getpid()))
	fmt.Fprintf(&b, "REQUESTED_AT='%s'\n", time.Now().UTC().Format(time.RFC3339))
	return b.String(), nil
}

// launchBareHelper runs the helper detached for the install kinds without
// a systemd path unit — bare (nohup/supervisord) and OpenRC (Alpine,
// restarted via `rc-service`). `sudo -n` is required (the binary swap + the
// process/service restart need root) and install-bare.sh / install-alpine.sh
// install the matching sudoers drop-in; if sudo refuses, the error is
// surfaced on the page with the exact command to run by hand.
func (u *NativeUpgrader) launchBareHelper(ctx context.Context) error {
	spawn := u.Spawn
	if spawn == nil {
		spawn = spawnDetachedHelper
	}
	if err := spawn(ctx, "setsid", "sudo", "-n", u.HelperPath); err != nil {
		return fmt.Errorf("launch privileged helper (needs the sudoers drop-in from install-bare.sh): %w", err)
	}
	return nil
}

// spawnDetachedHelper launches the helper in its own session so it
// survives the death of the skygate process it is about to replace.
// (systemd installs do not use this — their cgroup would kill a
// detached child, which is exactly why the path unit exists.)
func spawnDetachedHelper(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	applySysProcAttr(cmd)
	logFile := os.Getenv("SKYGATE_UPDATE_DIR")
	if logFile != "" {
		logFile = filepath.Join(logFile, "launch.log")
	} else {
		logFile = "/tmp/skygate-update-launch.log"
	}
	if f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		cmd.Stdout = f
		cmd.Stderr = f
		defer f.Close()
	}
	return cmd.Start()
}

// clearResultFiles removes a previous job's result so a stale
// "done" can never be attributed to the job we are starting.
func (u *NativeUpgrader) clearResultFiles() {
	for _, n := range []string{NativeResultStatusFile, NativeResultErrorFile, NativeResultBuildFile} {
		_ = os.Remove(filepath.Join(u.UpdateDir, n))
	}
}

// NativeResult is what the privileged helper reported.
type NativeResult struct {
	Status  string // done | rolled_back | failed
	Error   string
	Build   string
	Log     []string
	ModTime time.Time
}

// ReadNativeResult loads the helper's result files. Returns nil when
// the helper has not finished (no result.status yet).
func ReadNativeResult(updateDir string) *NativeResult {
	raw, err := os.ReadFile(filepath.Join(updateDir, NativeResultStatusFile))
	if err != nil {
		return nil
	}
	status := strings.TrimSpace(string(raw))
	if status == "" {
		return nil
	}
	res := &NativeResult{Status: status}
	if b, err := os.ReadFile(filepath.Join(updateDir, NativeResultErrorFile)); err == nil {
		res.Error = strings.TrimSpace(string(b))
	}
	if b, err := os.ReadFile(filepath.Join(updateDir, NativeResultBuildFile)); err == nil {
		res.Build = strings.TrimSpace(string(b))
	}
	if st, err := os.Stat(filepath.Join(updateDir, NativeResultStatusFile)); err == nil {
		res.ModTime = st.ModTime()
	}
	if b, err := os.ReadFile(filepath.Join(updateDir, NativeApplyLogFile)); err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				res.Log = append(res.Log, line)
			}
		}
	}
	return res
}

// ConfirmNativeSwap folds the helper's result into the update state.
// It is called on every /admin/update render for native installs,
// from whichever process happens to be serving (the old one when the
// helper bailed out before the restart, the new one after a
// successful swap). Returns true when it changed the state.
func ConfirmNativeSwap(store *StateStore, updateDir string) bool {
	if store == nil || updateDir == "" {
		return false
	}
	st := store.Get()
	if st == nil {
		return false
	}
	// Only native jobs are finalized from these files; the Docker
	// path has its own detached-subprocess protocol.
	if st.InstallKind != InstallSystemd.String() &&
		st.InstallKind != InstallOpenRC.String() &&
		st.InstallKind != InstallBare.String() {
		return false
	}
	if st.Phase == PhaseDone || st.Phase == PhaseFailed || st.Phase == PhaseRolledBack {
		return false
	}
	res := ReadNativeResult(updateDir)
	if res == nil {
		// No result yet. If the job has been sitting here for a
		// while, say so once instead of spinning silently.
		if !st.StartedAt.IsZero() && time.Since(st.StartedAt) > nativeResultStaleAfter {
			store.Log(LogWarn, fmt.Sprintf("no result from the privileged helper after %s — check `journalctl -u skygate-update` and %s",
				time.Since(st.StartedAt).Round(time.Second), filepath.Join(updateDir, NativeApplyLogFile)))
		}
		return false
	}
	// Two-phase jobs write the status file only at the very end, so a
	// result older than the job start is stale garbage.
	if !st.StartedAt.IsZero() && res.ModTime.Before(st.StartedAt.Add(-2*time.Second)) {
		return false
	}

	for _, line := range res.Log {
		level := LogInfo
		if strings.Contains(line, "ERROR") || strings.Contains(line, "FAILED") {
			level = LogError
		} else if strings.Contains(line, "WARN") {
			level = LogWarn
		}
		store.Log(level, "helper: "+line)
	}

	switch res.Status {
	case "done":
		if res.Build != "" {
			store.Log(LogInfo, "new build reports: "+res.Build)
		}
		store.Complete()
	case "rolled_back":
		store.Log(LogWarn, "the helper rolled back to the previous binary and restarted the service")
		if res.Error != "" {
			store.Log(LogWarn, "rollback reason: "+res.Error)
		}
		markRolledBack(store, res.Error)
	case "failed":
		msg := res.Error
		if msg == "" {
			msg = "the privileged helper failed (see the log lines above)"
		}
		store.Fail(errors.New(msg))
	default:
		store.Log(LogWarn, "unrecognised helper status "+strconv.Quote(res.Status)+"; leaving the job as-is")
		return false
	}

	// Consume the result so it cannot be replayed; the apply log is
	// kept for the operator (it is truncated by the next helper run).
	for _, n := range []string{NativeResultStatusFile, NativeResultErrorFile, NativeResultBuildFile} {
		_ = os.Remove(filepath.Join(updateDir, n))
	}
	return true
}

// markRolledBack transitions the state to PhaseRolledBack. Mirrors the
// lock discipline docker.go uses (same package).
func markRolledBack(store *StateStore, errMsg string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.state == nil {
		return
	}
	store.state.Phase = PhaseRolledBack
	store.state.FinishedAt = time.Now().UTC()
	store.state.ManualFallback = true
	if errMsg != "" {
		store.state.Error = errMsg
	}
	store.state.Log = appendLog(store.state.Log, LogWarn, "rollback complete — the previous version is running")
	_ = store.persistLocked()
}

// writeFileAtomic writes via a temp file in the destination directory
// + rename, so the path unit never observes a half-written request.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".skygate-update-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// runSystemctlCapture runs `systemctl <args>` and returns combined
// output. Note the asymmetry with the Docker path: here we are an
// unprivileged process, so only read-only subcommands are ever run
// from Go (is-active / show); the mutating ones live in the helper.
func runSystemctlCapture(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "systemctl", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
