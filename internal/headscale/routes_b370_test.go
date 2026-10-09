// B370 — the jump ProxyCommand must bracket an IPv6 target EXACTLY once.
//
// MEASURED LIVE, 2026-10-09 (reference deployment, PostgreSQL): the periodic sync
// was configuring the relays while the portal had just been recreated, and the
// jump rung for karolina — whose Tailscale addresses are IPv6-first — died with
//
//	exit-node sync(karolina): tailnet-jump [fd7a:115c:a1e0::2]:18022 via
//	root@100.64.0.3 … Bad stdio forwarding specification '[[fd7a:115c:a1e0::2]]:18022'
//
// The hop answered, the identity was right, and the rung was still dead: ssh
// expands `%h` to the target AS TYPED, an IPv6 target must be typed bracketed,
// and `-W '[%h]:%p'` therefore bracketed it a second time. The spec is now built
// from the same host/port the outer ssh receives (ForwardSpec), so an IPv6-only
// relay keeps that rung of the ladder.
package headscale

import (
	"strings"
	"testing"
)

func TestB370_ForwardSpecBracketsIPv6ExactlyOnce(t *testing.T) {
	cases := []struct {
		name string
		host string
		port string
		want string
	}{
		{"an IPv6 literal as the caller holds it (already bracketed)", "root@[fd7a:115c:a1e0::2]", "18022", "[fd7a:115c:a1e0::2]:18022"},
		{"a bare IPv6 literal", "fd7a:115c:a1e0::2", "22", "[fd7a:115c:a1e0::2]:22"},
		{"IPv4 is never bracketed", "root@100.64.0.3", "22", "100.64.0.3:22"},
		{"a hostname is never bracketed", "karolina", "", "karolina:22"},
		{"an explicit port survives", "root@karolina", "18022", "karolina:18022"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ForwardSpec(tc.host, tc.port); got != tc.want {
				t.Errorf("ForwardSpec(%q, %q) = %q, want %q", tc.host, tc.port, got, tc.want)
			}
			// The double bracket IS the defect: no spec may ever contain "[[".
			if strings.Contains(ForwardSpec(tc.host, tc.port), "[[") {
				t.Errorf("ForwardSpec(%q, %q) produced a double bracket", tc.host, tc.port)
			}
		})
	}
}

// TestB370_JumpArgvCarriesASingleBracketedSpec is the regression at the argv
// level: this is the exact shape that failed live.
func TestB370_JumpArgvCarriesASingleBracketedSpec(t *testing.T) {
	args := buildSetAdvertisedRoutesArgv("/k", "root@[fd7a:115c:a1e0::2]", "18022",
		"tailscale set --advertise-routes=0.0.0.0/0", "root@100.64.0.3")
	joined := strings.Join(args, " ")

	if strings.Contains(joined, "[[") {
		t.Fatalf("the argv carries a double-bracketed forwarding specification (the live failure): %v", args)
	}
	if !strings.Contains(joined, "-W '[fd7a:115c:a1e0::2]:18022'") {
		t.Fatalf("the forwarded spec must name the IPv6 target once, bracketed: %v", args)
	}
	// The outer connection still has to be hardened (B266) and to target the
	// same address the tunnel does.
	for _, want := range []string{"-- root@[fd7a:115c:a1e0::2]", "-p 18022", "-i /k", "-- root@100.64.0.3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the argv lost %q: %v", want, args)
		}
	}
}

// TestB370_TheForwardSpecFollowsTheOuterTarget keeps the property the old
// `%h`/`%p` form had by construction: the tunnel points at the relay THIS
// application is for, not at some other one.
func TestB370_TheForwardSpecFollowsTheOuterTarget(t *testing.T) {
	for _, tc := range []struct{ host, port string }{
		{"root@100.64.0.2", "18022"},
		{"root@[fd7a:115c:a1e0::2]", "18022"},
		{"root@karolina", ""},
	} {
		args := buildSetAdvertisedRoutesArgv("/k", tc.host, tc.port, "true", "root@100.64.0.3")
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-W '"+ForwardSpec(tc.host, tc.port)+"'") {
			t.Errorf("the forwarded spec does not match the outer target %q:%q: %v", tc.host, tc.port, args)
		}
	}
}
