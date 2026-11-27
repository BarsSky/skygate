// service_insight_b535.go — B535 (2026-11-27): «понимать, что не так с headscale».
//
// B533 answered «кто управляет службой, работает ли она, куда указывает её имя».
// The operator's next question on 2026-11-27 was «а что ИМЕННО произошло», and the
// card could only answer it with ONE line (the last line that contained "error").
// This file adds the two remaining measurements, both bounded and both pure where
// they can be:
//
//	(1) the unit's own words — the last lines of its journal, redacted, with a count
//	    of errors/warnings and a panic flag, read through the SAME ServiceProbe the
//	    rest of the collector uses (so it is testable on a machine with no
//	    headscale and no systemd);
//	(2) the state of the privileged handover — is a request still waiting for the
//	    applier, and does the LAST applier verdict actually describe a service that
//	    answers now. A verdict written before the current measurement tells us
//	    nothing about the current service, and a verdict written after it means the
//	    page must be loaded once more: that ordering is the whole point of
//	    PostRestartVerdict, and it is a pure function so the rule is pinned by tests
//	    rather than by a comment.
package headscale

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// Journal limits. Read more than we show: the counts must describe a real window,
// the display must stay scannable, and a crash loop can log megabytes — one bounded
// command, never a follow.
const (
	ServiceJournalRead  = 60
	ServiceJournalShown = 20
)

// ServiceJournalTimeout bounds the journal read. The page that shows it is the page
// an operator opens WHILE the control plane is mute, so a hung journald must cost
// seconds, not the request.
const ServiceJournalTimeout = 4 * time.Second

// JournalSnapshot is the unit's own words, already redacted.
type JournalSnapshot struct {
	Lines    []string // the last ServiceJournalShown lines, redacted
	Source   string   // what was read, e.g. "journalctl -u headscale"
	OK       bool     // the window was read (it may be empty: a quiet service)
	Note     string   // why nothing could be read, or a caveat ("partial window")
	Errors   int      // lines that look like failures in the whole window
	Warnings int      // lines that look like warnings
	Panic    bool     // a traceback in the window
}

// serviceSecretPatterns masks the shapes a headscale/tailscale log actually leaks:
// key=value pairs under a sensitive name, bearer tokens, and tskey-* auth keys.
// The list is deliberately SHORT and named: an over-broad regex marks good evidence
// as secret and the operator then cannot read the very line that explains a crash.
var serviceSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(token|password|passwd|secret|api[_-]?key|auth[_-]?key|preauth[_-]?key|private[_-]?key|client[_-]?secret)\s*([=:]\s*)("[^"]*"|'[^']*'|\S+)`),
	regexp.MustCompile(`(?i)\b(bearer)\s+[A-Za-z0-9._~+/-]{8,}=*`),
	regexp.MustCompile(`\btskey-[A-Za-z0-9_-]{8,}`),
}

// RedactServiceLine masks credentials in ONE journal line.
//
// It is intentionally idempotent: running it twice must produce the same text, so a
// line that is logged again by skygate (or read by two layers) cannot grow markers.
func RedactServiceLine(line string) string {
	out := line
	// Order matters: the key=value rule first, so `token=tskey-…` becomes
	// `token=<redacted>` and the auth-key rule then has nothing left to match.
	out = serviceSecretPatterns[0].ReplaceAllString(out, "${1}${2}<redacted>")
	out = serviceSecretPatterns[1].ReplaceAllString(out, "${1} <redacted>")
	out = serviceSecretPatterns[2].ReplaceAllString(out, "<redacted>")
	return out
}

// classifyServiceLine says what one raw line looks like. Kept separate from the
// counters so the rules are visible and testable one line at a time.
func classifyServiceLine(line string) (isErr, isWarn, isPanic bool) {
	low := strings.ToLower(line)
	for _, marker := range []string{"error", "fatal", "failed", "failure", "refused", "denied"} {
		if strings.Contains(low, marker) {
			isErr = true
			break
		}
	}
	for _, marker := range []string{"warn", "deprecated"} {
		if strings.Contains(low, marker) {
			isWarn = true
			break
		}
	}
	for _, marker := range []string{"panic", "goroutine ", "traceback"} {
		if strings.Contains(low, marker) {
			isPanic = true
			isErr = true
			break
		}
	}
	return isErr, isWarn, isPanic
}

