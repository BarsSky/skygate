// B366 — two live follow-ups to the panel-only cluster onboarding, both found by
// running the v1.5.107 flow end to end on a real second host (2026-10-09).
//
//	A. step 1 passed `--accept-routes`, which on that host CUT THE OPERATOR OFF:
//	   it installs every relay-advertised prefix into routing table 52, the kernel
//	   consults that table before `main`, and one of the 220 entries was the
//	   operator's own address — so the host answered the operator over
//	   tailscale0 and the SSH session died (tcpdump: SYNs in, no SYN-ACK out).
//	   Reproduced twice in each direction; `--accept-routes=false` restored it.
//	   A standby is a server: it needs the tailnet, not the relays' subnets.
//
//	B. the join registered `skygate_version="unknown"`, because the CLI read
//	   SKYGATE_VERSION (which nothing sets) with a literal "unknown" fallback
//	   instead of its own ldflags build identity. The test for B lives in
//	   cmd/skygate/self_version_b366_test.go; the source contract is in
//	   scripts/check_b366_onboard_keeps_the_operator_connected.sh.
package admin

import (
	"strings"
	"testing"
)

// TestB366_TailnetStepDoesNotAcceptRoutes pins A on the RENDERED command, in
// both branches (with and without a preauth key), and pins that the fix did not
// quietly drop the flags the step genuinely needs.
func TestB366_TailnetStepDoesNotAcceptRoutes(t *testing.T) {
	cases := []struct {
		name  string
		tsKey string
	}{
		{"without a preauth key", ""},
		{"with a preauth key", "hskey-auth-EXAMPLE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			steps := clusterOnboardSteps(clusterOnboardPayload{
				Hostname:   "svyatoslava",
				InviteTok:  "sgn1.A.B",
				APIURL:     "https://skygate.example.com",
				ControlURL: "https://head.example.com",
				Version:    "v1.5.108",
				TSKey:      tc.tsKey,
			})
			if len(steps) == 0 {
				t.Fatal("no steps rendered")
			}
			cmd := steps[0].Command

			// A1 — the defect: accepting the relays' routes must NOT be in the block.
			if strings.Contains(cmd, "--accept-routes") {
				t.Errorf("step 1 passes --accept-routes, which installs every relay prefix into table 52 "+
					"and can capture the operator's own management path (measured: the host answered SSH "+
					"over tailscale0 and the session died); command:\n%s", cmd)
			}
			// A2 — what the step must keep, so "remove the flag" cannot be
			// mistaken for "remove the networking".
			for _, want := range []string{
				"hostnamectl set-hostname svyatoslava",
				"tailscale up",
				"--login-server=https://head.example.com",
				"--hostname=svyatoslava",
				"--netfilter-mode=nodivert",
				"--accept-dns=false",
			} {
				if !strings.Contains(cmd, want) {
					t.Errorf("step 1 lost %q; command:\n%s", want, cmd)
				}
			}
			// A3 — the client install stays idempotent (B342.2).
			if !strings.Contains(cmd, "command -v tailscale >/dev/null ||") {
				t.Errorf("step 1 no longer installs the client idempotently; command:\n%s", cmd)
			}
			// A4 — the key branch is still selected by the payload.
			if tc.tsKey != "" {
				if !strings.Contains(cmd, "--authkey="+tc.tsKey) {
					t.Errorf("step 1 dropped the preauth key; command:\n%s", cmd)
				}
			} else if strings.Contains(cmd, "--authkey=") {
				t.Errorf("step 1 added an --authkey= that was not minted; command:\n%s", cmd)
			}
		})
	}
}
