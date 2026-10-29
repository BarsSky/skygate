// address_truth_b442_test.go — B442. The three rules are pure on purpose: each one is a
// statement about an ANSWER, not about DNS, a database or a headscale client.
package exit_rules

import "testing"

// TestB442_IPv6AnswersAreSeparatedNotDiscarded: before this, an AAAA answer was dropped by
// a `strings.Contains(addr, ":")` continue, so an IPv6-only domain resolved to an EMPTY set
// and every row of it was deleted while nothing said why.
func TestB442_IPv6AnswersAreSeparatedNotDiscarded(t *testing.T) {
	v4, v6 := SplitAnswers([]string{"203.0.113.10", "2001:db8::1", "", " 198.51.100.7 ", "2001:db8::2"})
	if len(v4) != 2 || v4[0] != "203.0.113.10" || v4[1] != "198.51.100.7" {
		t.Fatalf("A answers = %v", v4)
	}
	if len(v6) != 2 {
		t.Fatalf("AAAA answers = %v", v6)
	}
	// An IPv6-only destination must be RECOGNISED as such, which is what lets the caller
	// report it instead of deleting rows silently.
	v4only, v6only := SplitAnswers([]string{"2001:db8::5"})
	if len(v4only) != 0 || len(v6only) != 1 {
		t.Fatalf("IPv6-only split = %v / %v", v4only, v6only)
	}
}

// TestB442_OnePartialAnswerDoesNotDelete: the live shape — the first answer rotates and
// misses one address, the second one has it. Deleting it would take the ACL grant and the
// advertised route with it.
func TestB442_OnePartialAnswerDoesNotDelete(t *testing.T) {
	stored := []string{"203.0.113.10", "203.0.113.11", "203.0.113.12"}
	first := map[string]bool{"203.0.113.10": true}                        // .11 and .12 missing
	second := map[string]bool{"203.0.113.10": true, "203.0.113.11": true} // .11 is back
	drop, kept := ConfirmDeletions(stored, first, second, true)
	if len(kept) != 2 {
		t.Fatalf("kept = %v, want the address the second answer returned", kept)
	}
	if len(drop) != 1 || drop[0] != "203.0.113.12" {
		t.Fatalf("drop = %v, want only the address BOTH answers agree is gone", drop)
	}
}

// TestB442_AnEmptySecondAnswerDeletesNothing: when the second lookup fails (resolver
// hiccup) the caller passes an empty set — and then no row may be removed either, because
// a failed lookup is not evidence of absence.
func TestB442_AnEmptySecondAnswerDeletesNothing(t *testing.T) {
	stored := []string{"203.0.113.10"}
	drop, kept := ConfirmDeletions(stored, map[string]bool{}, map[string]bool{}, false)
	if len(drop) != 0 || len(kept) != 1 {
		t.Fatalf("drop=%v kept=%v — a failed second lookup must keep the row", drop, kept)
	}
}

// TestB442_OnlyAcceptRulesClaimAPrefix: a deny rule cannot be expressed in grants[] (B265),
// so it must not decide who serves the prefix either.
func TestB442_OnlyAcceptRulesClaimAPrefix(t *testing.T) {
	if !DenyClaimsPrefix("accept") {
		t.Error("an accept rule must claim its prefix")
	}
	if DenyClaimsPrefix("deny") {
		t.Error("a deny rule must not claim its prefix — it cannot be granted, and it can steal the owner")
	}
	if !DenyClaimsPrefix("") {
		t.Error("a rule with no action (pre-v0.31 row) must behave as accept")
	}
	if DenyClaimsPrefix("DENY") {
		t.Error("the action comparison must be case-insensitive")
	}
}
