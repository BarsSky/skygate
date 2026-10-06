// relay_transport_ipv6_b353_1_test.go — B353.1 (2026-10-06).
//
// Found by the live verification of B353, one line above the rung that worked:
//
//	exit-node sync(emilia): tailnet fd7a:115c:a1e0::3 answered but the routes could
//	  not be applied: SetAdvertisedRoutes(emilia): refusing unsafe ssh_target
//	  "root@fd7a:115c:a1e0::3" (expected [user@]host[:port]) — trying the next transport
//
// The ladder built that candidate itself (`RelayEndpoint.Target`), so the IPv6 rung
// was unusable by construction: `ssh` reads `root@fd7a:115c:a1e0::3` as user
// `root@fd7a` plus a malformed host, and the shape gate — correctly — refuses the
// unbracketed form. An IPv6-only relay therefore had one fewer path, and the failure
// text blamed the value the transport had just produced.
package exit_rules

import (
	"net"
	"strings"
	"testing"
)

func TestB353_1_IPv6TargetIsBracketed(t *testing.T) {
	cases := []struct {
		ep   RelayEndpoint
		want string
	}{
		// The live case: the tailnet IPv6 address of every relay in this tailnet.
		{RelayEndpoint{Kind: RelayEndpointTailnet, Host: "fd7a:115c:a1e0::3"}, "root@[fd7a:115c:a1e0::3]"},
		{RelayEndpoint{Kind: RelayEndpointTailnet, Host: "fd7a:115c:a1e0::3", Port: "22"}, "root@[fd7a:115c:a1e0::3]"},
		{RelayEndpoint{Kind: RelayEndpointTailnet, Host: "fd7a:115c:a1e0::3", Port: "18022"}, "root@[fd7a:115c:a1e0::3]:18022"},
		// Already bracketed: don't double-wrap.
		{RelayEndpoint{Kind: RelayEndpointTailnet, Host: "[fd7a:115c:a1e0::3]", Port: "22"}, "root@[fd7a:115c:a1e0::3]"},
		// IPv4 and names are untouched (the shape every existing test pins).
		{RelayEndpoint{Kind: RelayEndpointTailnet, Host: "100.64.0.2", Port: "18022"}, "root@100.64.0.2:18022"},
		{RelayEndpoint{Kind: RelayEndpointPublic, User: "deploy", Host: "relay.example.com"}, "deploy@relay.example.com"},
		// A full-form IPv6 with a port.
		{RelayEndpoint{Kind: RelayEndpointTailnet, Host: "2001:db8::1", Port: "2222"}, "root@[2001:db8::1]:2222"},
	}
	for _, c := range cases {
		got := c.ep.Target()
		if got != c.want {
			t.Errorf("Target(%+v) = %q; want %q", c.ep, got, c.want)
		}
	}
}

// TestB353_1_EveryTargetTheLadderBuildsIsAcceptable is the property that failed
// live: the transport must never hand its OWN candidate to a gate that refuses it.
// The shape check lives in internal/headscale, so this asserts the shape it
// accepts — a user@ host without a port, or a bracketed IPv6 with one.
func TestB353_1_EveryTargetTheLadderBuildsIsAcceptable(t *testing.T) {
	for _, host := range []string{"100.64.0.2", "fd7a:115c:a1e0::3", "2001:db8::1", "relay.example.com", "relay-4"} {
		for _, port := range []string{"", "22", "18022"} {
			ep := RelayEndpoint{Kind: RelayEndpointTailnet, User: "root", Host: host, Port: port}
			target := ep.Target()
			// The equivalent of headscale.IsSafeSSHTarget without importing the
			// package (the exit_rules package must not depend on it): an IPv6
			// literal MUST appear inside brackets.
			if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
				if !containsBracketedLiteral(target, host) {
					t.Errorf("Target(%s, port=%q) = %q — an unbracketed IPv6 literal is refused by the shape gate", host, port, target)
				}
			}
			if target == "" {
				t.Errorf("empty target for host=%q port=%q", host, port)
			}
		}
	}
}

func containsBracketedLiteral(target, host string) bool {
	return strings.Contains(target, "["+host+"]")
}

// TestB353_1_IPv6LabelIsUnambiguous: `fd7a:115c:a1e0::3:22` cannot be read by a
// human — or by the next parser — as a host with a port.
func TestB353_1_IPv6LabelIsUnambiguous(t *testing.T) {
	ep := RelayEndpoint{Kind: RelayEndpointTailnet, Host: "fd7a:115c:a1e0::3", Port: "22"}
	if got, want := ep.Label(), "tailnet [fd7a:115c:a1e0::3]:22"; got != want {
		t.Fatalf("Label() = %q; want %q", got, want)
	}
	// The IPv4 label is unchanged (existing contracts pin it).
	v4 := RelayEndpoint{Kind: RelayEndpointTailnet, Host: "100.64.0.2", Port: "18022"}
	if got, want := v4.Label(), "tailnet 100.64.0.2:18022"; got != want {
		t.Fatalf("IPv4 label changed: %q; want %q", got, want)
	}
}
