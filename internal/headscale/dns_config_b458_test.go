package headscale

// dns_config_b458_test.go — B458: the tailnet's resolver set must be READ, not assumed.
//
// The live defect this pins: headscale hands out `[1.1.1.1, 8.8.8.8]` while the tailnet
// routes only the second one, because a user rule happens to claim 8.8.8.8. Whoever deletes
// that rule takes DNS away from the whole tailnet — so the resolver set has to be measured
// from the same config headscale serves, and an unreadable config must say so instead of
// returning an empty list that looks healthy.

import (
	"os"
	"testing"
)

// TestB458_ParseDNSNameserversBlockList reads the exact block shape headscale's template
// writes, including a `split:` section that must not leak into `global`.
func TestB458_ParseDNSNameserversBlockList(t *testing.T) {
	cfg := `server_url: https://head.example.com
dns:
  magic_dns: true
  base_domain: tsnet.example.com
  nameservers:
    global:
    - 1.1.1.1
    - 8.8.8.8
    split:
      corp.example.com:
      - 10.0.0.53
derp:
  urls:
  - https://controlplane.tailscale.com/derpmap/default
`
	global, split, err := ParseDNSNameservers(cfg)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(global) != 2 || global[0] != "1.1.1.1" || global[1] != "8.8.8.8" {
		t.Fatalf("global = %v, want [1.1.1.1 8.8.8.8]", global)
	}
	if got := split["corp.example.com"]; len(got) != 1 || got[0] != "10.0.0.53" {
		t.Fatalf("split[corp.example.com] = %v, want [10.0.0.53]", got)
	}
}

// TestB458_ParseDNSNameserversInlineForm accepts the flow-list spelling too: an operator
// who edits the file by hand should not lose the measurement over formatting.
func TestB458_ParseDNSNameserversInlineForm(t *testing.T) {
	cfg := "dns:\n  nameservers:\n    global: [1.1.1.1, 9.9.9.9]\n"
	global, _, err := ParseDNSNameservers(cfg)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(global) != 2 || global[0] != "1.1.1.1" || global[1] != "9.9.9.9" {
		t.Fatalf("global = %v, want the inline list", global)
	}
}

// TestB458_ParseDNSNameserversIgnoresOtherSections is the "do not cry wolf" half: a
// `nameservers:` under a different top-level key must not be read as the tailnet's DNS.
func TestB458_ParseDNSNameserversIgnoresOtherSections(t *testing.T) {
	cfg := "derp:\n  nameservers:\n    global:\n    - 203.0.113.9\n"
	if _, _, err := ParseDNSNameservers(cfg); err == nil {
		t.Fatal("a nameservers key outside the dns: block was accepted as the tailnet resolver set")
	}
}

// TestB458_ResolverPrefixesArePrecise — the tailnet carries the address clients query, not
// the resolver's whole /24, and an unresolvable entry is reported rather than dropped.
func TestB458_ResolverPrefixesArePrecise(t *testing.T) {
	prefixes, skipped := ResolverPrefixes(DNSResolvers{Global: []string{"8.8.8.8", "2001:4860:4860::8888", "resolver.example.com"}})
	if len(prefixes) != 1 || prefixes[0] != "8.8.8.8/32" {
		t.Fatalf("prefixes = %v, want [8.8.8.8/32] (a /32, not the /24)", prefixes)
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped = %v, want the IPv6 and the hostname entry named", skipped)
	}
}

// TestB458_EnvOverrideAndUnreadableConfig — the knobs:
//   - SKYGATE_TAILNET_DNS_RESOLVERS wins (an operator whose config lives elsewhere);
//   - a non-IP entry is reported in Note instead of being silently dropped;
//   - with nothing readable the set is UNKNOWN with a reason, never "no resolvers".
func TestB458_EnvOverrideAndUnreadableConfig(t *testing.T) {
	t.Setenv("SKYGATE_TAILNET_DNS_RESOLVERS", "9.9.9.9, 1.0.0.1, not-an-ip")
	r := TailnetDNSResolvers()
	if !r.Known() || len(r.Global) != 2 || r.Global[0] != "1.0.0.1" {
		t.Fatalf("env override: %+v, want the two IPs sorted", r)
	}
	if r.Note == "" {
		t.Error("a rejected entry must be named in Note, not swallowed")
	}

	// No env, and point the candidates at a path that does not exist: the answer must be
	// UNKNOWN with a reason, because an empty resolver set that looks healthy is the
	// defect this block removes.
	t.Setenv("SKYGATE_TAILNET_DNS_RESOLVERS", "")
	t.Setenv("SKYGATE_HEADSCALE_CONFIG_PATH", "/nonexistent/config.yaml")
	t.Setenv("SKYGATE_HEADSCALE_CONFIG", "/nonexistent/config.yaml")
	prev := os.Getenv("HOME")
	_ = prev
	r = TailnetDNSResolvers()
	if r.Known() {
		t.Fatalf("expected an unknown resolver set, got %+v", r)
	}
	if r.Note == "" {
		t.Error("an unknown resolver set must carry the reason the operator can act on")
	}
}
