package update

// B393 — the swap verdict must name the evidence it checked.
//
// The defect this file closes
// ---------------------------
// `/admin/update` reported `phase=done, error=null` while the panel kept
// serving the previous build (live host, 2026-10-16, twice). Both producers
// of that verdict asked the same wrong question — "does /healthz answer
// 200 with status:ok?" — and the answer was yes, because the OLD container
// answers /healthz perfectly well:
//
//   - `DockerUpgrader.pollHealthz` (internal/update/docker.go) accepted any
//     200 and never looked at the `build` field, even though its own doc
//     comment promised an `expectedBuild` parameter that did not exist.
//   - `Service.confirmPendingSwap` (internal/feature/admin/update.go) did the
//     same from every `/admin/update` page render, then called
//     `store.Complete()`.
//
// So an update whose swap step never ran (the helper was handed an empty
// volume — see B393's F1) was promoted to `done` within one page view.
//
// The two witnesses used here
// ---------------------------
//  1. The build label: `/healthz` publishes `build` (e.g. "v1.5.131+b9b22468").
//     Version-part equality with the job target is a positive proof; a
//     DIFFERENT build is a negative one. Only labels that cannot be
//     compared (a branch, HEAD, a bare commit) fall back to "the label
//     changed", because the panel cannot predict git-describe output.
//  2. The container id: `docker compose up --force-recreate` always creates
//     a new container, so "the serving container is still the one that
//     requested the update" proves the swap did not happen — even when the
//     target is a branch and the label is unchanged.
//
// Exactly one of them has to hold, and neither may contradict a failure
// marker the swap script itself wrote into its log.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ContainerFingerprint identifies the skygate container serving the panel.
type ContainerFingerprint struct {
	ID        string
	Status    string
	StartedAt time.Time
}

// skygateServiceLabel is the compose label every install kind carries, and
// the same filter the migrate phase (docker.go) and the stuck-container
// helper (internal/feature/admin) already use.
const skygateServiceLabel = "label=com.docker.compose.service=skygate"

// SwapGracePeriod is how long an unfinished Docker job may sit at
// `build_done` before it is declared failed. It has to cover the whole
// mechanical sequence — the helper's `apk add docker-cli` (~10 s), the
// compose recreate (~10 s) and the entrypoint's `go build` on a cold cache
// (~30-60 s). Four minutes is generous on purpose: a false "failed" would
// make the operator re-run an update that is still in progress.
const SwapGracePeriod = 4 * time.Minute

// SwapLogFile is the swap subprocess log. The outer script and the helper
// both append to it, and — crucially — it lives on the operator-visible
// /data mount, not inside a container.
//
// A variable rather than a constant so tests can point it at a temp file
// (the same convention as `randReadOS` in state.go).
var SwapLogFile = "/data/skygate-update-swap.log"

// SwapFingerprint looks up the container serving the panel right now.
// It is a variable so tests can inject a fake daemon answer instead of
// requiring a live docker socket (state.go's `randReadOS` is the precedent).
var SwapFingerprint = lookupSwapFingerprint

// lookupSwapFingerprint asks the docker daemon for the skygate container's
// id/status/start time. Returns (nil, nil) — not an error — when no such
// container exists, because that is a legitimate state (native install,
// docker without the socket bind-mounted) in which the verdict simply has
// one fewer witness.
func lookupSwapFingerprint(ctx context.Context) (*ContainerFingerprint, error) {
	// Bounded: this runs on the /admin/update render path and ONCE AT BOOT
	// before the HTTP server takes over, so an unresponsive docker daemon must
	// not be able to delay a start or a page load. Ten seconds is far above a
	// healthy `docker ps` (~30 ms) and far below anything a human notices as a
	// hang.
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	ids, err := dockerOutput(ctx, "ps", "-aq", "--filter", skygateServiceLabel)
	if err != nil {
		return nil, err
	}
	id := firstLine(ids)
	if id == "" {
		return nil, nil
	}
	out, err := dockerOutput(ctx, "inspect", "--format",
		"{{.Id}}|{{.State.Status}}|{{.State.StartedAt}}", id)
	if err != nil {
		return nil, err
	}
	fp := parseContainerInspect(out)
	if fp.ID == "" {
		fp.ID = id
	}
	return fp, nil
}

// CurrentContainerID is the short form of the fingerprint lookup used where
// only "which container is this?" matters (recording the pre-swap id).
func CurrentContainerID(ctx context.Context) string {
	fp, err := SwapFingerprint(ctx)
	if err != nil || fp == nil {
		return ""
	}
	return fp.ID
}

func dockerOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.Output()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// parseContainerInspect parses `{{.Id}}|{{.State.Status}}|{{.State.StartedAt}}`.
// Malformed input yields a fingerprint whose fields are empty (never an
// invented value), so a parser surprise degrades to "no witness" instead of
// a confident wrong verdict.
func parseContainerInspect(out string) *ContainerFingerprint {
	fp := &ContainerFingerprint{}
	parts := strings.Split(strings.TrimSpace(out), "|")
	if len(parts) > 0 {
		fp.ID = strings.TrimSpace(parts[0])
	}
	if len(parts) > 1 {
		fp.Status = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(parts[2])); err == nil {
			fp.StartedAt = t
		}
	}
	return fp
}

// buildVersionPart normalises a build label or a job target to its
// comparable version part: "v1.5.131+b9b22468" → "1.5.131",
// "V1.5.131" → "1.5.131", "1.5.131" → "1.5.131".
//
// This mirrors what main.go's buildVersionString concatenation adds (the
// "+<commit>" suffix) and what normalizeUpdateTarget's "v" prefix is for;
// it is deliberately the ONLY string comparison used for "is this the build
// we asked for?" (the release path must not grow a third one).
func buildVersionPart(label string) string {
	s := strings.TrimSpace(label)
	if i := strings.Index(s, "+"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimPrefix(s, "v")
	s = strings.TrimPrefix(s, "V")
	return strings.ToLower(strings.TrimSpace(s))
}

// looksLikeReleaseTarget reports whether a version part is a release tag
// ("1.5.131"), i.e. something the running build's label can be compared to
// by equality. Branch names ("main"), HEAD and bare commit hashes are not:
// what `git describe` produces for them cannot be predicted here, so for
// those the only available proof is that the label changed.
func looksLikeReleaseTarget(versionPart string) bool {
	if versionPart == "" {
		return false
	}
	dots := 0
	for i, r := range versionPart {
		switch {
		case r >= '0' && r <= '9':
		case r == '.' && i > 0:
			dots++
		default:
			return false
		}
	}
	return dots > 0
}

// BuildSatisfiesTarget reports whether the build label served by /healthz is
// the build the job asked for.
//
//   - empty build → false (nothing proves anything)
//   - empty target → the label must at least differ from the previous build
//     (legacy states carry no target; accepting anything reintroduces the
//     false "done")
//   - release target → version-part equality
//   - branch/HEAD/commit target → the label must have changed; an unchanged
//     label is exactly what "the swap never ran" looks like
func BuildSatisfiesTarget(build, target, previousBuild string) bool {
	running := buildVersionPart(build)
	if running == "" {
		return false
	}
	want := buildVersionPart(target)
	previous := buildVersionPart(previousBuild)
	if want == "" {
		return previous == "" || running != previous
	}
	if running == want {
		return true
	}
	if !looksLikeReleaseTarget(want) {
		return previous == "" || running != previous
	}
	return false
}

// SwapVerdict is the evidence-based answer to "did the swap happen?".
type SwapVerdict struct {
	// OK is true when the evidence proves the swap (or, for a rollback, that
	// the previous build is serving again).
	OK bool
	// Reason is always set — it is what the operator reads in the job log,
	// so it must name what was compared, not just "failed".
	Reason string
	// Build / Expected / ContainerID are echoed into the log line so a
	// support read needs no other source.
	Build       string
	Expected    string
	ContainerID string
}

// EvaluateSwap decides the verdict from the two witnesses. It is pure: no
// clock, no IO, no docker — everything it needs arrives as arguments, which
// is what makes the regression testable without a live daemon.
func EvaluateSwap(st *State, runningBuild string, fp *ContainerFingerprint) SwapVerdict {
	if st == nil {
		return SwapVerdict{Reason: "no update state to verify"}
	}
	expected := st.ToVersion
	if st.Phase == PhaseRolledBack {
		expected = st.FromVersion
	}
	v := SwapVerdict{
		Build:    runningBuild,
		Expected: expected,
	}

	labelOK := BuildSatisfiesTarget(runningBuild, expected, st.FromVersion)
	containerKnown := fp != nil && fp.ID != ""
	if containerKnown {
		v.ContainerID = fp.ID
	}
	swapConfirmed := containerKnown && st.SwapFromContainerID != "" &&
		fp.ID != st.SwapFromContainerID

	switch {
	case labelOK && swapConfirmed:
		v.OK = true
		v.Reason = fmt.Sprintf("the serving build %s matches %s and the container was recreated", runningBuild, expected)
	case labelOK:
		v.OK = true
		v.Reason = fmt.Sprintf("the serving build %s matches %s", runningBuild, expected)
		if containerKnown && st.SwapFromContainerID != "" && fp.ID == st.SwapFromContainerID {
			// Contradiction worth surfacing: the label says "new build" but
			// the container was never recreated. Trust the label (docker
			// could be describing a different container than the one
			// answering) but say so.
			v.Reason += " (note: the container id is unchanged, so the swap may not have recreated it)"
		}
	case swapConfirmed:
		v.OK = true
		v.Reason = fmt.Sprintf("the container was recreated (%s → %s), so the swap ran; the serving build %s cannot be compared to the target %s",
			shortID(st.SwapFromContainerID), shortID(fp.ID), runningBuild, expected)
	default:
		v.Reason = describeSwapFailure(st, runningBuild, expected, fp)
	}
	return v
}

// describeSwapFailure explains WHY the verdict is negative, naming the
// witness that contradicted it. "The update failed" tells an operator
// nothing; "the container is still the one that asked for the update" tells
// them the recreate never ran.
func describeSwapFailure(st *State, runningBuild, expected string, fp *ContainerFingerprint) string {
	switch {
	case runningBuild == "":
		return "no /healthz response carried a build label, so the swap cannot be confirmed"
	case fp == nil || fp.ID == "":
		return fmt.Sprintf("the serving build is %s but the job targets %s (no container fingerprint available, so only the label could be checked)", runningBuild, expected)
	case st.SwapFromContainerID == "":
		return fmt.Sprintf("the serving build is %s but the job targets %s (the pre-update container id was never recorded, so only the label could be checked)", runningBuild, expected)
	case fp.ID == st.SwapFromContainerID:
		return fmt.Sprintf("the serving container is STILL %s — the same container that requested the update, so `docker compose up --force-recreate` never recreated it; the build is %s, the job targets %s",
			shortID(fp.ID), runningBuild, expected)
	default:
		return fmt.Sprintf("the serving build is %s but the job targets %s", runningBuild, expected)
	}
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// swapLogFailure looks for a failure marker the swap script wrote itself.
// The outer script runs inside the app container, so it sees the real /data
// and can report a pre-flight failure immediately instead of leaving the job
// at `build_done` until the grace period expires (B393 F5).
//
// Only lines at or after the job start count: the log is appended to across
// jobs, and an old FATAL must never fail a new one.
func swapLogFailure(path string, since time.Time) (bool, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, ""
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	// Walk backwards: the newest verdict wins.
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		at, body := splitSwapLogLine(line)
		// The success script logs "[ts] FAILED: …", the rollback script
		// "[ts] ROLLBACK FAILED: …", and the helper uses "FATAL:".
		if !strings.HasPrefix(body, "FAILED:") && !strings.Contains(body, " FAILED:") &&
			!strings.Contains(body, "FATAL:") {
			continue
		}
		if !since.IsZero() && !at.IsZero() && at.Before(since.Add(-2*time.Second)) {
			// Older than this job — not this job's verdict.
			return false, ""
		}
		return true, body
	}
	return false, ""
}

// splitSwapLogLine splits "[2026-10-16T20:08:31Z] rest" into its timestamp
// and the rest. A line without a timestamp yields a zero time, which callers
// treat as "unknown" rather than "ancient".
func splitSwapLogLine(line string) (time.Time, string) {
	if !strings.HasPrefix(line, "[") {
		return time.Time{}, line
	}
	end := strings.Index(line, "]")
	if end < 0 {
		return time.Time{}, line
	}
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(line[1:end]))
	if err != nil {
		return time.Time{}, strings.TrimSpace(line[end+1:])
	}
	return ts, strings.TrimSpace(line[end+1:])
}

