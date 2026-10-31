package admin

// tailnet_dns_b458.go — B458 (2026-10-31). The page half of "DNS must not be carried by
// accident".
//
// The sync (internal/feature/exit_rules) keeps the resolver prefixes in the assignment
// table as source='system' rows. This file answers the operator's question on the page:
// WHICH resolvers the control plane hands out, WHERE each one is carried, and WHICH of them
// is advertised by nobody — because that last case is the silent leak: the client is told to
// query 1.1.1.1, the tailnet has no route for it, and the query quietly leaves through the
// uplink (or dies on a filtered network).
//
// It reads the assignment table directly instead of reusing the page's rendered rows: those
// are CAPPED (the prefix card shows a window of the table), and a resolver that fell outside
// the window would be reported as "not carried" — a wrong statement about the operator's DNS
// is exactly what this block exists to prevent.

import (
	"log"
	"strings"

	"skygate/internal/headscale"
)

// TailnetDNSPrefix is one resolver prefix and what the data plane says about it.
type TailnetDNSPrefix struct {
	Prefix     string
	Carried    bool
	Advertised bool
	Owner      string
	Source     string
}

// TailnetDNSStatus is the whole resolver story for the page.
type TailnetDNSStatus struct {
	Known     bool
	Source    string
	Note      string
	Resolvers []string
	Skipped   []string
	Rows      []TailnetDNSPrefix
	// NotCarried lists the resolver prefixes that NO relay advertises — for those the
	// operator's DNS leaves the tailnet, which is exactly the state that must not be
	// silent.
	NotCarried []string
}

// loadTailnetDNSStatus composes the card from the assignment table and the live
// advertised-routes view.
func (s *Service) loadTailnetDNSStatus() TailnetDNSStatus {
	resolvers := headscale.TailnetDNSResolvers()
	st := TailnetDNSStatus{
		Source:    resolvers.Source,
		Note:      resolvers.Note,
		Resolvers: append([]string{}, resolvers.Global...),
		Known:     resolvers.Known(),
	}
	prefixes, skipped := headscale.ResolverPrefixes(resolvers)
	st.Skipped = skipped
	if len(prefixes) == 0 {
		return st
	}
	owners := map[string]PrefixOwnerRow{}
	if rows, err := s.dbc().Query(`SELECT prefix, COALESCE(exit_node_id,''), COALESCE(source,'auto')
	                                  FROM prefix_owner`); err == nil {
		for rows.Next() {
			var r PrefixOwnerRow
			if rows.Scan(&r.Prefix, &r.ExitNode, &r.Source) == nil {
				owners[r.Prefix] = r
			}
		}
		rows.Close()
	} else {
		log.Printf("[exit-nodes] tailnet-dns: cannot read prefix_owner: %v", err)
	}
	// The advertised sets, from the same live view the rest of the page uses. An
	// unreachable headscale leaves `advertised` empty, and that must be reported as "we
	// could not ask", not as "nobody advertises" (the B294 lesson).
	advertised := map[string]bool{}
	haveLive := false
	if nodes, err := s.HSGlobalFn().ListAllNodes(); err == nil {
		haveLive = true
		for _, n := range nodes {
			for _, p := range n.AvailableRoutes {
				advertised[strings.ToLower(strings.TrimSpace(p))] = true
			}
		}
	} else {
		log.Printf("[exit-nodes] tailnet-dns: cannot read the advertised routes: %v", err)
	}
	for _, p := range prefixes {
		row, ok := owners[p]
		carried := ok && strings.TrimSpace(row.ExitNode) != ""
		add := carried && haveLive && advertised[strings.ToLower(p)]
		st.Rows = append(st.Rows, TailnetDNSPrefix{
			Prefix:     p,
			Carried:    carried,
			Advertised: add,
			Owner:      row.ExitNode,
			Source:     row.Source,
		})
		if carried && haveLive && !add {
			st.NotCarried = append(st.NotCarried, p)
		}
	}
	return st
}
