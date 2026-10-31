// dns_config_b458.go — B458 (2026-10-31). «DNS внутри tailnet должен ехать по
// проекту, а не случайно».
//
// THE MEASURED DEFECT (reference install, 2026-10-31): headscale's config hands every
// client `dns.nameservers.global: [1.1.1.1, 8.8.8.8]`, but the tailnet only ROUTES the
// second one — and it routes it by accident: `prefix_owner` carries `8.8.8.0/24` and
// `8.8.4.0/24` because a USER RULE happens to claim `8.8.8.8`, while `1.1.1.1` has no
// prefix at all, so half of every client's DNS leaves the tailnet (and on a filtered
// network it does not leave at all). Delete that one rule and the whole tailnet silently
// falls back to the ISP resolver: a DNS setting that is carried by a data-plane
// coincidence is not a setting, it is a trap.
//
// This file is the MEASUREMENT half: read the resolvers headscale actually hands out, so
// skygate can carry (and report) exactly those prefixes instead of guessing. It is
// deliberately dependency-free, like DiscoverPolicyPath: the values are single scalars or
// a flat list, and a full YAML parser would be a new dependency for four lines.
package headscale

import (
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strings"
)

// DNSResolvers is the resolver set the tailnet hands to its clients.
type DNSResolvers struct {
	// Global is the IPv4 resolver list, de-duplicated and sorted.
	Global []string
	// Source names where the answer came from: "env" (SKYGATE_TAILNET_DNS_RESOLVERS),
	// a config path, or "" when nothing could be read.
	Source string
	// Note explains an empty or partial answer (a missing config, a non-IP entry, an
	// IPv6-only resolver). It is shown to the operator instead of being swallowed: an
	// unknown resolver set must never look like "DNS is fine".
	Note string
}

// Known reports whether the resolver set could be read at all.
func (r DNSResolvers) Known() bool { return len(r.Global) > 0 }

// TailnetDNSResolvers reads the resolver set the tailnet actually hands out.
//
// Order: SKYGATE_TAILNET_DNS_RESOLVERS (explicit operator override, comma separated) →
// the headscale config file (the same discovery list DiscoverPolicyPath uses, because a
// container and a native install keep it in different places) → unknown.
func TailnetDNSResolvers() DNSResolvers {
	if raw := strings.TrimSpace(os.Getenv("SKYGATE_TAILNET_DNS_RESOLVERS")); raw != "" {
		out := DNSResolvers{Source: "SKYGATE_TAILNET_DNS_RESOLVERS"}
		var bad []string
		for _, item := range strings.Split(raw, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if _, err := netip.ParseAddr(item); err != nil {
				bad = append(bad, item)
				continue
			}
			out.Global = append(out.Global, item)
		}
		out.Global = dedupeSorted(out.Global)
		if len(bad) > 0 {
			out.Note = "SKYGATE_TAILNET_DNS_RESOLVERS contains entries that are not IP addresses: " + strings.Join(bad, ", ")
		}
		return out
	}
	for _, p := range tailnetDNSConfigCandidates() {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		global, split, err := ParseDNSNameservers(string(b))
		if err != nil {
			return DNSResolvers{Source: p, Note: err.Error()}
		}
		out := DNSResolvers{Source: p}
		var nonIP []string
		for _, item := range global {
			if _, perr := netip.ParseAddr(item); perr != nil {
				nonIP = append(nonIP, item)
				continue
			}
			out.Global = append(out.Global, item)
		}
		out.Global = dedupeSorted(out.Global)
		if len(nonIP) > 0 {
			out.Note = "dns.nameservers.global names entries that are not IP addresses (a hostname resolver cannot be carried as a prefix): " + strings.Join(nonIP, ", ")
		}
		_ = split
		return out
	}
	return DNSResolvers{Note: "no headscale config.yaml was readable — set SKYGATE_TAILNET_DNS_RESOLVERS to name the resolver set"}
}

