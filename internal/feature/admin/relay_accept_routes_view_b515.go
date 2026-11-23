// relay_accept_routes_view_b515.go — B515.1 (2026-11-22)
//
// VISIBILITY FOR THE LOOP THAT COST A DAY. B514 made skygate force `--accept-routes=false`
// on every relay, but nothing on any page said whether a relay actually had it off — the
// operator learned about the loop only by measuring interface counters on two relays during
// one download.
//
// The distinction this file encodes is DESIRED vs OBSERVED:
//
//   - desired  — `exit_servers.accept_routes`, the tri-state the operator/UI asked for
//     (-1 = false, 0 = leave alone, 1 = true). Since B514 a relay always gets false
//     regardless, so the stored value is not what the page should shout about.
//   - observed — what the relay itself reported the last time the sync read it back. This is
//     the only thing that can prove the loop is gone, and it must be READ, never inferred.
//
// A relay whose observed state is "true" is an ACTIVE LOOP: exit traffic arriving there is
// forwarded back into the tailnet instead of leaving through its own uplink. That is a
// danger-level row, not a warning. "unknown" (never read, unreadable, unreachable) is a
// warning: it is a fact about the view, not about the relay, and the page must say which.
package admin

import "strings"

// relayAcceptRoutesSeverity is the page's tone for one relay.
type relayAcceptRoutesSeverity string

const (
	// relayAcceptRoutesOK — the relay reported "false": it carries what it advertises out
	// through its own uplink, which is the only correct state for a relay.
	relayAcceptRoutesOK relayAcceptRoutesSeverity = "ok"
	// relayAcceptRoutesLoop — the relay reported "true": an active loop.
	relayAcceptRoutesLoop relayAcceptRoutesSeverity = "danger"
	// relayAcceptRoutesUnknown — nobody has read it back yet, or the read failed. A fact
	// about the view; it must never be rendered as "fine".
	relayAcceptRoutesUnknown relayAcceptRoutesSeverity = "warn"
)

// classifyRelayAcceptRoutes turns the observed string into the page's verdict.
//
// `observed` is the raw string the sync stored: "true", "false", or anything else (empty,
// an error message, "routeall=<nil>"). Only the two exact words carry meaning; everything
// else is unknown ON PURPOSE — a parser that guesses would recreate the "green because we
// did not look" failure this block exists to end.
func classifyRelayAcceptRoutes(observed string) relayAcceptRoutesSeverity {
	switch observed {
	case "false":
		return relayAcceptRoutesOK
	case "true":
		return relayAcceptRoutesLoop
	default:
		return relayAcceptRoutesUnknown
	}
}

// annotateObservedAcceptRoutes fills the B515.1 columns on every row from the store the
// sync writes (relay hostname → the relay's own answer) and returns how many rows are an
// active loop and how many were never measured.
//
// It is pure and lives here, not in the handler, for the same reason the classifier does:
// the row/state pairing is the part that can be wrong (a hostname that does not match
// silently degrades every row to "unknown", which looks identical to "nobody deployed
// yet"). Matching is case-insensitive because the store's key is whatever the sync called
// the relay and headscale's own spelling is not guaranteed to match exit_servers byte for
// byte.
func annotateObservedAcceptRoutes(nodes []ExitNodeInfo, observed map[string]string) (loops, unknown int) {
	byLower := make(map[string]string, len(observed))
	for k, v := range observed {
		byLower[strings.ToLower(strings.TrimSpace(k))] = v
	}
	for i := range nodes {
		raw, ok := observed[nodes[i].Hostname]
		if !ok {
			raw = byLower[strings.ToLower(strings.TrimSpace(nodes[i].Hostname))]
		}
		nodes[i].ObserveAcceptRoutes = raw
		nodes[i].ObserveAcceptRoutesTone = string(classifyRelayAcceptRoutes(raw))
		switch classifyRelayAcceptRoutes(raw) {
		case relayAcceptRoutesLoop:
			loops++
		case relayAcceptRoutesUnknown:
			unknown++
		}
	}
	return loops, unknown
}