// swapLogTail returns the last n lines of the swap log, for attaching to a
// failure the operator has to act on. The log is the only place the reason
// lives — the state file is written by Go, the log by the shell scripts.
func swapLogTail(path string, n int) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	all := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(all) > n {
		all = all[len(all)-n:]
	}
	out := make([]string, 0, len(all))
	for _, line := range all {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}

// ResolvePendingSwap folds "an update job is unfinished but the process
// serving the panel is the newest one" into a verdict. It is called from two
// places that see the same state from opposite sides of the recreate:
//
//   - boot (cmd/skygate/main.go): the NEW container is the first process that
//     can witness the swap, and it must do so even if nobody opens the panel —
//     otherwise the job lingers at `build_done` forever.
//   - the /admin/update render (internal/feature/admin): covers the case where
//     the swap never happened at all, so no restart ever occurs.
//
// Returns true when it changed the state.
func ResolvePendingSwap(ctx context.Context, store *StateStore, runningBuild string) bool {
	return resolvePendingSwap(ctx, store, runningBuild, SwapFingerprint, SwapLogFile, time.Now())
}

// resolvePendingSwap is the injectable core of ResolvePendingSwap (now,
// fingerprint lookup and log path as arguments) so the whole verdict — grace
// period included — is testable without docker or a wall clock.
func resolvePendingSwap(ctx context.Context, store *StateStore, runningBuild string,
	fingerprint func(context.Context) (*ContainerFingerprint, error),
	logPath string, now time.Time) bool {

	if store == nil {
		return false
	}
	st := store.Get()
	if st == nil {
		return false
	}
	// Native (systemd / openrc / bare) jobs are finalized from the privileged
	// helper's result files by ConfirmNativeSwap; that path already checks the
	// build the helper reports. Docker is the one this verdict owns.
	if st.InstallKind != InstallDocker.String() {
		return false
	}
	if st.Phase != PhaseBuildDone && st.Phase != PhaseRolledBack {
		return false
	}
	if ctx.Err() != nil {
		return false
	}

	var fp *ContainerFingerprint
	if fingerprint != nil {
		got, err := fingerprint(ctx)
		if err == nil {
			fp = got
		} else {
			store.Log(LogDebug, "swap verdict: container fingerprint unavailable: "+err.Error())
		}
	}

	// A marker the script wrote itself short-circuits the grace period: the
	// swap already decided it cannot work, so waiting adds nothing.
	if failed, line := swapLogFailure(logPath, st.StartedAt); failed {
		msg := "the swap step reported a failure: " + line
		store.Fail(errors.New(msg))
		appendSwapLogTail(store, logPath)
		return true
	}

	v := EvaluateSwap(st, runningBuild, fp)
	if v.OK {
		store.Log(LogInfo, "swap verdict: "+v.Reason)
		switch st.Phase {
		case PhaseBuildDone:
			store.Complete()
		case PhaseRolledBack:
			markRolledBack(store, st.Error)
		}
		return true
	}
	// The "not yet" case is re-asked on every page render (~5 s) and inside
	// confirmPendingSwap's short wait, so an unthrottled line here would bury
	// the job's own log under a hundred identical verdicts.
	logSwapVerdictIfStale(store, "swap verdict: "+v.Reason, swapVerdictLogInterval)

	age := now.Sub(st.StartedAt)
	if st.StartedAt.IsZero() || age > SwapGracePeriod {
		store.Fail(errors.New(unfinishedSwapMessage(st, age, v)))
		appendSwapLogTail(store, logPath)
		return true
	}
	return false
}

