// Package exit_rules — resolved_by_domain_b185_test.go pins
// the B185 LookupResolvedForDomain merge behaviour. The CDN
// detector in cdn.go stores resolved subnets with
// parent_domain = "cdn:<provider>:<domain>" (e.g.
// "cdn:cloudflare:discord.gg"); the autoupdater's direct
// net.LookupHost path stores parent_domain = "<domain>"
// (e.g. "discord.gg"). A DOMAIN rule with target_value =
// "discord.gg" must match BOTH — otherwise the B184 three-
// state badge would show ⏳ pending for every Cloudflare/
// Fastly/Google/Akamai-routed domain even when its CDN
// ranges are already in headscale ApprovedRoutes.
//
// The 4 cases cover the merged-lookup contract: direct
// match, cdn alias match, both, neither.
package exit_rules

import "testing"

// TestLookupResolvedForDomain_DirectMatch — autoupdater's
// direct net.LookupHost path (parent_domain = domain).
func TestLookupResolvedForDomain_DirectMatch(t *testing.T) {
	resolved := map[string]map[string]bool{
		"6:29:emilia:discord.gg": {"1.1.1.1/32": true, "1.1.1.2/32": true},
	}
	got := LookupResolvedForDomain(resolved, 6, 29, "emilia", "discord.gg")
	if len(got) != 2 || !got["1.1.1.1/32"] || !got["1.1.1.2/32"] {
		t.Errorf("direct match: got %v, want 2 CIDRs", got)
	}
}

// TestLookupResolvedForDomain_CDNAlias — the B185 root
// cause: parent_domain = "cdn:cloudflare:discord.gg"
// must also match a DOMAIN rule with target_value =
// "discord.gg".
func TestLookupResolvedForDomain_CDNAlias(t *testing.T) {
	resolved := map[string]map[string]bool{
		"6:29:emilia:cdn:cloudflare:discord.gg": {
			"104.16.0.0/12": true, "172.64.0.0/13": true, "162.158.0.0/15": true,
		},
	}
	got := LookupResolvedForDomain(resolved, 6, 29, "emilia", "discord.gg")
	if len(got) != 3 {
		t.Errorf("cdn alias: got %v, want 3 Cloudflare CIDRs", got)
	}
}

// TestLookupResolvedForDomain_BothMerged — when both
// formats exist for the same domain, the helper must
// return the union (not just one or the other).
func TestLookupResolvedForDomain_BothMerged(t *testing.T) {
	resolved := map[string]map[string]bool{
		"6:29:emilia:discord.gg":                 {"1.1.1.1/32": true},
		"6:29:emilia:cdn:cloudflare:discord.gg": {"104.16.0.0/12": true, "162.158.0.0/15": true},
	}
	got := LookupResolvedForDomain(resolved, 6, 29, "emilia", "discord.gg")
	if len(got) != 3 {
		t.Errorf("merged: got %v, want 3 (1 from direct + 2 from cdn)", got)
	}
}

// TestLookupResolvedForDomain_None — no entries anywhere
// for this domain. Must return nil (caller treats nil ==
// "no resolution yet" → ⏳ pending).
func TestLookupResolvedForDomain_None(t *testing.T) {
	resolved := map[string]map[string]bool{
		"6:29:emilia:other-domain.com": {"1.1.1.1/32": true},
	}
	got := LookupResolvedForDomain(resolved, 6, 29, "emilia", "discord.gg")
	if got != nil {
		t.Errorf("none: got %v, want nil", got)
	}
}

// TestLookupResolvedForDomain_DifferentCDN — the helper
// must NOT match a cdn alias for a DIFFERENT domain
// (e.g. "cdn:cloudflare:discord.gg" must NOT match a
// DOMAIN rule with target_value = "discord.com").
func TestLookupResolvedForDomain_DifferentCDN(t *testing.T) {
	resolved := map[string]map[string]bool{
		"6:29:emilia:cdn:cloudflare:discord.gg": {"104.16.0.0/12": true},
	}
	got := LookupResolvedForDomain(resolved, 6, 29, "emilia", "discord.com")
	if got != nil {
		t.Errorf("wrong-domain match: got %v, want nil (discord.gg cdn rows must not match discord.com rule)", got)
	}
}
