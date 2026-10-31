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

// ensureTailnetDNSPrefixes makes the assignment table carry the resolvers the tailnet hands
// out, as source='system' rows. It never fails the pass: a DNS route is important, but a
// sync that aborts because a config file was unreadable would take the whole ACL with it.
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
	prefixes, skipped := headscale.ResolverPrefixes(resolvers)
	if len(skipped) > 0 {
		log.Printf("tailnet-dns: %d configured resolver(s) cannot be carried as an IPv4 prefix and are skipped: %s (source: %s)",
			len(skipped), strings.Join(skipped, ", "), resolvers.Source)
	}
	if len(prefixes) == 0 {
		return
	}
	healthy := healthyExitRelaysForAssignment(s.dbc())
	if err := prefixowner.EnsureSystemPrefixesLogged(s.dbc(), prefixes, healthy, nil); err != nil {
		log.Printf("tailnet-dns: cannot ensure the resolver prefixes %s: %v", strings.Join(prefixes, ", "), err)
	}
}
