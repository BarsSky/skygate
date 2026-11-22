package exit_rules

// route_idempotence_b511.go — B511 (2026-11-22)
//
// THE BACKGROUND WAVE: every staggered-sync pass re-approved every relay's routes, even
// when the set had not changed by a single prefix, and the reason the set DID change was
// logged as a bare count ("prefix ownership changed (inserted=9 changed=5)").
//
// Measured on the reference host (read-only, one 6-hour window):
//
//   * 36 ACL re-applies in 24h, almost all of them "prefix ownership changed
//     (inserted=N changed=M)" with N between 1 and 48;
//   * the generated policy shrank 97393 → 90585 bytes over six hours — i.e. the writes
//     were following REAL ownership moves, not phantom diffs;
//   * a diff of prefix_owner across one reconcile tick showed added=0 removed=0 and every
//     owner unchanged, while updated_at moved on 311 of 316 rows — so the moves happen in
//     BURSTS between ticks, and the counts alone cannot say which prefixes moved or why.
//
// Two consequences, both fixed here:
//
//  1. The APPROVAL step ran unconditionally. Approving routes is a headscale state change
//     (it changes what clients are told the tailnet serves), so re-approving an unchanged
//     set makes headscale fan a fresh netmap out to every device. On Wi-Fi that is
//     invisible; on a cellular client it lands in the middle of a stream. The step now
//     asks headscale what the relay already has and does nothing when it already has it.
//  2. A bare count is not a finding. When the reconcile reports inserted/changed, the log
//     now NAMES a bounded sample (prefix, old → new owner, source) plus the healthy set,
//     so the next investigation starts from the actual prefixes instead of a number.
//
// What this file deliberately does NOT do: it does not suppress a REAL move. If a prefix
// changed owner, the approval must still run — that is the whole point of the assignment
// table. Only "nothing changed" is skipped, and it says so.

import (
	"sort"
	"strings"

	"skygate/internal/headscale"
)

// routeSetEqual compares two route lists as SETS: order and duplicates are noise (the
// same prefix listed twice is still one route), and a re-ordered list is not a change.
func routeSetEqual(a, b []string) bool {
	return len(normalizeRouteSet(a)) == len(normalizeRouteSet(b)) &&
		routeSetCovers(a, b)
}

// routeSetCovers reports whether `have` contains every route in `want`.
func routeSetCovers(have, want []string) bool {
	set := normalizeRouteSet(have)
	for r := range normalizeRouteSet(want) {
		if !set[r] {
			return false
		}
	}
	return true
}

// normalizeRouteSet trims and de-duplicates, so a list built by two different code paths
// (the assignment table, the rule set, headscale's own serialisation) compares equal when
// it means the same thing.
func normalizeRouteSet(routes []string) map[string]bool {
	out := make(map[string]bool, len(routes))
	for _, r := range routes {
		r = strings.TrimSpace(r)
		if r != "" {
			out[r] = true
		}
	}
	return out
}

// sortedRouteSet renders a set deterministically — for logs, so two passes are comparable
// by eye and a test can pin the wording.
func sortedRouteSet(routes []string) []string {
	set := normalizeRouteSet(routes)
	out := make([]string, 0, len(set))
	for r := range set {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// relayRouteState returns what headscale currently reports for a relay. `known` is false
// when headscale could not be read or does not know the node — in that case the caller must
// fall back to approving (a missing answer is not evidence that nothing changed).
func relayRouteState(hs *headscale.Client, hostname string) (advertised, approved []string, known bool) {
	if hs == nil || strings.TrimSpace(hostname) == "" {
		return nil, nil, false
	}
	nodes, err := hs.ListAllNodes()
	if err != nil {
		return nil, nil, false
	}
	want := strings.TrimSpace(hostname)
	for _, n := range nodes {
		if !strings.EqualFold(n.GivenName, want) && !strings.EqualFold(n.Hostname, want) {
			continue
		}
		return n.AvailableRoutes, n.ApprovedRoutes, true
	}
	return nil, nil, false
}

// routesAlreadyApproved answers the only question the approval step needs: does the relay
// already have every route we are about to ask for? An empty target is not "approved"
// (there is nothing to approve, but the caller still wants its base routes).
func routesAlreadyApproved(approved, want []string) bool {
	if len(normalizeRouteSet(want)) == 0 {
		return false
	}
	return routeSetCovers(approved, want)
}

// ownershipChangeSample renders up to `limit` ownership changes as
// "prefix→relay(source)" for the ones that MOVED, and "prefix→relay" for new rows. It is
// the answer to "inserted=9 — WHICH nine?", which the counts alone never gave.
func ownershipChangeSample(before, after map[string]string, limit int) string {
	if limit <= 0 {
		return ""
	}
	prefixes := make([]string, 0, len(after))
	for p := range after {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	var parts []string
	for _, p := range prefixes {
		oldOwner, existed := before[p]
		newOwner := after[p]
		switch {
		case !existed:
			parts = append(parts, p+"→"+newOwner+"(new)")
		case oldOwner != newOwner:
			parts = append(parts, p+"→"+newOwner+"(was "+oldOwner+")")
		}
		if len(parts) >= limit {
			break
		}
	}
	return strings.Join(parts, ", ")
}
