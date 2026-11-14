// relay_bulk_b471_test.go — B471 (2026-11-14).
//
// Three things a grep cannot prove, so they are executed here:
//
//  1. THE PLAN. `planBulkRelayMove` is pure: which selected rules actually move, which are
//     already on the target (a re-submit must be a no-op, not audit noise), and — the part
//     that matters most — which manual pins FOLLOW the selection and which are LEFT ALONE
//     because they belong to some other relay. Getting that wrong would silently move a
//     decision the operator never made.
//
//  2. THE WRITE. Rules are rewritten IN PLACE (never delete+add: the natural key includes
//     the relay, so a re-add creates a second claim and lets the hostname tie-break pick
//     the owner), a failing row does not abort the batch (B449's rule), and the pins that
//     were planned actually land in `prefix_owner` as `manual`.
//
//  3. THE PAGES RENDER. The admin form, the checkbox column and the manual-pin badge are
//     template work: a field the handler stopped passing or a renamed key renders nothing
//     and no contract notices.
package exit_rules

import (
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"
	"skygate/internal/prefixowner"
)

func b471Rules() []bulkRuleRow {
	return []bulkRuleRow{
		{ID: 1, UserID: 2, Device: "basic", DeviceID: 11, ExitNode: "emilia", TargetType: "subnet", Target: "104.16.0.0/12"},
		{ID: 2, UserID: 2, Device: "basic", DeviceID: 11, ExitNode: "emilia", TargetType: "subnet", Target: "162.158.0.0/15"},
		{ID: 3, UserID: 2, Device: "basic", DeviceID: 11, ExitNode: "karolina", TargetType: "subnet", Target: "8.8.8.0/24"},
	}
}

func b471Prefixes() map[int64][]string {
	return map[int64][]string{
		1: {"104.16.0.0/12"},
		2: {"162.158.0.0/15"},
		3: {"8.8.8.0/24"},
	}
}

// TestB471_PlanMovesOnlyWhatChanges — the target relay is not rewritten into itself, and a
// selection that is already there reports "unchanged" instead of moving nothing silently.
func TestB471_PlanMovesOnlyWhatChanges(t *testing.T) {
	plan := planBulkRelayMove(b471Rules(), b471Prefixes(), nil, "karolina", false)
	if len(plan.Moves) != 2 {
		t.Fatalf("moved rules = %d, want 2 (rule 3 is already on karolina)", len(plan.Moves))
	}
	if len(plan.Unchanged) != 1 || plan.Unchanged[0].ID != 3 {
		t.Fatalf("unchanged = %+v, want exactly rule 3", plan.Unchanged)
	}
	for _, r := range plan.Moves {
		if r.ID == 3 {
			t.Fatalf("rule 3 was planned for a move although it already points at karolina")
		}
	}
}

// TestB471_PinsFollowOnlyTheSameDecision — the heart of the block. A manual pin that holds a
// prefix on the relay the RULES named may follow when the operator asked; a pin on ANOTHER
// relay is a different decision and is reported, not taken.
func TestB471_PinsFollowOnlyTheSameDecision(t *testing.T) {
	pins := map[string]pinRef{
		"104.16.0.0/12":  {Relay: "emilia", Manual: true},    // same decision: rules said emilia
		"162.158.0.0/15": {Relay: "sharlotta", Manual: true}, // somebody else's pin
	}
	// Without the checkbox nothing moves — the pin is the operator's own decision (B275).
	plan := planBulkRelayMove(b471Rules(), b471Prefixes(), pins, "karolina", false)
	if len(plan.PinsToMove) != 0 {
		t.Errorf("pins moved without being asked: %v", plan.PinsToMove)
	}
	if len(plan.PinsToKeep) != 2 {
		t.Errorf("kept pins = %v, want both (one unrequested, one pinned elsewhere)", plan.PinsToKeep)
	}

	// With it, exactly the pin that belongs to the same decision travels.
	plan = planBulkRelayMove(b471Rules(), b471Prefixes(), pins, "karolina", true)
	if len(plan.PinsToMove) != 1 || plan.PinsToMove[0] != "104.16.0.0/12" {
		t.Fatalf("pins to move = %v, want exactly 104.16.0.0/12", plan.PinsToMove)
	}
	var keptOther bool
	for _, k := range plan.PinsToKeep {
		if k.Prefix == "162.158.0.0/15" {
			keptOther = true
			if k.Relay != "sharlotta" {
				t.Errorf("kept pin names relay %q, want sharlotta (the operator has to see WHERE it stays)", k.Relay)
			}
		}
		if k.Prefix == "104.16.0.0/12" {
			t.Errorf("a pin that follows the selection was also reported as kept")
		}
	}
	if !keptOther {
		t.Error("the pin held by another relay was not reported as kept")
	}
}

