// target_b449_test.go — B449 (2026-10-30). The two live rows are the test cases.
package exitnode

import "testing"

func TestParseSSHTarget_B449(t *testing.T) {
	cases := []struct {
		raw            string
		wantUser, host string
		wantPort       int
		wantOK         bool
		why            string
	}{
		// The live rows, verbatim from exit_servers.
		{"root@100.64.0.3", "root", "100.64.0.3", 0, true, "emilia: port comes from the ssh_port column / default 22"},
		{"root@100.64.0.2:18022", "root", "100.64.0.2", 18022, true, "karolina: the port was INSIDE the target and became part of the hostname"},
		{"", "", "", 0, false, "aisha: an empty field is an operator omission, not a typo"},
		{"100.64.0.7", "", "100.64.0.7", 0, true, "no user → the caller defaults to root"},
		{"100.64.0.7:2200", "", "100.64.0.7", 2200, true, "bare host:port"},
		{"[fd7a:115c:a1e0::3]:22", "", "fd7a:115c:a1e0::3", 22, true, "bracketed IPv6 with a port"},
		{"deploy@[fd7a:115c:a1e0::3]", "deploy", "fd7a:115c:a1e0::3", 0, true, "bracketed IPv6, user, no port"},
		{"fd7a:115c:a1e0::3", "", "fd7a:115c:a1e0::3", 0, true, "bare IPv6: more than one colon is never host:port"},
		{"deploy@host.example.com:notaport", "", "", 0, false, "a non-numeric port is a typo and must say so"},
		{"-oProxyCommand=touch /tmp/x", "", "", 0, false, "the B266 argv-injection shape stays refused"},
		{"root@", "", "", 0, false, "nothing after the @ is not a host"},
		{"[fd7a::3", "", "", 0, false, "an unterminated bracket is a typo"},
	}
	for _, c := range cases {
		spec, reason, ok := ParseSSHTarget(c.raw)
		if ok != c.wantOK {
			t.Errorf("ParseSSHTarget(%q) ok=%v, want %v (%s) — reason=%q", c.raw, ok, c.wantOK, c.why, reason)
			continue
		}
		if !ok {
			if reason == "" {
				t.Errorf("ParseSSHTarget(%q) refused without a reason — the operator cannot act on that", c.raw)
			}
			continue
		}
		if spec.User != c.wantUser || spec.Host != c.host || spec.Port != c.wantPort {
			t.Errorf("ParseSSHTarget(%q) = {%q %q %d}, want {%q %q %d} (%s)",
				c.raw, spec.User, spec.Host, spec.Port, c.wantUser, c.host, c.wantPort, c.why)
		}
	}
}

func TestSSHHostWord_BracketsOnlyIPv6_B449(t *testing.T) {
	cases := []struct{ user, host, want, why string }{
		{"root", "100.64.0.2", "root@100.64.0.2", "IPv4 must never be bracketed"},
		{"root", "relay.example.com", "root@relay.example.com", "a DNS name must never be bracketed"},
		{"root", "fd7a:115c:a1e0::3", "root@[fd7a:115c:a1e0::3]", "a bare IPv6 literal must be"},
		{"", "100.64.0.3", "100.64.0.3", "an empty user is the caller's problem, not a malformed word"},
	}
	for _, c := range cases {
		if got := SSHHostWord(c.user, c.host); got != c.want {
			t.Errorf("SSHHostWord(%q, %q) = %q, want %q (%s)", c.user, c.host, got, c.want, c.why)
		}
	}
	// The exact live failure, spelled out: the pre-B449 rule bracketed anything with a
	// colon, so this pair produced the unresolvable `root@[100.64.0.2:18022]`.
	if got := SSHHostWord("root", "100.64.0.2"); got != "root@100.64.0.2" {
		t.Fatalf("regression: SSHHostWord produced %q", got)
	}
}
