package handlers

// exit_rules_cdn.go — CDN detection for the exit-rules autoupdater.
//
// 2026-07-28: Cloudflare, Fastly, Akamai, and other large CDNs use
// anycast networks that return DIFFERENT IPs to each DNS query
// (especially for high-traffic domains like artstation.com,
// github.com, docker.io). The per-IP /32 approach creates
// constant churn:
//
//   tick 1: add=18 remove=0
//   tick 2: add=0  remove=18   (Cloudflare rotated the IPs)
//   tick 3: add=18 remove=0
//   ...
//
// The fix: detect when a domain's resolved IPs all fall within
// a known CDN's published IP ranges, and replace the per-IP
// rules with the CDN's CIDR ranges. The ranges don't churn
// (they're stable network allocations), so the autoupdater
// has no add/remove work for that domain.
//
// Sources for the CIDR lists:
//   - Cloudflare: https://www.cloudflare.com/ips-v4
//   - Cloudflare IPv6: https://www.cloudflare.com/ips-v6
//   - Fastly:     https://www.fastly.com/static/docs/Fastly-IP-ranges.txt
//   - Akamai:     https://www.akamai.com/content/akamai/en/security-research/threat-intelligence-datafeed.html
//   - Google:     https://support.google.com/a/answer/60764
//   - CloudFront: https://docs.aws.amazon.com/AmazonCloudFront/latest/DeveloperGuide/LocationsOfEdgeServers.html
//
// The lists are hard-coded rather than fetched at runtime so
// the autoupdater works offline. Update quarterly or so.

import (
	"fmt"
	"net"
	"strings"
)

// cdnRange is one CDN's published IP ranges. name is the
// canonical CDN identifier (used in parent_domain markers
// like "cdn:cloudflare:artstation.com"); cidrs is the IPv4
// range list (IPv6 is handled by the existing IPv6-skip
// logic in DomainAutoUpdater).
type cdnRange struct {
	name  string
	cidrs []string
}

// knownCDNs is the registry of major CDN IP ranges. Order
// matters: detectCDN returns the FIRST CDN that matches all
// currentIPs. Cloudflare is listed first because it's the
// most common (artstation, github, docker, etc.).
//
// 2026-07-28 lists as of 2026 H1. Re-check quarterly.
var knownCDNs = []cdnRange{
	{
		name: "cloudflare",
		cidrs: []string{
			"173.245.48.0/20",
			"103.21.244.0/22",
			"103.22.200.0/22",
			"103.31.4.0/22",
			"141.101.64.0/18",
			"108.162.192.0/18",
			"190.93.240.0/20",
			"188.114.96.0/20",
			"197.234.240.0/22",
			"198.41.128.0/17",
			"162.158.0.0/15",
			"104.16.0.0/12",
			"104.24.0.0/14",
			"172.64.0.0/13",
			"131.0.72.0/22",
		},
	},
	{
		name: "fastly",
		cidrs: []string{
			"151.101.0.0/16",
			"199.232.0.0/16",
			"23.235.32.0/20",
			"23.227.32.0/19",
		},
	},
	{
		name: "google",
		cidrs: []string{
			"8.8.8.0/24",
			"8.8.4.0/24",
			"8.34.208.0/20",
			"8.35.192.0/20",
			"8.15.202.0/24",
			"142.250.0.0/15",
			"172.217.0.0/16",
			"216.58.192.0/19",
			"74.125.0.0/16",
			"173.194.0.0/16",
		},
	},
	{
		name: "akamai",
		cidrs: []string{
			"23.32.0.0/11",
			"23.192.0.0/11",
			"104.64.0.0/10",
			"184.24.0.0/13",
			"2.16.0.0/13",
		},
	},
}