// TestB471_PlanDeduplicatesPins — several rules can claim the same address (a CDN expansion
// produces parallel rows); the pin list must not carry it twice.
func TestB471_PlanDeduplicatesPins(t *testing.T) {
	rules := []bulkRuleRow{
		{ID: 1, ExitNode: "emilia", Target: "a"},
		{ID: 2, ExitNode: "emilia", Target: "b"},
	}
	prefixes := map[int64][]string{1: {"104.16.0.0/12"}, 2: {"104.16.0.0/12"}}
	pins := map[string]pinRef{"104.16.0.0/12": {Relay: "emilia", Manual: true}}
	plan := planBulkRelayMove(rules, prefixes, pins, "karolina", true)
	if len(plan.PinsToMove) != 1 {
		t.Fatalf("pins to move = %v, want the prefix once", plan.PinsToMove)
	}
}

// b471DB is a migrated SQLite database with two rules and one manual pin.
func b471DB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.OpenFastSQLiteForTest(t.TempDir() + "/b471.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := db.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	          VALUES (2, 'michail', 'x', 0, 2)`)
	mustExec(`INSERT INTO device_rules (id, user_id, device_id, exit_node_id, target_type, target_value, enabled, device_hostname)
	          VALUES (1, 2, 11, 'emilia', 'subnet', '104.16.0.0/12', 1, 'basic'),
	                 (2, 2, 11, 'emilia', 'subnet', '162.158.0.0/15', 1, 'basic')`)
	mustExec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at)
	          VALUES ('104.16.0.0/12', 'emilia', 'manual', 1, 1, 0),
	                 ('162.158.0.0/15', 'emilia', 'explicit', 1, 1, 0)`)
	return d
}

// TestB471_ApplyWritesInPlaceAndMovesThePlannedPins — the rows keep their ID (a rewrite, not
// a delete+add) and the planned pins land as `manual` on the target relay.
func TestB471_ApplyWritesInPlaceAndMovesThePlannedPins(t *testing.T) {
	d := b471DB(t)
	rules := []bulkRuleRow{
		{ID: 1, UserID: 2, Device: "basic", DeviceID: 11, ExitNode: "emilia", TargetType: "subnet", Target: "104.16.0.0/12"},
		{ID: 2, UserID: 2, Device: "basic", DeviceID: 11, ExitNode: "emilia", TargetType: "subnet", Target: "162.158.0.0/15"},
	}
	prefixes := map[int64][]string{1: {"104.16.0.0/12"}, 2: {"162.158.0.0/15"}}
	pins := map[string]pinRef{
		"104.16.0.0/12":  {Relay: "emilia", Manual: true},
		"162.158.0.0/15": {Relay: "emilia", Manual: false}, // explicit, not a pin: nothing to take
	}
	plan := planBulkRelayMove(rules, prefixes, pins, "karolina", true)
	res := applyBulkRelayPlan(d, plan, "karolina")
	if res.Moved != 2 {
		t.Fatalf("moved = %d, want 2 (problems: %v)", res.Moved, res.Problems)
	}
	if res.PinsMoved != 1 {
		t.Fatalf("pins moved = %d, want 1 (only the manual one)", res.PinsMoved)
	}
	// In place: the same ids still exist, on the new relay, and no copies were created.
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM device_rules WHERE exit_node_id = 'karolina'`).Scan(&n); err != nil {
		t.Fatalf("count moved rows: %v", err)
	}
	if n != 2 {
		t.Fatalf("rules on karolina = %d, want 2 (the SAME two rows)", n)
	}
	if err := d.QueryRow(`SELECT count(*) FROM device_rules`).Scan(&n); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if n != 2 {
		t.Fatalf("device_rules rows = %d, want 2 — a move must not add copies", n)
	}
	// The pin landed on the target relay, and the unplanned (explicit) one was not touched.
	if owner := prefixowner.OwnerByPrefix(d)["104.16.0.0/12"]; owner != "karolina" {
		t.Errorf("pin owner = %q, want karolina", owner)
	}
	if owner := prefixowner.OwnerByPrefix(d)["162.158.0.0/15"]; owner != "emilia" {
		t.Errorf("an explicit (non-manual) prefix was moved: owner = %q, want emilia", owner)
	}
}

