// prefixowner_b443_test.go — B443. «ПРАВИЛА ЗАВЯЗАНЫ НА ПОЛЬЗОВАТЕЛЕЙ И МЕЖДУ ПОЛЬЗОВАТЕЛЯМИ
// НЕТ ГОНКИ ВНУТРИ EXIT-NODES» — the operator asked for this to be CONFIRMED, not asserted
// in prose. What can be confirmed by a test is the property that makes a race impossible:
// the decision is a PURE FUNCTION of the claim set. It does not depend on the order the
// rows came back in, on which user submitted first, or on repeated evaluation — so two
// users whose rules touch the same prefix cannot win by being earlier, and a re-run cannot
// flip the answer.
package prefixowner

import (
	"math/rand"
	"testing"
)

func claimsForB443() []Claim {
	// Two users, one contested prefix: alice's two devices each claim it via emilia,
	// bob's one device claims it via karolina. Majority decides (alice), regardless of who
	// wrote first and regardless of the row order.
	return []Claim{
		{Prefix: "104.16.0.0/12", ExitNode: "emilia", DeviceID: 1},
		{Prefix: "104.16.0.0/12", ExitNode: "emilia", DeviceID: 2},
		{Prefix: "104.16.0.0/12", ExitNode: "karolina", DeviceID: 3},
	}
}

// TestB443_AssignmentIsOrderIndependent is the no-race proof at its strongest form: EVERY
// permutation of the same claims produces the same assignment.
func TestB443_AssignmentIsOrderIndependent(t *testing.T) {
	healthy := []string{"emilia", "karolina"}
	want := Assign(claimsForB443(), healthy, nil)
	if len(want) != 1 {
		t.Fatalf("expected exactly one assignment, got %d", len(want))
	}
	base := want[0]

	claims := claimsForB443()
	rng := rand.New(rand.NewSource(443))
	for round := 0; round < 50; round++ {
		rng.Shuffle(len(claims), func(i, j int) { claims[i], claims[j] = claims[j], claims[i] })
		got := Assign(claims, healthy, nil)
		if len(got) != 1 {
			t.Fatalf("round %d: %d assignments", round, len(got))
		}
		if got[0].Prefix != base.Prefix || got[0].ExitNode != base.ExitNode {
			t.Fatalf("round %d: assignment changed with the row order: %s → %s (was %s → %s)",
				round, got[0].Prefix, got[0].ExitNode, base.Prefix, base.ExitNode)
		}
	}
}

// TestB443_RepeatedEvaluationIsStable: a second pass over the SAME claims against the
// previous table must not move anything. If it did, two users' pages would show different
// owners seconds apart — the visible form of a race.
func TestB443_RepeatedEvaluationIsStable(t *testing.T) {
	healthy := []string{"emilia", "karolina"}
	claims := claimsForB443()
	first := Assign(claims, healthy, nil)
	if len(first) != 1 {
		t.Fatalf("first pass: %d assignments", len(first))
	}
	existing := []Existing{{Prefix: first[0].Prefix, ExitNode: first[0].ExitNode, Source: first[0].Source}}
	second := Assign(claims, healthy, existing)
	if len(second) != 1 || second[0].ExitNode != first[0].ExitNode {
		t.Fatalf("a re-run moved the owner without any input changing: %v → %v", first, second)
	}
}

// TestB443_WhoWroteFirstDoesNotMatter: the same claim MULTISET submitted in the two
// possible "arrival" orders gives one answer. This is the user-facing promise: there is no
// first-come-first-served inside the exit-node assignment.
func TestB443_WhoWroteFirstDoesNotMatter(t *testing.T) {
	healthy := []string{"emilia", "karolina"}
	alice := []Claim{
		{Prefix: "198.51.100.0/24", ExitNode: "emilia", DeviceID: 1},
		{Prefix: "198.51.100.0/24", ExitNode: "emilia", DeviceID: 2},
	}
	bob := []Claim{{Prefix: "198.51.100.0/24", ExitNode: "karolina", DeviceID: 3}}

	a := Assign(append(append([]Claim{}, alice...), bob...), healthy, nil)
	b := Assign(append(append([]Claim{}, bob...), alice...), healthy, nil)
	if a[0].ExitNode != b[0].ExitNode {
		t.Fatalf("the answer depended on who submitted first: %s vs %s", a[0].ExitNode, b[0].ExitNode)
	}
}
