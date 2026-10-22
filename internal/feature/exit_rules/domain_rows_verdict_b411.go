// domain_rows_verdict_b411.go — B411 (2026-10-22): a rotating CDN is not a
// broken mechanism.
//
// # WHY
//
// Measured on the operator's installation minutes apart, on a healthy panel:
//
//	exit_rules.domain_prefix :: 6 of 13 address(es) are stale or missing:
//	  agent.minimax.io         → 62.115.253.64 is in no enabled row
//	  cdn-registry-1.docker.io → 54.156.126.76 is in no enabled row
//	  cdn-registry-1.docker.io → 23.23.191.49 is in no enabled row
//	DNS auto-update is ON (every 6h0m0s) and will rewrite them on its next pass.
//
// and then, after the next pass ran (forced by clearing its markers):
//
//	same table: 5 published ranges untouched, 8 stale /32 rows removed,
//	62.115.253.65/32 written as a gap — the mechanism worked exactly as designed.
//
// The mechanism was fine both times; the ASSERTION was the problem. It compared
// today's live answers with the stored rows and called any difference "stale",
// which cannot be satisfied for a domain whose answers rotate every few minutes
// (Akamai 62.115.x, AWS registry edges) while the re-resolve interval is six
// hours: between two passes the panel is GUARANTEED to see answers the rows do not
// contain yet. A diagnostic that is red for a healthy system teaches the operator
// to ignore it — the exact failure mode B403/B405 were written to end.
//
// # WHAT THE VERDICT IS NOW
//
// The question a diagnostic can answer is not "is today's DNS inside the rows"
// (that is a race) but "will this heal by itself, or is the mechanism broken".
// Three answers, decided by the same policy the updater uses (B308's interval and
// its per-domain last-resolve stamp):
//
//	Covered          every answer is inside a stored row (or a stored range).
//	PendingNextPass  some answer is newer than the last resolve — expected for a
//	                 rotating provider; the next due pass covers it. PASS, but SAID.
//	Stale            a pass was DUE (never resolved, or the interval elapsed) and
//	                 the rows still do not cover the answers, or auto-update is off
//	                 so nothing will ever run. This is the pre-B409 defect class
//	                 and it FAILS.
//
// The distinction matters: before B409 the CDN-marker branch never resolved again,
// so a marked domain sat at "stale forever" while auto-update claimed to be ON —
// and the old assertion could not tell that apart from ordinary rotation. Now the
// broken case is the only failing one.
package exit_rules

import (
	"strconv"
	"time"
)

// DomainRowsVerdict is what a diagnostic may honestly say about a domain rule's
// derived rows. See the file header for why "the rows differ from today's DNS"
// is not one of the answers.
type DomainRowsVerdict int

const (
	// DomainRowsCovered — every current answer is inside a stored row.
	DomainRowsCovered DomainRowsVerdict = iota
	// DomainRowsPendingNextPass — the rows are one rotation behind; the interval
	// that B308 enforces will rewrite them on its next due pass.
	DomainRowsPendingNextPass
	// DomainRowsStale — the rows SHOULD have been refreshed and were not: either
	// the domain has never been resolved, the interval has elapsed, or nothing is
	// scheduled to run at all.
	DomainRowsStale
)

// String is used in test-failure messages and on the panel.
func (v DomainRowsVerdict) String() string {
	switch v {
	case DomainRowsCovered:
		return "covered"
	case DomainRowsPendingNextPass:
		return "pending-next-pass"
	case DomainRowsStale:
		return "stale"
	default:
		return "unknown"
	}
}

// DomainRowsVerdictFor classifies one domain rule.
//
//	uncovered  number of today's answers that no stored row contains (0 = covered)
//	lastResolve unix seconds of the last SUCCESSFUL resolve, 0 = never resolved
//	now        unix seconds
//	interval   the effective B308 interval; <= 0 means the gate is disabled and
//	           every tick resolves (the pre-B308 behaviour)
//
// A backwards clock (now < lastResolve) is treated as "the interval has not
// elapsed": the stamp is in the future, which B308 already resolves by resolving
// the domain again; reporting it as Stale would blame the rows for a clock.
func DomainRowsVerdictFor(uncovered int, lastResolve, now int64, interval time.Duration) DomainRowsVerdict {
	if uncovered <= 0 {
		return DomainRowsCovered
	}
	if lastResolve <= 0 {
		// Never resolved. A rule that has never been resolved is due on the very
		// next tick, so if this is still the state the resolver has not run for it
		// at all — that is the defect, not a rotation.
		return DomainRowsStale
	}
	if interval <= 0 {
		// Gate disabled: the next tick resolves every domain, so a difference is
		// transient by construction.
		return DomainRowsPendingNextPass
	}
	elapsed := now - lastResolve
	if elapsed < 0 {
		return DomainRowsPendingNextPass
	}
	if elapsed < int64(interval/time.Second) {
		return DomainRowsPendingNextPass
	}
	// The interval elapsed and the rows still do not cover the answers: the pass
	// that was supposed to run did not fix them.
	return DomainRowsStale
}

// DomainRowsVerdictNote renders the operator-facing sentence for a verdict, so
// the panel, the audit and the tests phrase it the same way.
func DomainRowsVerdictNote(v DomainRowsVerdict, uncovered int, lastResolve, now int64, interval time.Duration) string {
	switch v {
	case DomainRowsCovered:
		return "every current address is inside a stored row"
	case DomainRowsPendingNextPass:
		if interval <= 0 {
			return "the answers moved after the last resolve; the gate is disabled, so the next tick (5m) covers them"
		}
		remain := int64(interval/time.Second) - (now - lastResolve)
		if remain < 0 {
			remain = 0
		}
		return "the answers moved after the last resolve (" + humanizeSeconds(now-lastResolve) + " ago); the next pass in " +
			humanizeSeconds(remain) + " covers them"
	default:
		if lastResolve <= 0 {
			return "this rule has NEVER been resolved — the resolver has not run for it at all"
		}
		return "the last resolve was " + humanizeSeconds(now-lastResolve) + " ago, which is past the interval (" +
			humanizeSeconds(int64(interval/time.Second)) + "), so these rows should already have been rewritten"
	}
}

// humanizeSeconds renders a coarse, language-neutral duration ("3h12m", "45s")
// — the panel wraps it in a translated sentence, so it carries no English words.
func humanizeSeconds(s int64) string {
	if s < 0 {
		s = 0
	}
	itoa := func(i int64) string { return strconv.FormatInt(i, 10) }
	if s < 60 {
		return itoa(s) + "s"
	}
	if s < 3600 {
		return itoa(s/60) + "m"
	}
	h := s / 3600
	m := (s % 3600) / 60
	if m == 0 {
		return itoa(h) + "h"
	}
	return itoa(h) + "h" + itoa(m) + "m"
}