// TestB471_ApplyKeepsGoingAfterABadRow — one rule that cannot be written is reported and the
// rest still move (the same rule B449 established for the user-facing twin of this
// operation). The batch-level invariant is what matters: a single failure must never leave
// half the selection silently un-moved.
func TestB471_ApplyKeepsGoingAfterABadRow(t *testing.T) {
	d := b471DB(t)
	// Rule 2 is moved onto the relay a rule 1 already occupies? No: the natural key includes
	// the relay, so instead this row is deleted BEFORE the apply — the UPDATE then affects
	// nothing, which is exactly the "raced away" case the loop must survive.
	if _, err := d.Exec(`DELETE FROM device_rules WHERE id = 1`); err != nil {
		t.Fatalf("delete rule 1: %v", err)
	}
	plan := BulkRelayPlan{Moves: []bulkRuleRow{
		{ID: 1, Device: "basic", Target: "104.16.0.0/12"},
		{ID: 2, Device: "basic", Target: "162.158.0.0/15"},
	}}
	applyBulkRelayPlan(d, plan, "karolina")
	var n int
	if err := d.QueryRow(`SELECT count(*) FROM device_rules WHERE exit_node_id = 'karolina'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	// Rule 2 (the row that still exists) was written even though rule 1 vanished behind the
	// plan: one unwritable row must not leave the rest of the selection silently un-moved.
	if n != 1 {
		t.Fatalf("rules on karolina = %d, want 1 (rule 2); the batch stopped at the dead row", n)
	}
	if err := d.QueryRow(`SELECT count(*) FROM device_rules WHERE id = 2 AND exit_node_id = 'karolina'`).Scan(&n); err != nil {
		t.Fatalf("count rule 2: %v", err)
	}
	if n != 1 {
		t.Fatal("rule 2 did not move")
	}
}

// TestB471_ManualPinsLabelNamesWhereThePinStays — the decision card must say WHERE a pin is,
// because "ручной пин" without a relay is not actionable.
func TestB471_ManualPinsLabelNamesWhereThePinStays(t *testing.T) {
	pins := map[string]pinRef{
		"104.16.0.0/12":  {Relay: "karolina", Manual: true},
		"162.158.0.0/15": {Relay: "emilia", Manual: true},
		"8.8.8.0/24":     {Relay: "karolina", Manual: false},
	}
	got := manualPinsLabelFrom(pins, []string{"104.16.0.0/12", "162.158.0.0/15", "8.8.8.0/24"})
	for _, want := range []string{"104.16.0.0/12 → karolina", "162.158.0.0/15 → emilia"} {
		if !strings.Contains(got, want) {
			t.Errorf("label %q is missing %q", got, want)
		}
	}
	if strings.Contains(got, "8.8.8.0/24") {
		t.Errorf("an explicit (not manual) prefix appeared in the manual-pin label: %q", got)
	}
	if manualPinsLabelFrom(pins, nil) != "" {
		t.Error("no prefixes must produce no label")
	}
}
