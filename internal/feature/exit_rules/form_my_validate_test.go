// v1.3.13 — youtube.com/32 bug fix: validate targetValue is
// IP/CIDR for targetType "ip" or "subnet". Pre-fix, an operator
// who typed a bare hostname (e.g. "youtube.com") in the IP field
// would get "youtube.com/32" saved to the DB, which the ACL
// builder then promoted to a host alias "h-rule-youtube-com-32:
// youtube.com/32" — a malformed CIDR that headscale rejects.
//
// This test pins the isValidIPOrCIDR helper that the form uses
// to reject the bad input at the boundary. The handler-level
// test (PostMyExitRule with bad input → 400) is covered by
// integration tests in store_test.go.
package exit_rules

import "testing"

func TestIsValidIPOrCIDR_IPv4(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		// Valid IPv4 (bare)
		{"1.2.3.4", true},
		{"10.0.1.5", true},
		{"192.168.13.69", true},
		{"0.0.0.0", true},
		{"255.255.255.255", true},
		// Valid IPv4 CIDRs
		{"1.2.3.4/32", true},
		{"1.2.3.0/24", true},
		{"0.0.0.0/0", true},
		{"100.64.0.0/10", true},
		// Invalid: bare hostnames (the original bug)
		{"youtube.com", false},
		{"google.com", false},
		{"foo.bar.baz", false},
		// Invalid: CIDR with hostname
		{"youtube.com/32", false},
		{"google.com/24", false},
		// Invalid: garbage
		{"", false},
		{"not an ip", false},
		{"999.999.999.999", false},
		{"1.2.3.4.5", false},
		// Valid IPv6 (smoke test)
		{"::1", true},
		{"fe80::1", true},
		{"::1/128", true},
		{"2001:db8::/32", true},
	}
	for _, c := range cases {
		got := isValidIPOrCIDR(c.in)
		if got != c.want {
			t.Errorf("isValidIPOrCIDR(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
