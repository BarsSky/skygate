// B465 (2026-11-12) — the ssh target must be `[user@]host[:port]` and never `[host:port]:port`.
//
// THE MEASUREMENT (operator's installation, that day): the relay warning quoted
//
//	ssh root@[100.64.0.2:18022]:18022: exit status 255
//	ssh: Could not resolve hostname [100.64.0.2:18022]: Name does not resolve
//
// The host word was bracketed because it contained a colon and the port was then appended
// again. B449 fixed `sshHost()` (what ssh receives), but `SSHTarget()` — used by the error
// text, by `SSHFailureAdvice` and by `UpsertExitServer`, i.e. written back into
// `exit_servers.ssh_target` — kept the old rule. A value that is stored malformed stays
// malformed forever, so it is pinned here.
package exitnode

import (
	"regexp"
	"strings"
	"testing"
)

// bugShape is the exact malformation B465 removes: an IPv4 host with its port INSIDE the
// brackets, which ssh then tries to resolve as a hostname. Narrow on purpose — an IPv6
// literal legitimately has colons inside its brackets.
var bugShape = regexp.MustCompile(`\[[0-9]{1,3}(\.[0-9]{1,3}){3}:[0-9]+\]`)

func TestB465_SSHTargetNeverDoublesThePort(t *testing.T) {
	cases := []struct {
		name string
		r    Request
		want string
		why  string
	}{
		{
			name: "the live shape: the port is in BOTH the host and the field",
			r:    Request{User: "root", Host: "100.64.0.2:18022", Port: 18022},
			want: "root@100.64.0.2:18022",
			why:  "the pre-B465 code produced root@[100.64.0.2:18022]:18022, an unresolvable name",
		},
		{
			name: "the port only inside the host",
			r:    Request{User: "root", Host: "100.64.0.2:18022"},
			want: "root@100.64.0.2:18022",
			why:  "the port must survive the split, not be dropped",
		},
		{
			name: "plain host and port (the B412 shape, unchanged)",
			r:    Request{User: "root", Host: "82.26.94.56", Port: 2200},
			want: "root@82.26.94.56:2200",
		},
		{
			name: "default port is not printed",
			r:    Request{User: "root", Host: "82.26.94.56", Port: 22},
			want: "root@82.26.94.56",
		},
		{
			name: "a bare IPv6 literal IS bracketed, once, with the port outside",
			r:    Request{User: "root", Host: "fd7a:115c:a1e0::2", Port: 18022},
			want: "root@[fd7a:115c:a1e0::2]:18022",
		},
		{
			name: "an already-bracketed IPv6 with its port stays intact",
			r:    Request{User: "root", Host: "[fd7a:115c:a1e0::2]:18022", Port: 18022},
			want: "root@[fd7a:115c:a1e0::2]:18022",
		},
	}
	for _, c := range cases {
		got := c.r.SSHTarget()
		if got != c.want {
			t.Errorf("%s: SSHTarget() = %q, want %q (%s)", c.name, got, c.want, c.why)
		}
		// The malformation this block removes must never reappear in ANY shape: a
		// dotted-quad (IPv4) with a port inside brackets — `[100.64.0.2:18022]`. An IPv6
		// literal legitimately has colons inside its brackets, so the detector is
		// deliberately narrow.
		if bugShape.MatchString(got) {
			t.Errorf("%s: SSHTarget() = %q — an IPv4 host with a port was bracketed", c.name, got)
		}
	}
}

// TestB465_ErrorTextDoesNotQuoteAMalformedCommand — the message and the advice are built
// from the same value, so the operator reads a command they can actually run.
func TestB465_ErrorTextDoesNotQuoteAMalformedCommand(t *testing.T) {
	r := Request{User: "root", Host: "100.64.0.2:18022", Port: 18022}
	advice := SSHFailureAdvice(SSHDiagnosis{Kind: SSHKindNetwork}, r.SSHTarget(), "")
	if strings.Contains(advice, "]:") {
		t.Fatalf("the advice quotes a bracketed host:port: %q", advice)
	}
	if !strings.Contains(advice, "100.64.0.2") {
		t.Fatalf("the advice does not name the host at all: %q", advice)
	}
}
