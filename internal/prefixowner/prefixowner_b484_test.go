package prefixowner

import "testing"

// TestB484_OwnerMustBeARelayThatARuleNames — the live failure of 2026-11-17.
//
// `140.82.113.22/32` had rules for karolina and emilia (one each, so no majority), the
// ownership table said `sharlotta` with source `auto`, and because `via` on a CIDR grant is a
// permission FILTER the device holding those rules lost the address while everything else
// worked. The engine must never keep or assign an owner that no rule mentions while claiming
// relays are healthy.
func TestB484_OwnerMustBeARelayThatARuleNames(t *testing.T) {
	claims := []Claim{
		{Prefix: "140.82.113.22/32", ExitNode: "karolina", DeviceID: 9},
		{Prefix: "140.82.113.22/32", ExitNode: "emilia", DeviceID: 9},
	}
	healthy := []string{"emilia", "karolina", "sharlotta"}
	prev := []Existing{{Prefix: "140.82.113.22/32", ExitNode: "sharlotta", Source: "auto"}}

	got := Assign(claims, healthy, prev)
	if len(got) != 1 {
		t.Fatalf("assignments = %d, want 1", len(got))
	}
	if got[0].ExitNode != "karolina" && got[0].ExitNode != "emilia" {
		t.Fatalf("owner = %q, want a relay a rule names (karolina or emilia) — sharlotta has no claim", got[0].ExitNode)
	}
}

// TestB484_StickyKeepsAnOwnerThatStillHasClaims — the fix must not throw away stickiness: the
// previous owner stays while at least one rule still names it (no migration without a reason).
func TestB484_StickyKeepsAnOwnerThatStillHasClaims(t *testing.T) {
	claims := []Claim{
		{Prefix: "203.0.113.0/24", ExitNode: "karolina", DeviceID: 2},
	}
	healthy := []string{"emilia", "karolina", "sharlotta"}
	prev := []Existing{{Prefix: "203.0.113.0/24", ExitNode: "karolina", Source: "auto"}}

	got := Assign(claims, healthy, prev)
	if len(got) != 1 || got[0].ExitNode != "karolina" {
		t.Fatalf("a claiming owner must stay sticky, got %+v", got)
	}
}

// TestB484_TieBreaksBetweenClaimingRelays — with a tie the winner is one of the claiming relays,
// chosen by name so it cannot oscillate between ticks.
func TestB484_TieBreaksBetweenClaimingRelays(t *testing.T) {
	claims := []Claim{
		{Prefix: "198.51.100.0/24", ExitNode: "oscar", DeviceID: 1},
		{Prefix: "198.51.100.0/24", ExitNode: "papa", DeviceID: 2},
	}
	healthy := []string{"alpha", "oscar", "papa"}
	first := Assign(claims, healthy, nil)
	if len(first) != 1 {
		t.Fatalf("assignments = %d, want 1", len(first))
	}
	if first[0].ExitNode != "oscar" && first[0].ExitNode != "papa" {
		t.Fatalf("owner = %q, want one of the claiming relays", first[0].ExitNode)
	}
	// Re-running with that answer must not move it (no oscillation).
	second := Assign(claims, healthy, []Existing{{Prefix: "198.51.100.0/24", ExitNode: first[0].ExitNode, Source: "auto"}})
	if len(second) != 1 || second[0].ExitNode != first[0].ExitNode {
		t.Fatalf("the tie-break oscillated: %q then %q", first[0].ExitNode, second[0].ExitNode)
	}
}

// TestB484_UnclaimedPrefixStillSpreads — a prefix nobody names keeps B275's even spread; the fix
// is about claims, not about disabling load balancing.
func TestB484_UnclaimedPrefixStillSpreads(t *testing.T) {
	claims := []Claim{
		{Prefix: "10.10.0.0/16", DeviceID: 1},
		{Prefix: "10.11.0.0/16", DeviceID: 1},
	}
	got := Assign(claims, []string{"emilia", "karolina"}, nil)
	if len(got) != 2 {
		t.Fatalf("assignments = %d, want 2", len(got))
	}
	owners := map[string]int{}
	for _, a := range got {
		owners[a.ExitNode]++
	}
	if len(owners) != 2 {
		t.Fatalf("unclaimed prefixes must still spread over both relays, got %v", owners)
	}
}

// TestB484_AllClaimingRelaysDownFallsBack — if every claiming relay is unhealthy the engine must
// still answer (a prefix is better served by a healthy relay than by nobody), and the choice must
// come from the healthy set.
func TestB484_AllClaimingRelaysDownFallsBack(t *testing.T) {
	claims := []Claim{{Prefix: "192.0.2.0/24", ExitNode: "karolina", DeviceID: 3}}
	got := Assign(claims, []string{"emilia", "sharlotta"}, nil)
	if len(got) != 1 {
		t.Fatalf("assignments = %d, want 1", len(got))
	}
	if got[0].ExitNode != "emilia" && got[0].ExitNode != "sharlotta" {
		t.Fatalf("owner = %q, want a healthy relay when the claiming one is down", got[0].ExitNode)
	}
}

// TestB484_ContainsRelay — the guard used by the preference path.
func TestB484_ContainsRelay(t *testing.T) {
	set := []string{"alpha", "beta"}
	if !containsRelay(set, "beta") || containsRelay(set, "gamma") {
		t.Fatalf("containsRelay is wrong for %v", set)
	}
	if containsRelay(nil, "alpha") {
		t.Fatal("an empty set contains nothing")
	}
}