// swapVerdictLogInterval is how often an unresolved verdict may repeat itself.
const swapVerdictLogInterval = 45 * time.Second

// logSwapVerdictIfStale appends the message only when the last verdict line is
// older than interval. Looks at the state's own log, so it costs nothing and
// needs no extra bookkeeping (and it survives a process restart, which a
// package-level timestamp would not).
func logSwapVerdictIfStale(store *StateStore, msg string, interval time.Duration) {
	if st := store.Get(); st != nil {
		for i := len(st.Log) - 1; i >= 0; i-- {
			entry := st.Log[i]
			if !strings.HasPrefix(entry.Msg, "swap verdict:") {
				continue
			}
			if time.Since(entry.At) < interval {
				return
			}
			break
		}
	}
	store.Log(LogInfo, msg)
}

// unfinishedSwapMessage is the operator-facing explanation of a job that ran
// out of grace. It names how long it waited, the evidence that contradicted the
// swap, and the commands that produce the diagnostics, because the alternative
// is a support round trip.
func unfinishedSwapMessage(st *State, age time.Duration, v SwapVerdict) string {
	waited := SwapGracePeriod
	if !st.StartedAt.IsZero() && age > 0 && age < waited {
		waited = age
	}
	return fmt.Sprintf("the update never swapped the container after %s: %s. Nothing was changed on the host, so the panel is still running %s (the job targeted %s). "+
		"The swap script's log is %s — check the last lines for a HELPER/FATAL entry, then run `docker compose up -d --force-recreate --no-deps skygate` on the host or re-run the update once the cause is fixed.",
		waited.Round(time.Second), v.Reason, orNone(v.Build), orNone(v.Expected), SwapLogFile)
}

func orNone(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

// appendSwapLogTail copies the tail of the swap log into the job log so the
// failure that already exists on disk is visible on the page without a shell.
func appendSwapLogTail(store *StateStore, logPath string) {
	for _, line := range swapLogTail(logPath, 12) {
		store.Log(LogError, "swap log: "+line)
	}
}
