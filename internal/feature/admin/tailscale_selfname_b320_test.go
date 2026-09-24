package admin

// tailscale_selfname_b320_test.go — B320: the CANONICAL tailnet name must belong to
// the live client, and the automated half may only ever delete an offline ghost.
//
// The live shape on the agent VM (2026-09-24), from `headscale nodes list`:
//
//	id=57  given_name="skygate-host"   name="skygate-host-1-1"  tag:dev-infra-skygate-host
//	       100.64.0.9   OFFLINE since 2026-09-21 21:32   (machine key #1)
//	id=87  given_name="skygate-host-1" name="skygate-host-1"     tag:dev-infra-skygate-host-1
//	       100.64.0.10  ONLINE, this container            (machine key #2)
//
// while the client said `Self.HostName = "skygate-host-1"` and `.env`/compose pinned
// `SKYGATE_TS_HOSTNAME=skygate-host-1`. The `-1` is a v0.33.1.9 placeholder that B251
// fixed in the code but nobody removed from the operator's files — and because
// `isInfraNode`/`isReservedSelfHostname` compare the canonical name STRICTLY, every
// self-check was keying off the ghost.

import (
	"testing"

	"skygate/internal/headscale"
)

func b320Node(id, given, tag, user string, online bool, ips ...string) headscale.NodeView {
	n := headscale.NodeView{ID: id, GivenName: given, Online: online, UserName: user, IPAddresses: ips}
	if tag != "" {
		n.Tags = []string{tag}
	}
	return n
}

func TestB320_PickSelfNameGhostOnlyDeletesAnOfflineInfraDuplicate(t *testing.T) {
	ghost := b320Node("57", "skygate-host", "tag:dev-infra-skygate-host", "tagged-devices", false, "100.64.0.9")
	live := b320Node("87", "skygate-host-1", "tag:dev-infra-skygate-host-1", "tagged-devices", true, "100.64.0.10")

	// The live conflict: the ghost is found and it is NOT the live node.
	got, ok := pickSelfNameGhost([]headscale.NodeView{ghost, live}, "skygate-host-1", ReservedSelfHostname)
	if !ok || got.ID != "57" {
		t.Fatalf("pickSelfNameGhost = %+v (ok=%v), want node 57", got, ok)
	}

	// A node with the canonical name that is ONLINE is never a delete target: a live
	// holder is a different problem and a delete is not its solution.
	online := b320Node("58", "skygate-host", "tag:dev-infra-skygate-host", "infra", true)
	if _, ok := pickSelfNameGhost([]headscale.NodeView{online}, "skygate-host-1", ReservedSelfHostname); ok {
		t.Fatal("an ONLINE node holding the canonical name must never be selected")
	}

	// Somebody else's device is never touched, even offline with the same name.
	foreign := b320Node("59", "skygate-host", "tag:dev-daniil-laptop", "daniil", false)
	if _, ok := pickSelfNameGhost([]headscale.NodeView{foreign}, "skygate-host-1", ReservedSelfHostname); ok {
		t.Fatal("a non-infra offline node must never be selected")
	}

	// The live client itself, already wearing the canonical name, is not a ghost.
	self := b320Node("87", "skygate-host", "tag:dev-infra-skygate-host", "infra", true)
	if _, ok := pickSelfNameGhost([]headscale.NodeView{self}, "skygate-host", ReservedSelfHostname); ok {
		t.Fatal("the live client holding the canonical name is not a ghost")
	}

	// A hostname that merely LOOKS like ours is ignored (strict equality, B251).
	typo := b320Node("60", "skygate-host-test", "tag:dev-infra-skygate-host-test", "infra", false)
	if _, ok := pickSelfNameGhost([]headscale.NodeView{typo}, "skygate-host-1", ReservedSelfHostname); ok {
		t.Fatal("a suffixed/typo name must not be treated as the canonical holder")
	}

	// The infra USER is enough on its own (legacy nodes from before B251 tagging).
	legacy := b320Node("61", "skygate-host", "", "infra", false)
	if _, ok := pickSelfNameGhost([]headscale.NodeView{legacy}, "skygate-host-1", ReservedSelfHostname); !ok {
		t.Fatal("an offline node owned by the infra user must be a candidate")
	}
}

func TestB320_InfraFamilyRecognition(t *testing.T) {
	cases := []struct {
		name string
		node headscale.NodeView
		want bool
	}{
		{"infra tag", b320Node("1", "x", "tag:dev-infra-emilia", "tagged-devices", true), true},
		{"infra tag (short form)", b320Node("2", "x", "tag:infra-skygate-host", "tagged-devices", true), true},
		{"infra user", b320Node("3", "x", "", "infra", true), true},
		{"uppercase tag", b320Node("4", "x", "TAG:DEV-INFRA-SKYGATE-HOST", "tagged-devices", true), true},
		{"a user's device", b320Node("5", "x", "tag:dev-michail-basic", "tagged-devices", true), false},
		{"exit node", b320Node("6", "x", "tag:exit-node", "tagged-devices", true), false},
		{"nothing", b320Node("7", "x", "", "", true), false},
	}
	for _, c := range cases {
		if got := isInfraFamilyNode(c.node); got != c.want {
			t.Errorf("%s: isInfraFamilyNode = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestB320_ParseNodeIDToleratesGarbage(t *testing.T) {
	for in, want := range map[string]int64{"57": 57, "": 0, "abc": 0, "87x": 87, " 12 ": 0} {
		if got := parseNodeID(in); got != want {
			t.Errorf("parseNodeID(%q) = %d, want %d", in, got, want)
		}
	}
}
