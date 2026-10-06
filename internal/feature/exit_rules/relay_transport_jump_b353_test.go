// relay_transport_jump_b353_test.go — B353 (2026-10-06).
//
// The live sequence these tests exist for (reference deployment, measured):
//
//	exit-node sync(emilia): tailnet 100.64.0.3 is not answering … i/o timeout
//	container: tailscale ping 100.64.0.3 → timed out   (karolina, sharlotta → pong)
//	container: emilia relay="hel"    emilia: hel 13.4ms, derp28*.tailscale.com OPEN
//	container/host: derp28b/c/d.tailscale.com → i/o timeout
//	karolina: nc -z 100.64.0.3 22 → OPEN ; ssh root@100.64.0.3 → hon-brown.ptr.network
//
// A/B: emilia homed at waw → ping 85ms, tcp22 OPEN, ssh OK; homed at hel → timeout.
// So "the portal is on the tailnet" does NOT imply "the portal can reach this
// relay", and a peer relay is the only remaining path. These tests pin the hop
// selection, the hop-relative probe, and the honest labelling.
package exit_rules

import (
	"strings"
	"testing"

	"skygate/internal/db"
)

// rows for the two relays of the reference deployment, plus one unusable row.
func b353Rows() []jumpHopRow {
	return []jumpHopRow{
		{Hostname: "karolina", TailscaleIP: "100.64.0.2", SSHPort: "18022", SSHTarget: "root@100.64.0.2:18022", Proven: true},
		{Hostname: "sharlotta", TailscaleIP: "100.64.0.4", Proven: false},
		{Hostname: "emilia", TailscaleIP: "100.64.0.3", SSHTarget: "root@100.64.0.3", Proven: false},
		{Hostname: "nowhere", Proven: false},
	}
}

// TestB353_SelectJumpHopsExcludesTheTargetAndItsOwnAddresses: a hop to the relay we
// cannot reach is not a hop — and the target row is in the same table, because that
// is where relays are configured.
func TestB353_SelectJumpHopsExcludesTheTargetAndItsOwnAddresses(t *testing.T) {
	hops := selectJumpHops(b353Rows(), "emilia", []string{"100.64.0.3", "fd7a:115c:a1e0::3"})
	if len(hops) != 2 {
		t.Fatalf("want karolina + sharlotta as hops, got %+v", hops)
	}
	for _, h := range hops {
		if strings.EqualFold(h.Hostname, "emilia") {
			t.Fatalf("the target itself became a hop: %+v", h)
		}
		if strings.Contains(h.Target, "100.64.0.3") {
			t.Fatalf("a hop points at one of the target's own addresses: %+v", h)
		}
	}

	// A duplicate row under a different name but the target's address is refused too.
	dup := append(b353Rows(), jumpHopRow{Hostname: "emilia-dup", TailscaleIP: "100.64.0.3", Proven: true})
	hopsDup := selectJumpHops(dup, "emilia", []string{"100.64.0.3"})
	for _, h := range hopsDup {
		if strings.Contains(h.Target, "100.64.0.3") {
			t.Fatalf("a duplicate row of the target became a hop: %+v", h)
		}
	}
}

// TestB353_ProvenHopsComeFirst: the only evidence available inside the container is
// "this relay has answered us before", so a proven relay is tried before a relay
// skygate has never configured — and the order is stable, not map-random.
func TestB353_ProvenHopsComeFirst(t *testing.T) {
	hops := selectJumpHops(b353Rows(), "emilia", []string{"100.64.0.3"})
	if len(hops) < 2 || hops[0].Hostname != "karolina" {
		t.Fatalf("the proven relay must be first, got %+v", hops)
	}
	if !hops[0].Proven || !strings.Contains(hops[0].Why, "already applied routes") {
		t.Errorf("the first hop must carry its evidence, got %+v", hops[0])
	}
	if hops[1].Proven || !strings.Contains(hops[1].Why, "never been used as a hop") {
		t.Errorf("an unproven hop must say so, got %+v", hops[1])
	}

	// Deterministic: the same rows in a different order give the same hop order.
	reversed := []jumpHopRow{b353Rows()[2], b353Rows()[1], b353Rows()[0], b353Rows()[3]}
	again := selectJumpHops(reversed, "emilia", []string{"100.64.0.3"})
	for i := range hops {
		if again[i].Hostname != hops[i].Hostname {
			t.Fatalf("hop order is not stable: %v vs %v", hops, again)
		}
	}
}

