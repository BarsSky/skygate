// service_ranges_b455.go — B455 (2026-10-30). «каких адресов не хватает для discord — не все
// покрыто текущими правилами».
//
// MEASURED ON THE REFERENCE INSTALL, 2026-10-30: every address the Discord DOMAINS resolve to was
// already covered — `discord.com`/`ptb`/`canary` → 162.159.128-138.232/233, `discord.gg`/
// `gateway.discord.gg` → 162.159.130-136.234, `cdn.discordapp.com`/`discordapp.com` →
// 162.159.129-135.233, `media.discordapp.net` → 162.159.128-134.232 — all inside the
// `162.158.0.0/15` row. What was NOT covered is Discord's VOICE/MEDIA range, `66.22.192.0/18`:
// Discord's client is HANDED a voice server as `IP:port` by the gateway, so no DNS name exists to
// write a domain rule against and no domain rule can ever cover it. Chat and attachments worked;
// voice/созвоны could not.
//
// That is not a Discord quirk: a whole class of services (RTC/voice) works this way, so the ranges
// belong in a REGISTRY next to the CDN one instead of a hand-made CIDR rule per install. The rule
// expansion below appends a service's ranges to the derived set for a matching domain, which means
// one `discord.com` rule now covers the voice range as well — and the addition is announced through
// the normal prefix-owner path, so the operator sees who carries it.
package exit_rules

import "strings"

// ServiceRanges is one well-known service whose endpoints are partly announced as IP:port rather
// than by DNS, with the non-DNS ranges it needs.
type ServiceRanges struct {
	// Name is the provider label, also used in the derived row's parent marker.
	Name string
	// Domains are matched as SUFFIXES of the rule's domain, so `cdn.discordapp.com` and
	// `discord.com` both match `.discord.com`-style entries below.
	Domains []string
	// Prefixes are the ranges to add on top of what DNS resolves.
	Prefixes []string
	// Why is the operator-facing sentence for the panel/log: what breaks without this range.
	Why string
}

// serviceRangeRegistry is a closed list, deliberately small: a range added here is ANNOUNCED by a
// relay and can move prefixes, so it must be justified by a measurement, not by a guess. Discord is
// the case that was measured; Telegram's call ranges are the next candidate and are NOT here yet.
var serviceRangeRegistry = []ServiceRanges{
	{
		Name:     "Discord",
		Domains:  []string{"discord.com", "discord.gg", "discordapp.com", "discordapp.net", "discord.media", "discord.tools"},
		Prefixes: []string{"66.22.192.0/18"},
		Why:      "Discord hands the voice server to the client as IP:port from 66.22.192.0/18 — there is no DNS name to write a domain rule against, so chat works and voice/calls do not",
	},
}

// ServiceExtraRanges returns the additional prefixes a domain's rule needs, plus the service name
// and the reason. An empty result means "DNS is the whole answer for this domain".
//
// Matching is a suffix match on a dot boundary, so `mydiscord.com` does NOT match `discord.com`
// (a false positive here would announce 16k addresses for an unrelated site).
func ServiceExtraRanges(domain string) (service string, prefixes []string, why string) {
	d := strings.ToLower(strings.TrimSpace(domain))
	if d == "" {
		return "", nil, ""
	}
	for _, s := range serviceRangeRegistry {
		for _, want := range s.Domains {
			if d == want || strings.HasSuffix(d, "."+want) {
				return s.Name, append([]string{}, s.Prefixes...), s.Why
			}
		}
	}
	return "", nil, ""
}

// ServiceRangeSummary lists every registered service for the panel and for a test, so "what does
// skygate add on top of DNS" is answerable without reading this file. The slices are copied too:
// a caller that edits the answer must not be able to edit the registry.
func ServiceRangeSummary() []ServiceRanges {
	out := make([]ServiceRanges, 0, len(serviceRangeRegistry))
	for _, s := range serviceRangeRegistry {
		out = append(out, ServiceRanges{
			Name:     s.Name,
			Domains:  append([]string{}, s.Domains...),
			Prefixes: append([]string{}, s.Prefixes...),
			Why:      s.Why,
		})
	}
	return out
}