// CollectServiceJournal reads the last lines of the unit's own log.
//
// It never returns an error: a journal that could not be read is a Note. The
// manager decides HOW the log is asked for, because the three install kinds keep it
// in three different places, and guessing would produce an empty window that reads
// like a quiet service (the B522 lesson).
func CollectServiceJournal(ctx context.Context, probe ServiceProbe, manager, unit string, limit int) JournalSnapshot {
	var j JournalSnapshot
	if unit == "" {
		j.Note = "no unit name is configured, so no journal could be read"
		return j
	}
	if limit <= 0 {
		limit = ServiceJournalRead
	}
	jctx, cancel := context.WithTimeout(ctx, ServiceJournalTimeout)
	defer cancel()

	var out string
	var err error
	switch manager {
	case "systemd":
		j.Source = "journalctl -u " + unit
		out, err = probe.run(jctx, "journalctl", "-u", unit,
			"-n", fmt.Sprint(limit), "--no-pager", "-o", "cat")
		if err != nil {
			// journalctl exits non-zero both when the window is empty and when the
			// journal is unreadable; the text decides which, never the exit status
			// alone (AGENTS trap 15: an empty read is a fact about the CHECK).
			if strings.TrimSpace(out) == "" {
				j.Note = fmt.Sprintf("%s: %v (nothing was returned — an empty window is not 'no errors')", j.Source, err)
				return j
			}
			j.Note = fmt.Sprintf("%s: %v (partial window)", j.Source, err)
		}
	case "docker":
		j.Source = "docker logs --tail " + fmt.Sprint(limit) + " " + unit
		out, err = probe.run(jctx, "docker", "logs", "--tail", fmt.Sprint(limit), unit)
		if err != nil && strings.TrimSpace(out) == "" {
			j.Note = fmt.Sprintf("%s: %v", j.Source, err)
			return j
		}
	case "openrc":
		j.Note = "an OpenRC install keeps no journal for this unit: read /var/log/messages or run rc-service " + unit + " status"
		return j
	default:
		j.Note = "this host does not manage a " + unit + " service, so there is no journal to read"
		return j
	}

	var kept []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		isErr, isWarn, isPanic := classifyServiceLine(line)
		if isErr {
			j.Errors++
		}
		if isWarn {
			j.Warnings++
		}
		if isPanic {
			j.Panic = true
		}
		kept = append(kept, RedactServiceLine(line))
	}
	if len(kept) > ServiceJournalShown {
		kept = kept[len(kept)-ServiceJournalShown:]
	}
	j.Lines = kept
	// OK means «a window was read», not «the window had lines»: a healthy service
	// with a quiet journal is a measured fact (0 errors), not a failed measurement.
	j.OK = true
	return j
}

// lastErrorLine returns the last line the classifier called a failure, truncated to
// the one-line slot the card has always had.
//
// It redacts AGAIN even though the collector already hands it redacted lines: this is
// the other place a credential could reach the page, and RedactServiceLine is
// idempotent precisely so that defence costs nothing (pinned by
// TestB535_LastErrorLine_ComesFromTheShownAndRedactedTail, which feeds it RAW lines).
func lastErrorLine(lines []string) string {
	const maxLen = 300
	for i := len(lines) - 1; i >= 0; i-- {
		if isErr, _, _ := classifyServiceLine(lines[i]); isErr {
			l := RedactServiceLine(lines[i])
			if len(l) > maxLen {
				l = l[:maxLen] + "…"
			}
			return l
		}
	}
	return ""
}

// --- the privileged handover ------------------------------------------------

// ServicePending describes a request that is still sitting in the update dir.
//
// A file that exists means the unprivileged half has asked and the applier has not
// consumed the request yet (it removes the request before it writes its verdict).
// Saying so on the page is the difference between «нажал и ничего» and «перезапуск
// в процессе».
type ServicePending struct {
	Present bool
	Action  string
	Service string
	Since   time.Time
}

// ReadServicePending reports whether a request is waiting for the applier.
//
// A missing or unreadable file is NOT an error: "no request waiting" is a normal
// state, and a request we cannot parse must not blank the page.
func ReadServicePending() ServicePending {
	var p ServicePending
	path := ServiceRequestPath()
	st, err := os.Stat(path)
	if err != nil {
		return p
	}
	p.Present = true
	p.Since = st.ModTime().UTC()
	raw, err := os.ReadFile(path)
	if err != nil {
		return p
	}
	for _, line := range strings.Split(string(raw), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		val = strings.Trim(val, `"'`)
		switch key {
		case "ACTION":
			p.Action = val
		case "SERVICE":
			p.Service = val
		case "REQUESTED_AT":
			if ts, perr := time.Parse(time.RFC3339, val); perr == nil {
				p.Since = ts.UTC()
			}
		}
	}
	return p
}

// PostRestart is the answer to «сработал ли последний перезапуск».
type PostRestart string

const (
	// PostRestartNone — the helper has never written a verdict.
	PostRestartNone PostRestart = "none"
	// PostRestartPending — a verdict exists but it is NEWER than this measurement
	// (or its timestamp is unusable), so it says nothing about the service the page
	// is showing. Loading the page again re-measures and answers.
	PostRestartPending PostRestart = "pending"
	// PostRestartOK — the helper said ok AND the service answers right now.
	PostRestartOK PostRestart = "ok"
	// PostRestartFailed — the helper failed, or the service the page just measured
	// is not answering even though the helper said ok.
	PostRestartFailed PostRestart = "failed"
)

// PostRestartVerdict orders the applier's verdict against the current measurement.
//
// WHY AN ORDERING AND NOT A FLAG: the verdict file and the measurement are taken at
// different times by different processes, and the interesting case is exactly the
// one a flag gets wrong — a page rendered one millisecond after the restart shows
// "ok" from the helper next to a service that is still starting. If the verdict is
// not OLDER than the measurement, we have not verified anything yet and we say so.
func PostRestartVerdict(f ServiceFacts, r ServiceApplyResult) PostRestart {
	if !r.Present {
		return PostRestartNone
	}
	ts, err := time.Parse(time.RFC3339, strings.TrimSpace(r.UpdatedAt))
	if err != nil {
		// A verdict with no usable timestamp cannot be ordered. Treat it as «not
		// verified yet» rather than crediting it with the current state.
		return PostRestartPending
	}
	if !f.MeasuredAt.IsZero() && !ts.Before(f.MeasuredAt) {
		return PostRestartPending
	}
	if r.OK && f.State.Healthy() && f.APIOK {
		return PostRestartOK
	}
	return PostRestartFailed
}