// TestB353_HopWithoutAnAddressIsNotACandidate: jumping to a bare hostname the portal
// cannot resolve would only add a third way to fail.
func TestB353_HopWithoutAnAddressIsNotACandidate(t *testing.T) {
	for _, h := range selectJumpHops(b353Rows(), "emilia", []string{"100.64.0.3"}) {
		if strings.EqualFold(h.Hostname, "nowhere") {
			t.Fatalf("a relay with neither tailscale_ip nor ssh_target must not be a hop: %+v", h)
		}
	}
	// A public ssh_target IS acceptable: the hop has to be reachable from the portal
	// and able to reach the target, not to be on any particular network itself.
	hops := selectJumpHops([]jumpHopRow{{Hostname: "vps", SSHTarget: "deploy@relay.example.com:2200"}}, "emilia", []string{"100.64.0.3"})
	if len(hops) != 1 || hops[0].Target != "deploy@relay.example.com:2200" {
		t.Fatalf("a public ssh_target must remain a usable hop, got %+v", hops)
	}
	if !strings.Contains(hops[0].Why, "ssh_target") {
		t.Errorf("the hop must name where its address came from, got %q", hops[0].Why)
	}
}

// TestB353_JumpCandidateIsProbedThroughItsHop: probing the TARGET is the measurement
// that already failed; the new, checkable fact is whether the hop answers the portal.
func TestB353_JumpCandidateIsProbedThroughItsHop(t *testing.T) {
	jump := RelayEndpoint{Kind: RelayEndpointJump, User: "root", Host: "100.64.0.3", Port: "22",
		Jump: "root@100.64.0.2:18022", Why: "through karolina"}
	probe := jump.ProbeEndpoint()
	if probe.Host != "100.64.0.2" || probe.Port != "18022" {
		t.Fatalf("the hop must be the probe target, got %+v", probe)
	}
	if probe.Host == jump.Host {
		t.Fatalf("a jump candidate must never probe the unreachable target")
	}
	if !strings.Contains(probe.Why, "peer relay") {
		t.Errorf("the probe must be labelled as the peer relay, got %q", probe.Why)
	}
	// A direct candidate probes itself — unchanged from B310.
	direct := RelayEndpoint{Kind: RelayEndpointTailnet, Host: "100.64.0.2", Port: "18022"}
	if got := direct.ProbeEndpoint(); got.Host != direct.Host || got.Port != direct.Port {
		t.Fatalf("a direct candidate must probe itself, got %+v", got)
	}
}

// TestB353_JumpLabelNamesBothEnds: the failure text is what the operator sees in the
// flash message, and "tailnet 100.64.0.3 is not answering" through a hop and straight
// from the portal have completely different fixes.
func TestB353_JumpLabelNamesBothEnds(t *testing.T) {
	jump := RelayEndpoint{Kind: RelayEndpointJump, Host: "100.64.0.3", Port: "22", Jump: "root@100.64.0.2"}
	if got, want := jump.Label(), "tailnet-jump 100.64.0.3:22 via root@100.64.0.2"; got != want {
		t.Fatalf("Label() = %q; want %q", got, want)
	}
	if got, want := jump.Target(), "root@100.64.0.3"; got != want {
		t.Fatalf("Target() = %q; want %q (the hop travels separately)", got, want)
	}
	// The direct label keeps the pre-B353 wording exactly.
	direct := RelayEndpoint{Kind: RelayEndpointTailnet, Host: "100.64.0.2", Port: "18022"}
	if got, want := direct.Label(), "tailnet 100.64.0.2:18022"; got != want {
		t.Fatalf("a direct label changed: %q; want %q", got, want)
	}
}

