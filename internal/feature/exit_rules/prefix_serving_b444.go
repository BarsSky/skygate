// prefix_serving_b444.go — B444 (2026-10-29) — «собирает необходимую информацию о том какие
// подсети и адреса необходимо также добавить в список на маршрутизацию»: the diagnosis of a
// prefix that NOBODY serves.
//
// The assignment table decides who serves a prefix (B275) and the relays advertise what the
// table gives them (B441). Both can still be wrong at once in a way no page said out loud: a
// prefix the table owns, whose owner is not advertising it (or advertises it without
// approval), is a destination every client pinned to that prefix cannot reach. Before this
// block the only signal was a system test that sampled three domain rules and a "prefix
// conflicts" log line.
//
// Two facts are deliberately separate:
//
//   - the METRIC (prefixes not applied in the last sync pass) is about OUR apply — it is
//     periodic and names the transport problem (SSH unreachable, relay refused the set);
//   - the DIAGNOSTIC (prefixes nobody advertises) is the LIVE truth read from headscale,
//     which is what a client actually sees.
//
// Pure functions only: both halves are computed from maps the caller already has, so the
// rule is testable without a database, a relay or a headscale client.
package exit_rules

import (
	"net/netip"
	"sort"
	"strings"
)

// UnservedPrefix is one prefix the assignment table owns that its owner does not serve.
type UnservedPrefix struct {
	Prefix string
	Owner  string
	// Reason is a sentence the operator can act on, not a code.
	Reason string
}

// ServingReport is the answer for the whole table.
type ServingReport struct {
	// Owned is how many prefixes the table assigns at all.
	Owned int
	// Served is how many of them are advertised by their owner.
	Served int
	// Unserved names the rest, sorted by prefix so the page does not shuffle.
	Unserved []UnservedPrefix
}

// ReportServing compares the assignment table with what the relays actually advertise.
//
// `advertisedByRelay` maps a relay hostname to the prefixes headscale reports as APPROVED
// for it — the client-visible truth, not our intention. A prefix counts as served when the
// owner advertises it OR a covering prefix (a /24 covers a /32 inside it): advertising the
// larger range is exactly how the CDN path (B183/B274/B298) serves its members.
func ReportServing(owners map[string]string, advertisedByRelay map[string][]string) ServingReport {
	rep := ServingReport{Owned: len(owners)}
	if len(owners) == 0 {
		return rep
	}
	// Pre-parse each relay's advertised prefixes ONCE — the table can hold hundreds of
	// prefixes and re-parsing per comparison would be quadratic in string work.
	advertised := make(map[string][]netip.Prefix, len(advertisedByRelay))
	for relay, list := range advertisedByRelay {
		parsed := make([]netip.Prefix, 0, len(list))
		for _, p := range list {
			if pf, err := netip.ParsePrefix(strings.TrimSpace(p)); err == nil {
				parsed = append(parsed, pf)
			}
		}
		advertised[strings.ToLower(strings.TrimSpace(relay))] = parsed
	}

	prefixes := make([]string, 0, len(owners))
	for p := range owners {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)

	for _, p := range prefixes {
		owner := strings.ToLower(strings.TrimSpace(owners[p]))
		if owner == "" {
			rep.Unserved = append(rep.Unserved, UnservedPrefix{Prefix: p, Owner: "",
				Reason: "the assignment table has no owner for this prefix"})
			continue
		}
		list, known := advertised[owner]
		if !known {
			rep.Unserved = append(rep.Unserved, UnservedPrefix{Prefix: p, Owner: owner,
				Reason: "the owner relay is not in headscale's node list (deleted, renamed or never registered)"})
			continue
		}
		if prefixCoveredBy(list, p) {
			rep.Served++
			continue
		}
		rep.Unserved = append(rep.Unserved, UnservedPrefix{Prefix: p, Owner: owner,
			Reason: "the owner relay does not advertise this prefix, so clients pinned to it cannot reach the destination"})
	}
	return rep
}

// prefixCoveredBy reports whether any advertised prefix contains the target exactly or as a
// covering range.
//
// THE BASE EXIT ROUTES DO NOT COUNT. Every exit node advertises `0.0.0.0/0` and `::/0` (the
// sync always prepends them), and a default route technically "contains" every prefix — so
// counting it would make this whole diagnostic vacuous, reporting "all served" on an install
// where nothing is. The routing decision for a specific destination is longest-prefix match
// in the tailnet: a relay becomes the subnet router for 198.51.100.0/24 by advertising THAT
// prefix (or a covering one that is not the default), not by being an exit node.
func prefixCoveredBy(advertised []netip.Prefix, target string) bool {
	pf, err := netip.ParsePrefix(strings.TrimSpace(target))
	if err != nil {
		// Not a prefix we can reason about (a bare domain, an empty string): it can only be
		// matched literally, which the loop below does not do — report it as UNKNOWN rather
		// than silently counting it as served.
		return false
	}
	addr := pf.Addr()
	for _, a := range advertised {
		if a == pf {
			return true
		}
		if a.Bits() == 0 {
			continue // 0.0.0.0/0 or ::/0 — the base exit routes, not a claim about this prefix
		}
		// A covering range: the advertised prefix contains the whole target prefix.
		if a.Bits() <= pf.Bits() && a.Contains(addr) {
			return true
		}
	}
	return false
}

// relayApplyLooksFailed turns one sync result label into "did this relay accept its routes".
//
// The label shape is the one the flash and the journal already carry — "ssh=ok approved=3",
// "local=ok via …", "ssh=err=…", "local=err=…", "info=no rules target this node …". A label
// containing "err=" is a failure; an empty label means the relay was never visited, which is
// also not a success. This is a PURE reading of a string the caller already has, so the
// metric cannot disagree with what the operator sees on the sync flash.
func relayApplyLooksFailed(label string) bool {
	l := strings.ToLower(strings.TrimSpace(label))
	if l == "" {
		return true
	}
	if strings.Contains(l, "err=") {
		return true
	}
	// "info=…" means the relay was deliberately not configured this pass (no rules target
	// it) — not a failure of the transport, but it also did not apply anything, so a prefix
	// that believes this relay serves it is NOT applied.
	return strings.HasPrefix(l, "info=")
}

// CountOwnedPrefixesNotApplied counts the table's prefixes whose owner did not accept its
// routes in this pass — the periodic metric. It is intentionally narrower than
// ReportServing (it cannot see headscale's approved set), and its name says so.
func CountOwnedPrefixesNotApplied(owners map[string]string, resultByRelay map[string]string) int {
	n := 0
	for _, owner := range owners {
		owner = strings.ToLower(strings.TrimSpace(owner))
		if owner == "" {
			n++
			continue
		}
		label, visited := resultByRelay[owner]
		if !visited {
			// A relay the assignment table names but the sync never visited: it did not
			// apply anything. (B441 made this impossible for the table-owned set; counting
			// it keeps the metric honest if that regresses.)
			n++
			continue
		}
		if relayApplyLooksFailed(label) {
			n++
		}
	}
	return n
}
