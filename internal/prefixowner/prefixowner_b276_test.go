// B276 — the ownership vote must be per DEVICE, not per derived row.
//
// A domain rule is expanded by the CDN resolver into one derived row per parent
// domain, and several parents can share a CIDR (live: `basic` carried five
// `104.16.0.0/12` rows for one device, and the auto-updater rewrote ~145 rows and
// re-deduplicated ~110 every five minutes). Assign() reads the claim COUNTS as the
// operator's vote — "explicit majority wins, hostname breaks a tie" — so counting
// rows let that churn decide which relay owns a prefix. Since B275 makes the owner
// the per-CIDR ACL pin, a churn-driven flip silently invalidates every pin for that
// prefix: the device keeps its rule, the table points at another relay, and the
// route stops being delivered (measured on the reference host: 28 Cloudflare/Google
// prefixes flipped, and the ACL — which is only regenerated on a rule/user/device
// change — kept the old pins).
package prefixowner

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openB276ClaimsDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", "file:"+t.TempDir()+"/b276.db")
	if err != nil {
		t.Skipf("sqlite driver unavailable: %v", err)
	}
	if _, err := d.Exec(`CREATE TABLE device_rules (
		id INTEGER PRIMARY KEY,
		user_id INTEGER DEFAULT 0,
		device_id INTEGER DEFAULT 0,
		user_name TEXT DEFAULT '',
		exit_node_id TEXT DEFAULT '',
		target_type TEXT DEFAULT '',
		target_value TEXT DEFAULT '',
		enabled INTEGER DEFAULT 1
	)`); err != nil {
		t.Fatalf("create device_rules: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// TestB276_LoadClaimsCountsOneVotePerDevice is the regression: five rows for the
// same (device, relay, prefix) must read back as ONE claim, so the majority cannot
// be manufactured by duplicates.
func TestB276_LoadClaimsCountsOneVotePerDevice(t *testing.T) {
	d := openB276ClaimsDB(t)
	// Five derived rows for device 29 (basic) → emilia on the same /12, exactly the
	// duplicate shape the CDN expansion produced live.
	for i := 0; i < 5; i++ {
		if _, err := d.Exec(`INSERT INTO device_rules (device_id, exit_node_id, target_type, target_value, enabled)
		                     VALUES (29, 'emilia', 'ip', '104.16.0.0/12', 1)`); err != nil {
			t.Fatalf("insert duplicate: %v", err)
		}
	}
	// One row for device 9 (skyworker) → karolina on the same prefix.
	if _, err := d.Exec(`INSERT INTO device_rules (device_id, exit_node_id, target_type, target_value, enabled)
	                     VALUES (9, 'karolina', 'ip', '104.16.0.0/12', 1)`); err != nil {
		t.Fatalf("insert single: %v", err)
	}

	claims, err := LoadClaims(d)
	if err != nil {
		t.Fatalf("LoadClaims: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("LoadClaims returned %d claims, want 2 (one per device+relay, NOT one per derived row): %+v", len(claims), claims)
	}

	// And the vote itself: a 1:1 tie, which the hostname tie-break resolves. With
	// the pre-B276 row counting this was 5:1 for emilia — the same two devices and
	// the same rules producing a different owner depending on the dedup phase.
	assignments := Assign(claims, []string{"emilia", "karolina"}, nil)
	if len(assignments) != 1 {
		t.Fatalf("Assign returned %d assignments, want 1: %+v", len(assignments), assignments)
	}
	if got := assignments[0]; got.Claims != 2 || got.Devices != 2 {
		t.Errorf("assignment = %+v, want claims=2 devices=2 (one vote per device)", got)
	}
	if assignments[0].ExitNode != "emilia" {
		t.Errorf("owner = %q, want emilia (1:1 tie broken by hostname, deterministically)", assignments[0].ExitNode)
	}
}

// TestB276_LoadClaimsIgnoresDisabledAndDomainRows: only enabled ip/subnet rows are
// claims — a domain row is not a prefix, and a disabled rule must not vote.
func TestB276_LoadClaimsIgnoresDisabledAndDomainRows(t *testing.T) {
	d := openB276ClaimsDB(t)
	for _, q := range []string{
		`INSERT INTO device_rules (device_id, exit_node_id, target_type, target_value, enabled) VALUES (29,'emilia','domain','discord.com',1)`,
		`INSERT INTO device_rules (device_id, exit_node_id, target_type, target_value, enabled) VALUES (29,'emilia','ip','1.1.1.1/32',0)`,
		`INSERT INTO device_rules (device_id, exit_node_id, target_type, target_value, enabled) VALUES (29,'emilia','subnet','10.0.0.0/24',1)`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	claims, err := LoadClaims(d)
	if err != nil {
		t.Fatalf("LoadClaims: %v", err)
	}
	if len(claims) != 1 || claims[0].Prefix != "10.0.0.0/24" {
		t.Fatalf("claims = %+v, want only the enabled subnet row", claims)
	}
}