// TestB353_AppendJumpCandidatesIsLastAndCarriesTheHop: the jump rung must come AFTER
// every direct candidate (a healthy relay never pays for a hop), must reuse the direct
// candidate's user/port, and must skip the whole idea when the target has no tailnet
// address for the hop to reach.
func TestB353_AppendJumpCandidatesIsLastAndCarriesTheHop(t *testing.T) {
	d := newB276DB(t)
	seed := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	seed(`INSERT INTO exit_servers (node_id, hostname, tailscale_ip, ssh_port, enabled) VALUES ('3','emilia','100.64.0.3','22',1)`)
	seed(`INSERT INTO exit_servers (node_id, hostname, tailscale_ip, ssh_port, enabled) VALUES ('11','karolina','100.64.0.2','18022',1)`)
	// The proven flag comes from the SAME record the assignment engine reads.
	recordRelayApply(d, "karolina", relayApplyOutcome{Label: "ssh=ok via tailnet", Via: "tailnet", Endpoint: "tailnet 100.64.0.2:18022"})

	cfg := relaySSHConfig{TailscaleIP: "100.64.0.3", SSHPort: "22"}
	direct := []RelayEndpoint{{Kind: RelayEndpointTailnet, User: "root", Host: "100.64.0.3", Port: "22", Why: "headscale reports this tailnet address for the relay"}}
	cands, _ := appendJumpCandidates(d, nil, cfg, "emilia", direct)

	if len(cands) < 2 {
		t.Fatalf("expected the direct candidate plus at least one hop, got %+v", cands)
	}
	if cands[0].Jump != "" || cands[0].Kind != RelayEndpointTailnet {
		t.Fatalf("the direct candidate must stay FIRST, got %+v", cands[0])
	}
	var jump *RelayEndpoint
	for i := range cands[1:] {
		if cands[i+1].Kind == RelayEndpointJump {
			jump = &cands[i+1]
			break
		}
	}
	if jump == nil {
		t.Fatalf("no jump candidate was appended: %+v", cands)
	}
	if jump.Host != "100.64.0.3" || jump.Port != "22" || jump.User != "root" {
		t.Fatalf("the jump candidate must aim at the same address/user/port as the direct one, got %+v", *jump)
	}
	if !strings.Contains(jump.Jump, "100.64.0.2") || !strings.Contains(jump.Jump, "18022") {
		t.Fatalf("the hop must carry karolina's address AND port, got %q", jump.Jump)
	}
	if !IsTailnetAddress(jump.Host) {
		t.Fatalf("only a tailnet target is wrapped, got %+v", *jump)
	}

	// Nothing for the hop to reach → no jump rung at all.
	none, _ := appendJumpCandidates(d, nil, relaySSHConfig{}, "emilia",
		[]RelayEndpoint{{Kind: RelayEndpointName, Host: "emilia"}})
	if len(none) != 1 {
		t.Fatalf("a target with no tailnet address must not gain jump candidates, got %+v", none)
	}
}

// TestB353_ProvenRelaysReadsTheApplyRecord: the hop ordering must use the same
// evidence as prefix assignment (a successful `relay_apply_state`), and a FAILED
// record must not be read as proven.
func TestB353_ProvenRelaysReadsTheApplyRecord(t *testing.T) {
	d := newB276DB(t)
	recordRelayApply(d, "karolina", relayApplyOutcome{Label: "ssh=ok via tailnet", Via: "tailnet", Endpoint: "tailnet 100.64.0.2:18022"})
	recordRelayApply(d, "emilia", relayApplyOutcome{Label: "ssh=err=tailnet 100.64.0.3 is not answering"})
	if err := db.SetGlobalSetting(d, "relay_apply_state:sharlotta", "1700000000|ok"); err != nil {
		t.Fatalf("seed sharlotta: %v", err)
	}

	got := provenRelays(d)
	if !got["karolina"] {
		t.Errorf("a successful apply must mark the relay proven: %v", got)
	}
	if got["emilia"] {
		t.Errorf("a FAILED apply must not mark the relay proven: %v", got)
	}
	if !got["sharlotta"] {
		t.Errorf("the record is read case-insensitively and from the same prefix: %v", got)
	}
}
