// weights_b453_test.go — B453 (2026-10-30). «веса нагрузки на relay».
package prefixowner

import (
	"testing"
)

func TestParseWeights_B453(t *testing.T) {
	w, rejected := ParseWeights(" emilia = 3 , karolina=1,broken,relay-x=0,relay-y=-2,=5")
	if w["emilia"] != 3 || w["karolina"] != 1 {
		t.Fatalf("parsed weights = %v, want emilia=3 karolina=1", w)
	}
	if _, ok := w["relay-x"]; ok {
		t.Error("weight 0 must be REJECTED, not silently kept (it looks like an intent the engine does not implement)")
	}
	if len(rejected) != 4 {
		t.Errorf("rejected = %v, want the four unusable entries named", rejected)
	}
	// The empty setting is the default state of every install: no weights, no rejections.
	empty, rejectedEmpty := ParseWeights("")
	if len(empty) != 0 || len(rejectedEmpty) != 0 {
		t.Errorf("empty setting parsed to %v / %v", empty, rejectedEmpty)
	}
	if empty.Of("anything") != DefaultWeight {
		t.Errorf("an unknown relay must weigh %d", DefaultWeight)
	}
}

// TestWeightsChangeWhereNewPrefixesLand_B453 is the property the operator asked for: a relay with a
// bigger weight takes a bigger share of the prefixes that are being decided.
func TestWeightsChangeWhereNewPrefixesLand_B453(t *testing.T) {
	relays := []string{"big", "small"}
	w := Weights{"big": 3, "small": 1}

	dist := WeightDistribution(relays, map[string]int{}, w, 8)
	if dist["big"] != 6 || dist["small"] != 2 {
		t.Fatalf("distribution = %v, want big=6 small=2 (3:1 of eight)", dist)
	}

	// And an even split when nobody set a weight — the pre-B453 behaviour must be untouched.
	even := WeightDistribution(relays, map[string]int{}, nil, 8)
	if even["big"] != 4 || even["small"] != 4 {
		t.Fatalf("unweighted distribution = %v, want an even 4/4", even)
	}
}

// TestWeightsNeverMigrateAStickyPrefix_B453 is the safety half: raising a weight must not move
// traffic that is already flowing, only change where the next decisions land.
func TestWeightsNeverMigrateAStickyPrefix_B453(t *testing.T) {
	claims := []Claim{
		{Prefix: "10.0.0.0/24"},
	}
	existing := []Existing{{Prefix: "10.0.0.0/24", ExitNode: "small", Source: "auto"}}

	// small suddenly looks tiny next to big, but it is healthy and the prefix is already there.
	got := AssignWithWeights(claims, []string{"big", "small"}, existing, Weights{"big": 100, "small": 1})
	if len(got) != 1 {
		t.Fatalf("assignments = %v", got)
	}
	if got[0].ExitNode != "small" {
		t.Fatalf("a sticky prefix migrated to %q merely because a weight changed — that is live traffic", got[0].ExitNode)
	}
}

// TestWeightsApplyWhenTheOwnerIsGone_B453: the weight DOES decide a reassignment (the engine was
// choosing anyway), and the failover is still recorded.
func TestWeightsApplyWhenTheOwnerIsGone_B453(t *testing.T) {
	claims := []Claim{{Prefix: "10.0.0.0/24"}}
	existing := []Existing{{Prefix: "10.0.0.0/24", ExitNode: "dead", Source: "auto"}}

	got := AssignWithWeights(claims, []string{"big", "small"}, existing, Weights{"big": 4, "small": 1})
	if len(got) != 1 || got[0].ExitNode != "big" {
		t.Fatalf("reassignment = %v, want the heavier relay to take it", got)
	}
	if got[0].FailoverFrom == "" {
		t.Error("the failover from the dead relay was not recorded")
	}
}

// TestWeightsAreClampedAndDeterministic_B453 pins the two ways this could break a pass: a zero or
// negative weight must not divide by zero, and equal ratios must not oscillate between passes.
func TestWeightsAreClampedAndDeterministic_B453(t *testing.T) {
	w := Weights{"a": 0, "b": -3}
	if w.Of("a") != 1 || w.Of("b") != 1 || w.Of("missing") != 1 {
		t.Fatalf("Of() must clamp to at least 1, got a=%d b=%d missing=%d", w.Of("a"), w.Of("b"), w.Of("missing"))
	}
	first := pickLeastRelative([]string{"a", "b"}, map[string]int{}, Weights{"a": 2, "b": 2})
	for i := 0; i < 20; i++ {
		if again := pickLeastRelative([]string{"a", "b"}, map[string]int{}, Weights{"a": 2, "b": 2}); again != first {
			t.Fatalf("equal ratios chose %q then %q — the table would oscillate", first, again)
		}
	}
	if first != "a" {
		t.Fatalf("equal ratios must break the tie by name, got %q", first)
	}
	// Cross-multiplication, not float: 1/3 and 2/6 are the same ratio, so the tie-break applies.
	if got := pickLeastRelative([]string{"a", "b"}, map[string]int{"a": 1, "b": 2}, Weights{"a": 3, "b": 6}); got != "a" {
		t.Fatalf("equal relative load chose %q, want the name tie-break", got)
	}
}
