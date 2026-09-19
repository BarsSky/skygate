// File: internal/feature/admin/infra_colocation_b265_test.go
//
// B265 / B265.1 (2026-09-19) — pin the "skygate runs ON an exit node"
// detector, including the false-positive that the first cut produced on
// the reference VM.
//
// B265.1 regression: the first version returned true whenever EITHER
// side of the comparison was the reserved name `skygate-host`
// (`if nm == "skygate-host" || id.Value == "skygate-host"`). Because
// the identity list always contains the skygate host's own name, EVERY
// exit node matched and the boot log listed all three relays as
// co-located:
//
//	exit-colocation: WARNING skygate runs on the same host as 3 exit node(s):
//	  exit node emilia (node_id=3) is this host (matched skygate-host via
//	  SKYGATE_TS_HOSTNAME); … sharlotta …; … karolina …
//
// Exact equality already covers the intended case, so the special-case
// clause is gone and these tests keep it gone.

package admin

import (
	"testing"

	"skygate/internal/headscale"
)

func exitNode(id, hostname string, ips []string) headscale.NodeView {
	return headscale.NodeView{
		ID:          id,
		Hostname:    hostname,
		GivenName:   hostname,
		IPAddresses: ips,
		Tags:        []string{"tag:exit-node"},
		IsExitNode:  true,
	}
}

// TestB2651_NoFalsePositiveForRemoteRelays is the exact live failure:
// three relays on other hosts must NOT be reported as co-located just
// because the skygate host's own name is in the identity list.
func TestB2651_NoFalsePositiveForRemoteRelays(t *testing.T) {
	ids := []ColocationIdentity{
		{Value: "skygate-host", Source: "SKYGATE_TS_HOSTNAME"},
		{Value: "192.168.13.69", Source: "SKYGATE_DERP_PROBE_HOST"},
	}
	relays := []headscale.NodeView{
		exitNode("3", "emilia", []string{"100.64.0.3", "fd7a:115c:a1e0::3"}),
		exitNode("4", "sharlotta", []string{"100.64.0.4", "fd7a:115c:a1e0::4"}),
		exitNode("11", "karolina", []string{"100.64.0.2", "fd7a:115c:a1e0::2"}),
	}
	for _, n := range relays {
		if id, ok := isColocatedExitNode(n, ids); ok {
			t.Errorf("isColocatedExitNode(%s) = true (matched %q via %s); a relay on another host must not be reported as co-located", n.Hostname, id.Value, id.Source)
		}
	}
}

// TestB265_MatchesByHostname covers the real positive case: the node
// whose hostname IS the skygate host's name.
func TestB265_MatchesByHostname(t *testing.T) {
	ids := []ColocationIdentity{{Value: "skygate-host", Source: "SKYGATE_TS_HOSTNAME"}}
	n := exitNode("57", "skygate-host", []string{"100.64.0.9"})
	id, ok := isColocatedExitNode(n, ids)
	if !ok {
		t.Fatalf("isColocatedExitNode(skygate-host) = false; want a match")
	}
	if id.Value != "skygate-host" {
		t.Errorf("matched identity = %q, want skygate-host", id.Value)
	}
}

// TestB265_MatchesByTailnetIP covers the native-install shape where the
// relay's own tailnet IP is the only reliable identity.
func TestB265_MatchesByTailnetIP(t *testing.T) {
	ids := []ColocationIdentity{{Value: "100.64.0.9", Source: "self tailnet IP"}}
	n := exitNode("57", "some-other-name", []string{"100.64.0.9", "fd7a:115c:a1e0::9"})
	if _, ok := isColocatedExitNode(n, ids); !ok {
		t.Errorf("isColocatedExitNode did not match on the node's own tailnet IP")
	}
}

// TestB265_CaseInsensitive pins the case handling (node hostnames keep
// their original case).
func TestB265_CaseInsensitive(t *testing.T) {
	ids := []ColocationIdentity{{Value: "skygate-host", Source: "SKYGATE_TS_HOSTNAME"}}
	n := exitNode("57", "SkyGate-Host", []string{"100.64.0.9"})
	if _, ok := isColocatedExitNode(n, ids); !ok {
		t.Errorf("isColocatedExitNode is case-sensitive; want a match for SkyGate-Host vs skygate-host")
	}
}

// TestHostIdentities_DedupAndLowercase pins the identity list shape:
// trimmed, lower-cased, de-duplicated, and empty values ignored.
func TestHostIdentities_DedupAndLowercase(t *testing.T) {
	t.Setenv("SKYGATE_TS_HOSTNAME", "SkyGate-Host")
	t.Setenv("SKYGATE_DERP_PROBE_HOST", " 192.0.2.69 ")
	ids := hostIdentities("skygate-host", "192.0.2.69", "MyHost", nil)
	seen := map[string]string{}
	for _, id := range ids {
		if id.Value != "" { // empty values are dropped by the helper
			if _, dup := seen[id.Value]; dup {
				t.Errorf("hostIdentities returned the duplicate value %q", id.Value)
			}
			seen[id.Value] = id.Source
		}
	}
	for _, want := range []string{"skygate-host", "192.0.2.69", "myhost"} {
		if _, ok := seen[want]; !ok {
			t.Errorf("hostIdentities missing %q (got %v)", want, seen)
		}
	}
	if len(ids) != 3 {
		t.Errorf("hostIdentities returned %d identities (%v), want 3", len(ids), ids)
	}
}

// TestExitNodeColocationSummary pins the operator-facing message shape
// (used in the boot log).
func TestExitNodeColocationSummary(t *testing.T) {
	got := ExitNodeColocationSummary([]ExitNodeColocation{
		{NodeID: "3", Hostname: "emilia", MatchedValue: "skygate-host", MatchedBy: "SKYGATE_TS_HOSTNAME"},
	})
	if got == "" || !containsAll(got, "emilia", "node_id=3", "skygate-host", "SKYGATE_TS_HOSTNAME") {
		t.Errorf("summary = %q, want it to name the relay, its id, the matched value and the source", got)
	}
	if ExitNodeColocationSummary(nil) != "" {
		t.Errorf("summary for no colocations should be empty")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
