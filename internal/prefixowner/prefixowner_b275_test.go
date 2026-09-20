// B275 — the prefix→relay assignment engine. Pure-function contracts.
//
// Live case (2026-09-20): `basic`(michail)→emilia and `skyworker`(skyadmin)
// →karolina claimed the same 28 Cloudflare/Google ranges; headscale
// serves a prefix from exactly one relay, so one of the two devices lost
// those destinations depending on which relay happened to hold the
// primary. B275 makes the owner explicit, stable and operator-editable.
package prefixowner

import "testing"

func TestAssign_ExplicitMajorityWins(t *testing.T) {
	claims := []Claim{
		{Prefix: "104.16.0.0/12", ExitNode: "emilia", DeviceID: 29},
		{Prefix: "104.16.0.0/12", ExitNode: "emilia", DeviceID: 29},
		{Prefix: "104.16.0.0/12", ExitNode: "karolina", DeviceID: 9},
	}
	got := Assign(claims, []string{"emilia", "karolina"}, nil)
	if len(got) != 1 {
		t.Fatalf("assignments = %d, want 1", len(got))
	}
	if got[0].ExitNode != "emilia" || got[0].Source != "explicit" {
		t.Errorf("got {%s %s}, want {emilia explicit}", got[0].ExitNode, got[0].Source)
	}
	if got[0].Claims != 3 || got[0].Devices != 2 {
		t.Errorf("claims=%d devices=%d, want 3/2", got[0].Claims, got[0].Devices)
	}
}

func TestAssign_UnhealthyExplicitOwnerIsNotUsed(t *testing.T) {
	claims := []Claim{{Prefix: "8.8.8.0/24", ExitNode: "emilia", DeviceID: 1}}
	got := Assign(claims, []string{"karolina"}, nil)
	if len(got) != 1 || got[0].ExitNode != "karolina" {
		t.Fatalf("got %+v, want the healthy relay to take over", got)
	}
}

func TestAssign_ManualPinSurvivesAndStaysPut(t *testing.T) {
	claims := []Claim{{Prefix: "91.108.12.0/22", DeviceID: 9}} // no exit node named
	existing := []Existing{{Prefix: "91.108.12.0/22", ExitNode: "karolina", Source: "manual"}}
	got := Assign(claims, []string{"emilia", "karolina", "sharlotta"}, existing)
	if len(got) != 1 || got[0].ExitNode != "karolina" || got[0].Source != "manual" {
		t.Fatalf("got %+v, want the manual pin to be preserved", got)
	}
}

func TestAssign_AutoIsLoadBalancedAndSticky(t *testing.T) {
	claims := []Claim{
		{Prefix: "10.0.0.0/8", DeviceID: 1},
		{Prefix: "10.1.0.0/16", DeviceID: 1},
	}
	relays := []string{"emilia", "karolina"}
	first := Assign(claims, relays, nil)
	if len(first) != 2 {
		t.Fatalf("assignments = %d, want 2", len(first))
	}
	owners := map[string]int{}
	for _, a := range first {
		if a.Source != "auto" {
			t.Errorf("%s source = %s, want auto", a.Prefix, a.Source)
		}
		owners[a.ExitNode]++
	}
	if len(owners) != 2 {
		t.Errorf("auto spread = %v, want one prefix per relay", owners)
	}
	// Re-running with the previous table must not move anything.
	second := Assign(claims, relays, []Existing{
		{Prefix: first[0].Prefix, ExitNode: first[0].ExitNode, Source: "auto"},
		{Prefix: first[1].Prefix, ExitNode: first[1].ExitNode, Source: "auto"},
	})
	for i := range second {
		if second[i].ExitNode != first[i].ExitNode {
			t.Errorf("prefix %s moved from %s to %s on a no-op re-run (not sticky)",
				second[i].Prefix, first[i].ExitNode, second[i].ExitNode)
		}
	}
}

func TestAssign_EmptyHealthySetFallsBackToNamedRelays(t *testing.T) {
	// headscale unreachable: the table must not be emptied.
	claims := []Claim{{Prefix: "1.2.3.0/24", ExitNode: "emilia", DeviceID: 1}}
	got := Assign(claims, nil, nil)
	if len(got) != 1 || got[0].ExitNode != "emilia" {
		t.Fatalf("got %+v, want a fallback assignment", got)
	}
}
