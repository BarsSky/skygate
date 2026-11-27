// tailnet_dns_b458.go — B458 (2026-10-31). «DNS внутри tailnet должен ехать по
// проекту, а не случайно».
//
// The measurement and the parsing live in internal/headscale (it reads headscale's own
// config); this file is the SYNC half: turn the configured resolvers into system-owned
// prefixes so the route and the ACL follow automatically, and say out loud what could not
// be carried.
//
// Why the sync and not the panel: the resolvers are a property of the control plane, and
// the control plane can change (an operator edits config.yaml and restarts headscale). A
// value that only refreshes when somebody opens a page is the same class of accident this
// block removes — the route must follow the config on the maintenance tick.
package exit_rules

import (
	"log"
	"strings"

	"skygate/internal/headscale"
	"skygate/internal/prefixowner"
)

// ensureTailnetDNSPrefixes makes the assignment table carry exactly the resolvers that
// NEED a route — the ones nothing outside the tailnet can reach. B524 (2026-11-27) is the
// measured reason the set is not "every resolver the tailnet hands out": a public resolver
// carried as a /32 route is MORE SPECIFIC than an exit node's 0.0.0.0/0, so a client that
// accepts subnet routes resolves through whichever relay owns that prefix — wherever it is
// — no matter which exit node the operator picked. Named here, once, on every pass that
// prunes them, because "DNS does not go through the exit node" is invisible on the wire.
//
// It never fails the pass: a DNS route is important, but a sync that aborts because a
// config file was unreadable would take the whole ACL with it.
func (s *Service) ensureTailnetDNSPrefixes() {
	resolvers := headscale.TailnetDNSResolvers()
	if !resolvers.Known() {
		// Named, not silent: "no resolvers" and "cannot read the resolver set" look the
		// same on the wire and must not look the same in the journal.
		if note := strings.TrimSpace(resolvers.Note); note != "" {
			log.Printf("tailnet-dns: the resolver set could not be read (%s) — no DNS prefix is carried this pass; fix SKYGATE_TAILNET_DNS_RESOLVERS or SKYGATE_HEADSCALE_CONFIG_PATH if the tailnet has custom resolvers", note)
		}
		return
	}
	carried, skipped, public := headscale.ResolverPrefixes(resolvers)
	if len(skipped) > 0 {
		log.Printf("tailnet-dns: %d configured resolver(s) cannot be carried as an IPv4 prefix and are skipped: %s (source: %s)",
			len(skipped), strings.Join(skipped, ", "), resolvers.Source)
	}
	if len(public) > 0 {
		log.Printf("tailnet-dns: %d public resolver(s) are NOT carried as subnet routes (B524): %s — a public resolver needs no route, and its /32 would override the 0.0.0.0/0 of every exit node, sending DNS to whichever relay owns the prefix instead of the chosen exit node (source: %s)",
			len(public), strings.Join(public, ", "), resolvers.Source)
	}
	// Always call, even with an empty list: releasing the rows that used to carry a public
	// resolver is what stops the relays from advertising it, and that release only happens
	// inside EnsureSystemPrefixes. A nil slice means "do not manage the set", so an empty
	// answer is normalised to a non-nil one here (see EnsureSystemPrefixes' contract).
	healthy := healthyExitRelaysForAssignment(s.dbc())
	if carried == nil {
		carried = []string{}
	}
	if err := prefixowner.EnsureSystemPrefixesLogged(s.dbc(), carried, healthy, nil); err != nil {
		log.Printf("tailnet-dns: cannot ensure the resolver prefixes %s: %v", strings.Join(carried, ", "), err)
	}
}
