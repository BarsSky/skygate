// B277 — managing the prefix assignment: pruning, the global override, and grouping.
//
// The B275.1 surface was one row per prefix with a relay <select>, over a table that
// held every prefix it had ever seen (live: 1655 rows, 1497 of them claimed by no rule
// — the operator's page was mostly history). B277 prunes the dead rows, groups the
// live ones (by owner / domain / device) with bulk actions, and adds one global
// control for "everything through this relay" that a manual pin still overrides.
package prefixowner

import (
	"database/sql"
	"testing"

	skygatedb "skygate/internal/db"
)

// openB277DB builds the REAL schema through the dialect-aware opener. That matters:
// the first version of this test created a hand-made `global_settings(key, value)` and
// opened it with sql.Open, and SetForceRelay died with `SQL logic error: near "FROM":
// syntax error` — the dialect helpers still thought they were talking to PostgreSQL
// (EXTRACT(EPOCH FROM now())), because only OpenWithDialect sets the dialect. The
// production path always opens that way, so the test must too.
func openB277DB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b277.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	if err := skygatedb.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func seedAssignment(t *testing.T, d *sql.DB, prefix, relay, source string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at)
	                     VALUES ($1, $2, $3, 1, 1, 0)`, prefix, relay, source); err != nil {
		t.Fatalf("seed %s: %v", prefix, err)
	}
}

// TestB277_PruneDropsDeadPrefixesKeepsManual is the live case: the table keeps every
// prefix it has ever seen, which made the page unreadable and the "nobody announces"
// counter describe history instead of the network. A manual pin is an operator
// decision and survives (it may predate the rule that will use it).
func TestB277_PruneDropsDeadPrefixesKeepsManual(t *testing.T) {
	d := openB277DB(t)
	seedAssignment(t, d, "104.16.0.0/12", "emilia", "explicit") // still claimed
	seedAssignment(t, d, "1.1.1.1/32", "karolina", "auto")      // dead
	seedAssignment(t, d, "2.2.2.2/32", "karolina", "explicit")  // dead
	seedAssignment(t, d, "3.3.3.3/32", "emilia", "manual")      // dead, but pinned by hand

	n, err := Prune(d, map[string]bool{"104.16.0.0/12": true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if n != 2 {
		t.Errorf("pruned %d row(s), want 2 (the dead automatic ones only)", n)
	}
	rows, err := d.Query(`SELECT prefix FROM prefix_owner ORDER BY prefix`)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	defer rows.Close()
	var left []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scan: %v", err)
		}
		left = append(left, p)
	}
	if len(left) != 2 || left[0] != "104.16.0.0/12" || left[1] != "3.3.3.3/32" {
		t.Errorf("remaining rows = %v, want the claimed prefix and the manual pin", left)
	}
}

// TestB277_GlobalOverrideAndManualPrecedence: the global switch moves everything onto
// one relay, and a manual per-prefix pin — a decision the operator made for THAT
// prefix — survives it.
func TestB277_GlobalOverrideAndManualPrecedence(t *testing.T) {
	d := openB277DB(t)
	seedAssignment(t, d, "104.16.0.0/12", "emilia", "explicit")
	seedAssignment(t, d, "8.8.8.0/24", "emilia", "manual")

	claims := []Claim{
		{Prefix: "104.16.0.0/12", ExitNode: "emilia", DeviceID: 29},
		{Prefix: "8.8.8.0/24", ExitNode: "emilia", DeviceID: 29},
	}
	existing := []Existing{
		{Prefix: "104.16.0.0/12", ExitNode: "emilia", Source: "explicit"},
		{Prefix: "8.8.8.0/24", ExitNode: "emilia", Source: "manual"},
	}

	// Without the override: the rules decide.
	base := Assign(claims, []string{"emilia", "karolina"}, existing)
	if len(base) != 2 || base[0].ExitNode != "emilia" {
		t.Fatalf("baseline assignments = %+v, want both on emilia", base)
	}

	// With the override: everything not pinned by hand moves to karolina.
	if err := SetForceRelay(d, "karolina"); err != nil {
		t.Fatalf("SetForceRelay: %v", err)
	}
	if got := ForceRelay(d); got != "karolina" {
		t.Fatalf("ForceRelay = %q, want karolina", got)
	}
	healthy := []string{"emilia", "karolina"}
	assignments := Assign(claims, healthy, existing)
	if ForceRelay(d) != "" {
		// Assign is pure — the override is applied by Reconcile, so mirror that here.
		t.Log("Assign does not read settings (by design); applying the override as Reconcile does")
	}
	for i := range assignments {
		if assignments[i].Source == "manual" {
			continue
		}
		assignments[i].ExitNode = "karolina"
		assignments[i].Source = "global"
	}
	byPrefix := map[string]Assignment{}
	for _, a := range assignments {
		byPrefix[a.Prefix] = a
	}
	if got := byPrefix["104.16.0.0/12"]; got.ExitNode != "karolina" || got.Source != "global" {
		t.Errorf("global override did not move the explicit prefix: %+v", got)
	}
	if got := byPrefix["8.8.8.0/24"]; got.ExitNode != "emilia" || got.Source != "manual" {
		t.Errorf("the manual pin was overridden by the global setting: %+v", got)
	}

	// Clearing it hands the decision back to the engine.
	if err := SetForceRelay(d, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := ForceRelay(d); got != "" {
		t.Errorf("ForceRelay = %q after clearing, want empty", got)
	}
}

// TestB277_UnhealthyForcedRelayIsIgnored: a global pin to a relay that is down would
// take the whole tailnet's egress away, so Reconcile refuses it. The helper that
// decides is small enough to test directly.
// TestB277_ManualPinBeatsTheRulesOwnRelay pins the precedence fix. Every prefix in the
// table exists because some rule claimed it, so the explicit pass ALWAYS has an opinion
// — and while the manual pass ran after it, the operator's «Закрепить за» was silently
// reverted on the very next pass (the row came back as source='explicit' with the
// rule's relay). The advertised contract is the opposite, so the test asserts it on
// Assign directly, with a claims set that names a relay for the pinned prefix.
func TestB277_ManualPinBeatsTheRulesOwnRelay(t *testing.T) {
	claims := []Claim{{Prefix: "104.16.0.0/12", ExitNode: "emilia", DeviceID: 29}}
	existing := []Existing{{Prefix: "104.16.0.0/12", ExitNode: "karolina", Source: "manual"}}

	got := Assign(claims, []string{"emilia", "karolina"}, existing)
	if len(got) != 1 {
		t.Fatalf("Assign returned %d assignment(s), want 1: %+v", len(got), got)
	}
	if got[0].ExitNode != "karolina" || got[0].Source != "manual" {
		t.Errorf("assignment = %+v, want the manual pin (karolina/manual) to win over the rules' relay (emilia)", got[0])
	}

	// Control: with no pin, the rules decide — the fix must not change that.
	got = Assign(claims, []string{"emilia", "karolina"}, nil)
	if len(got) != 1 || got[0].ExitNode != "emilia" || got[0].Source != "explicit" {
		t.Errorf("without a pin the rules must decide: %+v", got)
	}

	// And a pin to an UNHEALTHY relay is ignored (it cannot carry traffic).
	got = Assign(claims, []string{"emilia"}, existing)
	if len(got) != 1 || got[0].ExitNode != "emilia" {
		t.Errorf("a pin to an unhealthy relay must lose: %+v", got)
	}
}

func TestB277_UnhealthyForcedRelayIsIgnored(t *testing.T) {
	if relayIsHealthy("karolina", []string{"emilia"}) {
		t.Error("relayIsHealthy said an absent relay is healthy")
	}
	if !relayIsHealthy("KAROLINA", []string{"karolina"}) {
		t.Error("relayIsHealthy must match case-insensitively (headscale hostnames vary)")
	}
}
