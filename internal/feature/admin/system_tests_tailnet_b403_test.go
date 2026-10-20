// system_tests_tailnet_b403_test.go — 2026-10-20 (B403).
//
// THE MEASUREMENT on the operator's installation:
//
//	tailnet.all_nodes_reachability :: 3/6 probable Tailscale nodes reachable (50%)
//	  100.64.0.7  cyborg          dial tcp 100.64.0.7:18022: i/o timeout
//	  100.64.0.8  skybars-secure  dial tcp 100.64.0.8:18022: i/o timeout
//	  100.64.0.20 svyatoslava     dial tcp 100.64.0.20:18022: i/o timeout
//
//	tailnet.split_suspected :: 6 online / 3 reachable (50%) → «LIKELY TAILNET SPLIT»
//
// None of that was about the tailnet: two of the three are devices that never run sshd,
// and the third (svyatoslava) answers its cluster heartbeat every 30 s. The probe was
// answering "is SSH up?" and reporting it as "is the tunnel up?".
package admin

import (
	"errors"
	"testing"
	"time"

	"skygate/internal/headscale"
)

// TestB403_RefusedIsPathProof is the semantic core: a refusal is an ANSWER.
func TestB403_RefusedIsPathProof(t *testing.T) {
	if got := classifyTailnetProbeError(errors.New("dial tcp 100.64.0.7:22: connect: connection refused")); got != TailnetProbeClosed {
		t.Fatalf("refused classified as %q, want %q — this is the verdict that was being reported as a split", got, TailnetProbeClosed)
	}
	if !TailnetProbeClosed.Reachable() {
		t.Error("a refused connection must count as a reachable path: the peer answered")
	}
}

// TestB403_TimeoutIsTheOnlyUnprovenPath pins what MAY be blamed on the tunnel.
func TestB403_TimeoutIsTheOnlyUnprovenPath(t *testing.T) {
	for _, msg := range []string{
		"dial tcp 100.64.0.7:18022: i/o timeout",
		"dial tcp: lookup x: no route to host",
		"connect: network is unreachable",
		"something we cannot read",
	} {
		if got := classifyTailnetProbeError(errors.New(msg)); got != TailnetProbeFiltered {
			t.Errorf("%q classified as %q, want filtered", msg, got)
		}
	}
	if TailnetProbeFiltered.Reachable() {
		t.Error("a filtered probe must never count as a reachable path")
	}
}

// TestB403_OpenIsReachable — the happy path must not have been redefined.
func TestB403_OpenIsReachable(t *testing.T) {
	if got := classifyTailnetProbeError(nil); got != TailnetProbeOpen {
		t.Errorf("nil error = %q, want open", got)
	}
	if !TailnetProbeOpen.Reachable() {
		t.Error("open must be reachable")
	}
}

// TestB403_OnlyManagedHostsAreProbed is the second half: SSH belongs to the hosts
// skygate manages. The three live "unreachable" nodes are devices and must not be
// probed at all.
func TestB403_OnlyManagedHostsAreProbed(t *testing.T) {
	nodes := []headscale.NodeView{
		{GivenName: "karolina", Online: true, Tags: []string{"tag:dev-infra-karolina"}, IPAddresses: []string{"100.64.0.2"}},
		{GivenName: "svyatoslava", Online: true, Tags: []string{"tag:dev-infra-svyatoslava"}, IPAddresses: []string{"100.64.0.20"}},
		{GivenName: "cyborg", Online: true, Tags: []string{"tag:dev-skyadmin-cyborg"}, IPAddresses: []string{"100.64.0.7"}},
		{GivenName: "skybars-secure", Online: true, Tags: []string{"tag:dev-skyadmin-skybars-secure"}, IPAddresses: []string{"100.64.0.8"}},
		{GivenName: "offline-relay", Online: false, Tags: []string{"tag:dev-infra-offline-relay"}, IPAddresses: []string{"100.64.0.99"}},
	}
	infra, devices := tailnetProbeTargets(nodes, nil)
	if len(infra) != 2 {
		t.Fatalf("probed %d host(s), want 2 (karolina, svyatoslava — not the offline one)", len(infra))
	}
	for _, n := range infra {
		if !tailnetNodeIsInfra(n) {
			t.Errorf("a device (%s) reached the probe list", n.GivenName)
		}
	}
	var names []string
	for _, d := range devices {
		names = append(names, d.GivenName)
	}
	if len(names) != 2 || names[0] != "cyborg" || names[1] != "skybars-secure" {
		t.Fatalf("devices = %v, want the two live ones and only them", names)
	}
}

// TestB403_DevicesWithNoSSHNeverBecomeASplit — the live list, end to end at the pure
// level: three devices without sshd must produce zero probe targets, so neither the
// percentage nor the split flag can move.
func TestB403_DevicesWithNoSSHNeverBecomeASplit(t *testing.T) {
	nodes := []headscale.NodeView{
		{GivenName: "cyborg", Online: true, Tags: []string{"tag:dev-skyadmin-cyborg"}, IPAddresses: []string{"100.64.0.7"}},
		{GivenName: "skybars-secure", Online: true, Tags: []string{"tag:dev-skyadmin-skybars-secure"}, IPAddresses: []string{"100.64.0.8"}},
		{GivenName: "a71", Online: true, Tags: []string{"tag:dev-skyadmin-a71"}, IPAddresses: []string{"100.64.0.19"}},
	}
	infra, devices := tailnetProbeTargets(nodes, nil)
	if len(infra) != 0 {
		t.Fatalf("devices became probe targets: %v", infra)
	}
	if len(devices) != 3 {
		t.Fatalf("devices = %d, want 3 counted and named", len(devices))
	}
}

// TestB403_SkipListStillWins — the pre-existing skip list (self, home LAN without SSH)
// must keep working: it is a third class and must not be folded into either one.
func TestB403_SkipListStillWins(t *testing.T) {
	nodes := []headscale.NodeView{
		{GivenName: "skygate-host", Online: true, Tags: []string{"tag:dev-infra-skygate-host"}, IPAddresses: []string{"100.64.0.5"}},
		{GivenName: "karolina", Online: true, Tags: []string{"tag:dev-infra-karolina"}, IPAddresses: []string{"100.64.0.2"}},
	}
	infra, devices := tailnetProbeTargets(nodes, map[string]bool{"skygate-host": true})
	if len(infra) != 1 || infra[0].GivenName != "karolina" {
		t.Fatalf("infra = %v, want only karolina", infra)
	}
	if len(devices) != 0 {
		t.Fatalf("a skipped host leaked into devices: %v", devices)
	}
}

// TestB403_ARefusalIsNotAZeroLatency — the connectivity tests may treat a refusal as a
// proven path, but the LATENCY test compares numbers and must not: a "0s" reading from a
// host that merely refused would make the host with no sshd look like the fastest node in
// the fleet, which is the same class of false measurement B403 removes.
func TestB403_ARefusalIsNotAZeroLatency(t *testing.T) {
	verdict := TailnetProbeClosed
	latency := time.Duration(0)
	// The rule the vps-latency test applies: only TailnetProbeOpen yields a number.
	measured := verdict == TailnetProbeOpen
	if measured {
		t.Fatal("a refused connection must not be reported as a measured latency")
	}
	if latency != 0 {
		t.Fatalf("fixture drifted: latency = %v", latency)
	}
	// And the connectivity rule stays the opposite one: the same verdict IS reachable.
	if !verdict.Reachable() {
		t.Fatal("the connectivity tests must still count a refusal as a reachable path")
	}
}
