// service_ranges_b455_test.go — B455 (2026-10-30). The operator's question was «каких адресов не
// хватает для discord», and the answer has to stay TRUE: the web/gateway side is covered by DNS, the
// voice side is not nameable, and only a registry entry can carry it.
package exit_rules

import (
	"net/netip"
	"strings"
	"testing"
)

func TestServiceExtraRangesMatchesDiscordDomains_B455(t *testing.T) {
	for _, domain := range []string{
		"discord.com", "discord.gg", "cdn.discordapp.com", "media.discordapp.net",
		"gateway.discord.gg", "ptb.discord.com", "canary.discord.com", "discordapp.com",
		"stable.dl2.discordapp.net", "DISCORD.COM",
	} {
		name, prefixes, why := ServiceExtraRanges(domain)
		if name != "Discord" {
			t.Errorf("%s: service = %q, want Discord", domain, name)
		}
		if len(prefixes) == 0 || prefixes[0] != "66.22.192.0/18" {
			t.Errorf("%s: prefixes = %v, want the measured voice range 66.22.192.0/18", domain, prefixes)
		}
		if !strings.Contains(why, "voice") {
			t.Errorf("%s: the reason must say what breaks without the range: %q", domain, why)
		}
	}
}

// TestServiceExtraRangesDoesNotOverreach_B455: a false positive announces 16 384 addresses for an
// unrelated site, so the match must be on a DOT boundary and nothing else may match.
func TestServiceExtraRangesDoesNotOverreach_B455(t *testing.T) {
	for _, domain := range []string{"mydiscord.com", "notdiscord.gg", "discord.com.evil.example", "rutracker.org", ""} {
		if name, prefixes, _ := ServiceExtraRanges(domain); name != "" || len(prefixes) != 0 {
			t.Errorf("%s matched %q / %v — a suffix must be on a dot boundary", domain, name, prefixes)
		}
	}
}

// TestServiceRegistryRangesAreValidCIDRs_B455 keeps the table itself honest: a typo in a range would
// be silently dropped by the resolver and look like the registry does nothing.
func TestServiceRegistryRangesAreValidCIDRs_B455(t *testing.T) {
	sum := ServiceRangeSummary()
	if len(sum) == 0 {
		t.Fatal("the registry is empty — this test would pass vacuously")
	}
	for _, s := range sum {
		if s.Name == "" || len(s.Domains) == 0 || len(s.Prefixes) == 0 || s.Why == "" {
			t.Errorf("incomplete entry: %+v", s)
		}
		for _, p := range s.Prefixes {
			if _, err := netip.ParsePrefix(p); err != nil {
				t.Errorf("%s: %q is not a CIDR: %v", s.Name, p, err)
			}
		}
	}
}

// TestServiceExtraRangesReturnsACopy_B455: the caller appends to the slice it gets, so a shared
// backing array would corrupt the registry for every later domain in the same pass.
func TestServiceExtraRangesReturnsACopy_B455(t *testing.T) {
	_, first, _ := ServiceExtraRanges("discord.com")
	first[0] = "203.0.113.0/24"
	_, second, _ := ServiceExtraRanges("discord.com")
	if second[0] != "66.22.192.0/18" {
		t.Fatalf("the registry was mutated through a returned slice: %v", second)
	}
}
