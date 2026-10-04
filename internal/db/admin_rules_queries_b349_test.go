// admin_rules_queries_b349_test.go — B349 (2026-10-04).
//
// THE FILTERED ADMIN VIEW WAS A 500, AND ONLY A BEHAVIOURAL TEST CATCHES THAT.
//
// `qSelectAllRulesForAdminByDevice` kept the 12-column list while B277.4 had
// extended the shared scan loop to 13 destinations, so every call to the
// /admin/exit-rules?device=NAME drill-down ended in
//
//	sql: expected 12 destination arguments in Scan, not 13
//
// which the handler rendered as a raw 500. A grep contract can see that a column is
// missing; only RUNNING the query proves the SELECT and the scan agree — which is
// exactly what these tests do, for BOTH admin views, through the real public
// helpers.
package db

import (
	"database/sql"
	"testing"
)

// newAdminQueriesTestDB is a migrated SQLite database with one user, one device
// ownership row and two rules.
func newAdminQueriesTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := sql.Open("sqlite", "file:admin_queries_b349?mode=memory&cache=shared")
	if err != nil {
		t.Skipf("sqlite driver unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	mustExec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	mustExec(`CREATE TABLE IF NOT EXISTS portal_users (id INTEGER PRIMARY KEY, username TEXT, password_hash TEXT, is_admin INTEGER)`)
	mustExec(`CREATE TABLE IF NOT EXISTS node_owner_map (node_id TEXT, headscale_user_id INTEGER, username TEXT, tag TEXT, tagged_by_user_id INTEGER, tagged_at INTEGER, hostname TEXT, os TEXT, device_type TEXT)`)
	mustExec(`CREATE TABLE IF NOT EXISTS device_rules (
		id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER, device_id INTEGER, exit_node_id TEXT,
		target_type TEXT, target_value TEXT, action TEXT, enabled INTEGER, device_ip TEXT,
		parent_domain TEXT, created_at INTEGER, all_devices INTEGER)`)
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES (1, 'skyadmin', 'x', 0)`)
	mustExec(`INSERT INTO node_owner_map (node_id, username, hostname) VALUES ('56', 'skyadmin', 'cyborg')`)
	mustExec(`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, enabled, parent_domain, created_at, all_devices) VALUES
	          (1, 56, '', 'domain', 'youtube.com', 'accept', 1, 'youtube.com', 0, 0),
	          (1, 56, '', 'subnet', '8.8.8.0/24',  'accept', 1, '',           0, 0)`)
	return d
}

// TestGetAllRulesForAdminQueries_B349 runs BOTH admin views. Before the fix the
// second one failed with the scan-arity error — a 500 on the operator's screen.
func TestGetAllRulesForAdminQueries_B349(t *testing.T) {
	d := newAdminQueriesTestDB(t)

	all, err := GetAllRulesForAdmin(d)
	if err != nil {
		t.Fatalf("GetAllRulesForAdmin: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("unfiltered view: got %d rows, want 2", len(all))
	}

	byDevice, err := GetAllRulesForAdminByDevice(d, "cyborg")
	if err != nil {
		t.Fatalf("GetAllRulesForAdminByDevice returned an error — this is the B349 defect (the SELECT and the shared scan disagree on the column count): %v", err)
	}
	if len(byDevice) != 2 {
		t.Fatalf("filtered view: got %d rows (%+v), want the 2 rules of cyborg", len(byDevice), byDevice)
	}
	if byDevice[0].UserName != "skyadmin" {
		t.Errorf("filtered row 0 carries user %q, want skyadmin (the LEFT JOIN onto portal_users must still populate it)", byDevice[0].UserName)
	}
	// The all_devices column is the one that was missing; it must be READ, not
	// merely selected (a wrong position would silently shift every other field).
	if byDevice[0].AllDevices {
		t.Errorf("row 0 reports AllDevices=true on a rule seeded with all_devices=0 — the new column is not in the position the scan expects")
	}
	if !byDevice[0].Enabled {
		t.Errorf("row 0 reports Enabled=false on a rule seeded with enabled=1 — the column order drifted")
	}

	// The case-insensitivity the drill-down link relies on (the admin page
	// lower-cases the hostname in the URL, node_owner_map may not).
	if upper, uerr := GetAllRulesForAdminByDevice(d, "CYBORG"); uerr != nil || len(upper) != 2 {
		t.Errorf("GetAllRulesForAdminByDevice(\"CYBORG\") = %d rows (err %v), want 2 — the WHERE is LOWER() on both sides", len(upper), uerr)
	}
	// An unknown device is an empty result, not an error (the page renders "0
	// rules shown" and the filter banner).
	if none, nerr := GetAllRulesForAdminByDevice(d, "no-such-device"); nerr != nil || len(none) != 0 {
		t.Errorf("unknown device: %d rows (err %v), want 0 and no error", len(none), nerr)
	}
}
