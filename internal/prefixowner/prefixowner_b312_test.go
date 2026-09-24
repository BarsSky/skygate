// internal/prefixowner/prefixowner_b312_test.go — B312 (v1.5.77).
//
// The operator's rule: when a relay cannot be reached at all, compensate on the other
// exit nodes, but "приоритет расставлять на сходный признак расположения" — prefer the
// relay in the same place. The engine keeps every authority it had (manual pin,
// explicit majority, sticky) and the preference is only consulted for a prefix that is
// being REASSIGNED because its owner is gone.
package prefixowner

import "testing"

func claimsB312(prefix, relay string) []Claim {
	return []Claim{{Prefix: prefix, ExitNode: relay, DeviceID: 1}}
}

// TestAssignWithPreference_PrefersTheNamedRelay: karolina is gone (not in `healthy`),
// emilia and sharlotta are both healthy, and the caller says "sharlotta is the closest
// to karolina" — the prefix must go there, not to the engine's least-loaded pick.
func TestAssignWithPreference_PrefersTheNamedRelay(t *testing.T) {
	claims := append(claimsB312("104.16.0.0/12", "karolina"), claimsB312("142.250.0.0/15", "karolina")...)
	existing := []Existing{
		{Prefix: "104.16.0.0/12", ExitNode: "karolina", Source: "explicit"},
		{Prefix: "142.250.0.0/15", ExitNode: "karolina", Source: "explicit"},
	}
	healthy := []string{"emilia", "sharlotta"}
	prefer := func(prefix, previousOwner string, candidates []string) string {
		if previousOwner == "karolina" {
			return "sharlotta"
		}
		return ""
	}
	got := AssignWithPreference(claims, healthy, existing, prefer)
	for _, a := range got {
		if a.ExitNode != "sharlotta" {
			t.Errorf("%s went to %s; want the preferred sharlotta", a.Prefix, a.ExitNode)
		}
	}
}

// TestAssignWithPreference_IgnoresAnUnhealthyChoice: a preference may never pin a
// prefix to a relay that is down (the engine's own rule, and the reason its result is
// re-checked against the healthy set).
func TestAssignWithPreference_IgnoresAnUnhealthyChoice(t *testing.T) {
	claims := claimsB312("104.16.0.0/12", "karolina")
	existing := []Existing{{Prefix: "104.16.0.0/12", ExitNode: "karolina", Source: "explicit"}}
	healthy := []string{"emilia"}
	prefer := func(prefix, previousOwner string, candidates []string) string { return "sharlotta" }
	got := AssignWithPreference(claims, healthy, existing, prefer)
	if len(got) != 1 {
		t.Fatalf("want one assignment, got %+v", got)
	}
	if got[0].ExitNode != "emilia" {
		t.Errorf("owner = %s; want emilia (the only healthy relay — sharlotta is down)", got[0].ExitNode)
	}
}

// TestAssignWithPreference_NotConsultedForAHealthyOwner: nothing moves without a
// reason. A prefix whose owner is healthy keeps its owner even when the preference
// names someone else — otherwise the preference would become a permanent reshuffle.
func TestAssignWithPreference_NotConsultedForAHealthyOwner(t *testing.T) {
	claims := claimsB312("104.16.0.0/12", "emilia")
	existing := []Existing{{Prefix: "104.16.0.0/12", ExitNode: "emilia", Source: "explicit"}}
	healthy := []string{"emilia", "sharlotta"}
	called := false
	prefer := func(prefix, previousOwner string, candidates []string) string {
		called = true
		return "sharlotta"
	}
	got := AssignWithPreference(claims, healthy, existing, prefer)
	if called {
		t.Errorf("the preference must not even be asked about a prefix whose owner is healthy")
	}
	if got[0].ExitNode != "emilia" {
		t.Errorf("owner = %s; want emilia (explicit majority, healthy)", got[0].ExitNode)
	}
}

// TestAssignWithPreference_ManualPinStillWins: the operator's own pin is authority #1
// and the location preference must not touch it while its relay is healthy.
func TestAssignWithPreference_ManualPinStillWins(t *testing.T) {
	claims := append(claimsB312("104.16.0.0/12", "karolina"), claimsB312("104.16.0.0/12", "karolina")...)
	claims = append(claims, claimsB312("104.16.0.0/12", "emilia")...)
	existing := []Existing{{Prefix: "104.16.0.0/12", ExitNode: "emilia", Source: "manual"}}
	healthy := []string{"emilia", "sharlotta"}
	prefer := func(prefix, previousOwner string, candidates []string) string { return "sharlotta" }
	got := AssignWithPreference(claims, healthy, existing, prefer)
	if got[0].ExitNode != "emilia" || got[0].Source != "manual" {
		t.Errorf("manual pin = %+v; want emilia/manual", got[0])
	}
}

// TestAssignWithPreference_NilPreferenceKeepsTheOldBehaviour: Assign (and therefore
// every existing caller) must be bit-for-bit the engine it was.
func TestAssignWithPreference_NilPreferenceKeepsTheOldBehaviour(t *testing.T) {
	claims := claimsB312("104.16.0.0/12", "karolina")
	existing := []Existing{{Prefix: "104.16.0.0/12", ExitNode: "karolina", Source: "explicit"}}
	healthy := []string{"emilia", "sharlotta"}
	withNil := AssignWithPreference(claims, healthy, existing, nil)
	withAssign := Assign(claims, healthy, existing)
	if len(withNil) != len(withAssign) || withNil[0].ExitNode != withAssign[0].ExitNode {
		t.Errorf("Assign and AssignWithPreference(nil) disagree: %+v vs %+v", withNil, withAssign)
	}
}
