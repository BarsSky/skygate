// system_tests_prefix_serving_b444.go — B444. THE DIAGNOSTIC the operator asked for: «какие
// подсети и адреса необходимо также добавить в список на маршрутизацию».
//
// The assignment table (B275) decides which relay serves a prefix, and the relays advertise
// what the table gives them (B441). When the two disagree the destination is a black hole for
// every client pinned to it, and until now the only signal was a log line plus a system test
// that sampled three domain rules. This test compares the table with what headscale reports
// as APPROVED for each relay — the client-visible truth — and NAMES what is unserved, with the
// reason in the operator's words.
package admin

import (
	"context"
	"fmt"
	"strings"

	"skygate/internal/feature/exit_rules"
	"skygate/internal/prefixowner"
)

func init() {
	TestRegistry = append(TestRegistry, b444Tests...)
}

var b444Tests = []SystemTestDef{
	{
		Name:        "exit_rules.prefix_serving",
		Category:    "exit_rules",
		Description: "every prefix the assignment table owns is advertised (and approved) by its owner relay",
		Run: func(ctx context.Context) (SystemTestStatus, string) {
			s := getTestService()
			if s == nil || s.dbc() == nil {
				return SystemTestSkip, "service or DB not initialised"
			}
			owners := prefixowner.OwnerByPrefix(s.dbc())
			if len(owners) == 0 {
				return SystemTestSkip, "the assignment table is empty — nothing owns a prefix yet (the next sync pass fills it)"
			}
			hs, err := b394Headscale()
			if err != nil {
				return SystemTestSkip, err.Error()
			}
			nodes, err := hs.ListAllNodes()
			if err != nil {
				return SystemTestFail, "list nodes: " + err.Error()
			}
			advertised := map[string][]string{}
			for _, n := range nodes {
				host := strings.ToLower(strings.TrimSpace(n.Hostname))
				if host == "" {
					continue
				}
				// ApprovedRoutes is what the tailnet actually serves; AvailableRoutes is only
				// what the node offered. Using the advertised-but-unapproved set would report
				// prefixes as served that no client can reach — the exact false positive B273
				// removed from the health ladder.
				advertised[host] = n.ApprovedRoutes
			}
			rep := exit_rules.ReportServing(owners, advertised)
			if len(rep.Unserved) == 0 {
				return SystemTestPass, fmt.Sprintf("all %d owned prefix(es) are advertised and approved by their owner", rep.Owned)
			}
			// Named, bounded, and actionable: the operator needs the relay and the reason, not
			// a count. Ten is enough to act on and short enough to read in the panel.
			var b strings.Builder
			fmt.Fprintf(&b, "%d of %d owned prefix(es) are NOT served by their owner:\n", len(rep.Unserved), rep.Owned)
			for i, u := range rep.Unserved {
				if i == 10 {
					fmt.Fprintf(&b, "… and %d more\n", len(rep.Unserved)-10)
					break
				}
				fmt.Fprintf(&b, "  %s → %s: %s\n", u.Prefix, u.Owner, u.Reason)
			}
			b.WriteString("Fix: /admin/exit-nodes (prefix assignment) or re-run the route sync; a relay that cannot be reached over SSH keeps its prefixes and serves none of them.")
			return SystemTestFail, strings.TrimSpace(b.String())
		},
	},
}