// tailnetDNSConfigCandidates is DiscoverPolicyPath's discovery list, so a container and a
// native install are both covered (a native host keeps the file under /etc/headscale or
// /var/lib/headscale; the container mounts the operator's directory and the env var names
// it).
func tailnetDNSConfigCandidates() []string {
	return []string{
		os.Getenv("SKYGATE_HEADSCALE_CONFIG_PATH"),
		os.Getenv("SKYGATE_HEADSCALE_CONFIG"),
		"/etc/headscale/config.yaml",
		"/etc/headscale/config.yml",
		"/var/lib/headscale/config.yaml",
		"/opt/headscale/config.yaml",
	}
}

// ParseDNSNameservers extracts (dns.nameservers.global, dns.nameservers.split) from a
// headscale config.yaml. Only the `dns:` block is considered, so a `nameservers:` under
// another section can never be mistaken for DNS.
//
// Both YAML spellings are accepted — the block list headscale's own template writes:
//
//	dns:
//	  nameservers:
//	    global:
//	    - 1.1.1.1
//	    - 8.8.8.8
//
// and the inline form `global: [1.1.1.1, 8.8.8.8]`.
func ParseDNSNameservers(cfg string) (global []string, split map[string][]string, err error) {
	split = map[string][]string{}
	inDNS := false
	dnsIndent := -1
	// section is "global" or "split" while inside `nameservers:`; lastDomain names the
	// split entry the following `- item` lines belong to.
	section := ""
	lastDomain := ""
	for _, raw := range strings.Split(cfg, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		if !inDNS {
			if strings.HasPrefix(trimmed, "dns:") {
				inDNS = true
				dnsIndent = indent
			}
			continue
		}
		// A key at or above the dns: indent ends the block.
		if indent <= dnsIndent {
			break
		}
		switch {
		case strings.HasPrefix(trimmed, "nameservers:"):
			section = ""
			lastDomain = ""
		case strings.HasPrefix(trimmed, "global:"):
			section = "global"
			lastDomain = ""
			global = append(global, inlineList(strings.TrimPrefix(trimmed, "global:"))...)
		case strings.HasPrefix(trimmed, "split:"):
			section = "split"
			lastDomain = ""
			// staticcheck S1011: append a slice, not a loop over it.
			split["<inline>"] = append(split["<inline>"], inlineList(strings.TrimPrefix(trimmed, "split:"))...)
		case section == "split" && strings.HasPrefix(trimmed, "- "):
			// A split item belongs to the nearest preceding domain key.
			if item := yamlScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))); item != "" && lastDomain != "" {
				split[lastDomain] = append(split[lastDomain], item)
			}
		case section == "split" && strings.HasSuffix(trimmed, ":"):
			lastDomain = yamlScalar(strings.TrimSuffix(trimmed, ":"))
			if lastDomain != "" {
				if _, ok := split[lastDomain]; !ok {
					split[lastDomain] = nil
				}
			}
		case section == "global" && strings.HasPrefix(trimmed, "- "):
			if item := yamlScalar(strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))); item != "" {
				global = append(global, item)
			}
		}
	}
	if len(global) == 0 && len(split) == 0 {
		return nil, split, fmt.Errorf("dns.nameservers.global is empty or absent")
	}
	return global, split, nil
}

// inlineList parses a YAML flow list (`[a, b]`) or a bare scalar (`a`).
func inlineList(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	v = strings.TrimPrefix(v, "[")
	v = strings.TrimSuffix(v, "]")
	var out []string
	for _, item := range strings.Split(v, ",") {
		if s := yamlScalar(strings.TrimSpace(item)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func dedupeSorted(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// ResolverPrefixes turns the resolver set into the /32 prefixes the tailnet must carry.
//
// A /32, not the resolver's /24: the tailnet needs to reach the address clients QUERY, and
// announcing a whole /24 moves a neighbourhood of somebody else's addresses through the
// exit node for no reason. IPv6 resolvers are skipped with a note — the derived route rows
// are IPv4 today (B409), and silently announcing nothing would look like "DNS is fine".
func ResolverPrefixes(r DNSResolvers) (prefixes []string, skipped []string) {
	for _, s := range r.Global {
		addr, err := netip.ParseAddr(s)
		if err != nil {
			skipped = append(skipped, s)
			continue
		}
		if !addr.Is4() {
			skipped = append(skipped, s)
			continue
		}
		prefixes = append(prefixes, addr.String()+"/32")
	}
	sort.Strings(prefixes)
	return prefixes, skipped
}
