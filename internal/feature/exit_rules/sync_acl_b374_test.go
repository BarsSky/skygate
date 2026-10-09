// sync_acl_b374_test.go — B374 (2026-10-09), the ACL half.
//
// Measured live on the reference deployment: after the 14:08 container recreate the
// assignment table had moved 197 of karolina's prefixes to emilia minutes earlier,
// and the periodic drift check deferred its re-apply with
//
//	acl-drift: periodic drift check (the assignment table did not move) needs a
//	re-apply but one ran … ago — deferring to the next pass (throttle 30m0s)
//
// — a FALSE statement (the table HAD moved) on a FALSE budget: the 30-minute
// DERIVED-ROW budget was being spent on an ownership move, so every client of every
// moved prefix lost its route for half an hour.
//
// The fix names the two cases instead of inferring them (`aclDriftBudget` /
// `aclDriftReason`), gives the ownership-move case its own entry point
// (`periodicDriftCheckAfterOwnershipMove`) and leaves B298's churn path exactly as it
// was. The tests below pin the BUDGET DECISION and the REASON, both of which are pure.
package exit_rules

import (
	"strings"
	"testing"
	"time"
)

// TestB374_AclDriftBudgetFollowsThePlanes: the operator budget when the planes
// disagree, the churn budget when nothing moved. A revert to a single budget fails
// here, and so does a swap of the two.
func TestB374_AclDriftBudgetFollowsThePlanes(t *testing.T) {
	if got := aclDriftBudget(true); got != ownershipACLThrottle {
		t.Fatalf("aclDriftBudget(moved=true) = %s, want the operator budget %s — an ownership move must not wait 30 minutes", got, ownershipACLThrottle)
	}
	if got := aclDriftBudget(false); got != churnACLThrottle {
		t.Fatalf("aclDriftBudget(moved=false) = %s, want the churn budget %s (B298 must stay intact)", got, churnACLThrottle)
	}
	if ownershipACLThrottle >= churnACLThrottle {
		t.Fatalf("the operator budget (%s) is not shorter than the churn budget (%s) — the fix would have no effect", ownershipACLThrottle, churnACLThrottle)
	}
}

// TestB374_AclDriftReasonNamesWhatHappened: the two cases must be distinguishable in
// the journal, and neither may repeat the false live sentence. This is the contract
// behind "never 'the assignment table did not move' when it did".
func TestB374_AclDriftReasonNamesWhatHappened(t *testing.T) {
	moved := aclDriftReason(true)
	stable := aclDriftReason(false)
	if strings.TrimSpace(moved) == "" || strings.TrimSpace(stable) == "" {
		t.Fatal("both reasons must be non-empty — a deferral line without a reason is exactly what the operator could not act on")
	}
	if moved == stable {
		t.Fatalf("both cases render the same sentence (%q) — the journal cannot say which plane disagreed", moved)
	}
	for _, want := range []string{"disagree", "ownership move"} {
		if !strings.Contains(moved, want) {
			t.Errorf("the moved reason does not contain %q: %q", want, moved)
		}
	}
	for _, forbidden := range []string{"did not move", "no ownership move was observed in this pass"} {
		if strings.Contains(moved, forbidden) {
			t.Errorf("the moved reason still claims nothing moved (%q): %q", forbidden, moved)
		}
	}
	if !strings.Contains(stable, "no ownership move was observed") {
		t.Errorf("the stable reason must say that no ownership move was OBSERVED (not that the table did not move): %q", stable)
	}
}

// TestB374_OwnershipMoveAppliesInsideTheChurnWindow is the behavioural half. With an
// apply five minutes old (inside the 30-minute churn budget, outside the 60-second
// operator one):
//
//   - the ownership-move path MUST apply — that is the fix;
//   - the churn path MUST NOT — that is B298, preserved.
func TestB374_OwnershipMoveAppliesInsideTheChurnWindow(t *testing.T) {
	t.Setenv("SKYGATE_ACL_VIA_ENABLED", "true")

	_, stub, s := b298Harness(t)
	setLastApply(5 * time.Minute)
	if res := s.applyACLIfDriftedThrottled("skygate-periodic-drift",
		"periodic drift check ("+aclDriftReason(true)+")", false, aclDriftBudget(true)); !res.Applied {
		t.Fatalf("an ownership move was deferred by the churn budget: %+v — this is the live 30-minute stall", res)
	}
	if got := stub.putCount(); got != 1 {
		t.Fatalf("policy PUTs after the ownership-move drift check = %d, want 1", got)
	}
}

// TestB374_ChurnStaysOnTheLongBudget is the guard against "fixing" part 2 by giving
// every drift path the operator budget: a derived-DNS rewrite five minutes after the
// last apply must still be absorbed.
func TestB374_ChurnStaysOnTheLongBudget(t *testing.T) {
	t.Setenv("SKYGATE_ACL_VIA_ENABLED", "true")

	_, stub, s := b298Harness(t)
	setLastApply(5 * time.Minute)
	if res := s.applyACLIfDriftedThrottled("skygate-periodic-drift",
		"periodic drift check ("+aclDriftReason(false)+")", false, aclDriftBudget(false)); res.Applied {
		t.Fatalf("a derived-rule drift inside the churn budget was applied anyway: %+v — B298 is undone and a file-mode host restarts headscale every five minutes", res)
	}
	if got := stub.putCount(); got != 0 {
		t.Fatalf("policy PUTs = %d, want 0 (the churn budget must absorb it)", got)
	}
}
