package prefixowner

// assign_b512_test.go — B512 (2026-11-22).
//
// THE WANDERING PREFIX. Measured on the reference host: 16 relay-less rules (a CDN rule set
// created without an exit node) claimed prefixes with no relay at all, and the auto pass
// re-spread them on every reconcile — `43.112.68.68/32` left sharlotta for emilia with
// claims=1 and no failover bookkeeping. Every such move rewrites the per-CIDR ACL pins,
// which rewrites the policy, which makes headscale fan a fresh netmap out to every device.
//
// The rule these tests pin: a relay-less claim means "the engine decides", and the engine
// ALREADY decided — the existing owner is that decision. Moving a healthy owner because
// "nobody named a relay" is not a decision, it is a coin flip that costs every client a map
// update. The reservation must still be broken when the rules genuinely move (a claim that
// names a relay, or the owner dying), which the sibling tests keep covering.

import "testing"

// b512Claims builds a claim set: `relayless` prefixes have one claim naming no relay.
func b512Claims(relayless ...string) []Claim {
	out := make([]Claim, 0, len(relayless))
	for _, p := range relayless {
		out = append(out, Claim{Prefix: p, ExitNode: "", DeviceID: 9})
	}
	return out
}

func b512Existing(prefix, owner, source string) Existing {
	return Existing{Prefix: prefix, ExitNode: owner, Source: source}
}

// The core contract: a relay-less claim does NOT move an existing healthy owner.
func TestB512_RelaylessClaimKeepsTheExistingOwner(t *testing.T) {
	claims := b512Claims("43.112.68.68/32")
	existing := []Existing{b512Existing("43.112.68.68/32", "sharlotta", "auto")}
	as := Assign(claims, []string{"emilia", "karolina", "sharlotta"}, existing)
	if len(as) != 1 {
		t.Fatalf("expected one assignment, got %d", len(as))
	}
	if as[0].ExitNode != "sharlotta" {
		t.Errorf("a relay-less claim moved the owner to %q; it must stay on sharlotta — "+
			"\"engine decides\" is not a reason to evict a healthy owner", as[0].ExitNode)
	}
	if as[0].Source != "auto" {
		t.Errorf("the source of a kept relay-less prefix must stay auto, got %q", as[0].Source)
	}
}

// Repeatability is the whole point: a second pass must not move it either.
func TestB512_RelaylessClaimIsStableAcrossPasses(t *testing.T) {
	claims := b512Claims("185.199.111.153/32", "100.30.208.234/32")
	healthy := []string{"emilia", "karolina", "sharlotta"}
	owners := map[string]string{}
	var existing []Existing
	for i := 0; i < 4; i++ {
		as := Assign(claims, healthy, existing)
		existing = existing[:0]
		for _, a := range as {
			if prev, ok := owners[a.Prefix]; ok && prev != a.ExitNode {
				t.Fatalf("pass %d moved %s from %s to %s — a relay-less claim must be sticky",
					i, a.Prefix, prev, a.ExitNode)
			}
			owners[a.Prefix] = a.ExitNode
			existing = append(existing, b512Existing(a.Prefix, a.ExitNode, a.Source))
		}
	}
}

// The guard must not turn into a latch: when the owner is not healthy, the prefix moves.
func TestB512_UnhealthyOwnerStillMoves(t *testing.T) {
	claims := b512Claims("43.112.68.68/32")
	existing := []Existing{b512Existing("43.112.68.68/32", "sharlotta", "auto")}
	as := Assign(claims, []string{"emilia", "karolina"}, existing)
	if len(as) != 1 {
		t.Fatalf("expected one assignment, got %d", len(as))
	}
	if as[0].ExitNode == "sharlotta" {
		t.Errorf("an UNHEALTHY owner must lose the prefix; got %q", as[0].ExitNode)
	}
}

// And it must not overrule the rules: a claim that names a relay still decides.
func TestB512_ExplicitClaimStillBeatsTheExistingOwner(t *testing.T) {
	claims := []Claim{{Prefix: "104.16.0.0/12", ExitNode: "karolina", DeviceID: 9}}
	existing := []Existing{b512Existing("104.16.0.0/12", "emilia", "auto")}
	as := Assign(claims, []string{"emilia", "karolina", "sharlotta"}, existing)
	if len(as) != 1 || as[0].ExitNode != "karolina" {
		t.Fatalf("a rule naming karolina must win, got %+v", as)
	}
	if as[0].Source != "explicit" {
		t.Errorf("source must be explicit when the rules named the relay, got %q", as[0].Source)
	}
}
