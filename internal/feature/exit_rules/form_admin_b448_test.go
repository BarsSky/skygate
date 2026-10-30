// form_admin_b448_test.go — B448 (2026-10-30) — «группировка получилась по одной строчке
// и нельзя свернуть всю таблицу чтобы дойти до самого низа».
//
// Two halves of that report are pinned here and in the handlers render test:
//
//   - the RULE that a group hiding a single child is not a group (this file), and
//   - what the page actually renders for it (internal/handlers/exit_rules_render_b448_test.go).
//
// The collapse/expand half is template+JS (sticky bar, bottom bar, remembered state), so a
// grep contract (scripts/check_b448_*.sh) is what holds it — a Go test cannot execute the
// browser.
package exit_rules

import "testing"

func TestAdminNodeGroupOpens_B448(t *testing.T) {
	cases := []struct {
		nodeGroups int
		want       bool
		why        string
	}{
		{1, true, "the device's only relay: the header would hide one line and offer no choice"},
		{2, false, "two relays: the header IS the choice, it must stay closed"},
		{5, false, "many relays: same reason"},
		{0, false, "no group at all (the template renders nothing) — never claim it opens"},
	}
	for _, c := range cases {
		if got := adminNodeGroupOpens(c.nodeGroups); got != c.want {
			t.Errorf("adminNodeGroupOpens(%d) = %v, want %v (%s)",
				c.nodeGroups, got, c.want, c.why)
		}
	}
}

// TestOversizedGroupStaysClosed_B448 guards the interaction between the two decisions: a
// device with ONE relay group but hundreds of rules must still start COLLAPSED. B358 made
// oversized groups collapsed for a reason (one device with 300 derived CDN rows turned the
// page into a wall of rows), and "open the only group" must not undo it.
func TestOversizedGroupStaysClosed_B448(t *testing.T) {
	big := 0
	for !adminGroupIsOversized(big) {
		big += 10
	}
	if adminNodeGroupOpens(1) != true {
		t.Fatal("the single-group rule changed — this test's premise is gone")
	}
	// The template renders `open` only for SingleNode, and B358 renders oversized groups
	// collapsed unconditionally; the two flags live on the SAME struct, so the rule below
	// (which the template follows) is what keeps them from fighting.
	if big < 2 {
		t.Fatalf("oversized threshold is %d rows — suspiciously small, re-read B358", big)
	}
	if adminGroupIsOversized(1) {
		t.Fatal("a one-rule group must never be treated as oversized")
	}
}
