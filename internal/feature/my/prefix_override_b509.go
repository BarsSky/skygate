package my

// prefix_override_b509.go — B509 (2026-11-25)
//
// THE EXIT-NODE CHOICE IS NOT ABSOLUTE, AND /my/exit-nodes SAID NOTHING ABOUT IT.
//
// Operator report, verbatim in spirit: «самое проблемное что если отключать сбрасывать и
// прочее на a71 то он всё равно работает только с sharlotta выбранной в приложении, если
// ставить karolina то не идёт» — i.e. the choice appeared to behave inconsistently. It
// does not: it behaves EXACTLY as the tailnet routes say, and the tailnet is per-destination.
//
// Measured on the panel host with ONE exit node selected (seven echo endpoints, five
// answers): three destinations left through the selected relay and three through a
// different one. The reason is the same rule that makes per-device CDN rules work at all
// (B274/B275): skygate pins a prefix to exactly one relay, and a destination inside that
// prefix — typically a CDN range named by a rule — leaves through the PREFIX OWNER, while
// the selected exit node only governs the destinations no advertised prefix covers.
//
// A user who picks an exit node therefore has no way to know that part of their traffic
// goes out somewhere else. This file adds the missing statement to the page where the
// choice is made. It is deliberately a STATEMENT, not a control: the pin is not a defect
// to be cleared here (clearing it would break the rules that need it), and B507 taught us
// that a page must not promise something the system does not do.
//
// The count is the page's gate: a tailnet with no pinned prefixes has nothing to explain,
// so the note stays silent rather than speculating.

import (
	"database/sql"
	"strings"

	"skygate/internal/prefixowner"
)

// defaultRoutePrefixes are the two catch-all prefixes. Every relay advertises them, so
// they carry no "this destination goes through a particular relay" meaning — counting
// them would make the note appear on a tailnet with zero pinned destinations.
func isDefaultRoutePrefix(prefix string) bool {
	switch strings.TrimSpace(prefix) {
	case "0.0.0.0/0", "::/0":
		return true
	}
	return false
}

// countPinnedIn is the pure half: the counting rule over the assignment table's rows.
// Split out so the rule can be tested without a database (the query is one SELECT and
// carries no logic of its own).
func countPinnedIn(owners map[string]string) int {
	n := 0
	for prefix := range owners {
		if !isDefaultRoutePrefix(prefix) {
			n++
		}
	}
	return n
}

// countPinnedPrefixes reports how many prefixes are pinned to a specific relay, i.e. how
// much of the internet is NOT governed by the selected exit node. A query error answers
// 0: the note is an explanation, and an explanation must never be the reason a page
// fails or claims something it could not read.
func countPinnedPrefixes(d *sql.DB) int {
	if d == nil {
		return 0
	}
	return countPinnedIn(prefixowner.OwnerByPrefix(d))
}