// detectCDN returns (cdnName, rangesToUse, matched) where:
//   - cdnName is the first CDN whose published ranges cover ALL
//     IPs in the input set, or "" if no CDN matches.
//   - rangesToUse is the list of CIDR strings to use in place
//     of the per-IP /32 rules. For most CDNs this is the
//     union of all their CIDRs (since we want to cover all
//     possible IPs, not just the ones DNS happened to return
//     this tick).
//   - matched is true if a CDN was found.
//
// detectCDN is the single source of truth for "is this domain
// CDN-served?" and is called by DomainAutoUpdater and the
// regression tests.
//
// "All IPs in CDN" is the criterion — if even ONE IP is
// outside the CDN's ranges, we return no match. This avoids
// the "domain A is on Cloudflare + a tiny AWS edge for
// auth.example.com" case where partial coverage would leave
// a gap.
func detectCDN(ips map[string]bool) (string, []string, bool) {
	if len(ips) == 0 {
		return "", nil, false
	}
	// Parse the CDN CIDRs once (cached at package init).
	for _, cdn := range knownCDNs {
		parsed, err := parseCIDRs(cdn.cidrs)
		if err != nil {
			continue
		}
		matched := 0
		for ipStr := range ips {
			ip := net.ParseIP(ipStr)
			if ip == nil {
				continue
			}
			if cidrsContain(parsed, ip) {
				matched++
			} else {
				// Any unmatched IP breaks the CDN detection for
				// this domain — we want FULL coverage, not partial.
				goto nextCDN
			}
		}
		if matched == len(ips) && matched > 0 {
			return cdn.name, cdn.cidrs, true
		}
	nextCDN:
	}
	return "", nil, false
}

// parseCIDRs parses a list of CIDR strings. The result is a
// slice of *net.IPNet. On any parse error, returns the
// partial result and the error (the autoupdater should
// log and skip if a CDN's list is malformed).
func parseCIDRs(cidrs []string) ([]*net.IPNet, error) {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, s := range cidrs {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return out, fmt.Errorf("parse %q: %w", s, err)
		}
		out = append(out, n)
	}
	return out, nil
}

// cidrsContain returns true if ip falls into any of the
// provided CIDRs.
func cidrsContain(cidrs []*net.IPNet, ip net.IP) bool {
	for _, n := range cidrs {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// cdnParentMarker builds the parent_domain value for a CDN
// rule. The marker lets the autoupdater recognize the rule
// as a CDN-routed rule on the next tick and skip DNS
// resolution entirely (the ranges are stable, no need to
// re-resolve).
//
// Format: "cdn:<cdn>:<original-domain>". The "cdn:" prefix
// distinguishes CDN rules from per-IP /32 rules
// (parent_domain="artstation.com"). Operators can see the
// distinction in /my/exit-rules and the audit log.
func cdnParentMarker(cdnName, domain string) string {
	return "cdn:" + cdnName + ":" + domain
}

// isCDNMarker returns true if the parent_domain string is
// a valid CDN marker. The format is "cdn:<name>:<domain>";
// malformed inputs like "cdn:" or "cdn::" (empty CDN name)
// return false. The autoupdater should not treat those as
// CDN rules — if it sees one, it should fall back to the
// per-IP path or leave the rule alone.
func isCDNMarker(parentDomain string) bool {
	if !strings.HasPrefix(parentDomain, "cdn:") {
		return false
	}
	rest := strings.TrimPrefix(parentDomain, "cdn:")
	idx := strings.Index(rest, ":")
	if idx <= 0 {
		// No colon after the CDN name → malformed.
		return false
	}
	if idx == len(rest)-1 {
		// Ends with ":" (empty original domain) → malformed.
		return false
	}
	return true
}

// cdnFromMarker extracts the CDN name from a CDN marker
// parent_domain. Returns "" if not a marker.
func cdnFromMarker(parentDomain string) string {
	if !isCDNMarker(parentDomain) {
		return ""
	}
	// "cdn:cloudflare:artstation.com" → "cloudflare"
	rest := strings.TrimPrefix(parentDomain, "cdn:")
	idx := strings.Index(rest, ":")
	if idx < 0 {
		return rest
	}
	return rest[:idx]
}

// cdnParentMarkerGuess returns a SQL LIKE pattern for
// looking up an existing CDN rule for a specific domain.
// The "%" in the middle matches any CDN name
// (cloudflare/fastly/google/akamai), so the autoupdate
// doesn't have to know the CDN in advance.
//
// Format: "cdn:%:<domain>". The caller MUST use this as a
// SQL LIKE pattern, not as an equality value.
//
// Why "guess" not "marker": the autoupdate's short-circuit
// check runs BEFORE DNS resolution and CDN detection —
// we don't know the CDN name yet. The "%" lets the lookup
// match any CDN. If a CDN rule for this domain exists
// (regardless of which CDN), the lookup returns it and the
// autoupdate skips DNS entirely.
func cdnParentMarkerGuess(domain string) string {
	return "cdn:%:" + domain
}
