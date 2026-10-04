// device_index_b348_test.go — B348 (2026-10-04).
//
// The inventory is what makes a device survive pagination, so these tests are
// about COMPLETENESS (every device with enabled rules, with the right count) and
// about the drill-down MATCHING (a link carries the denormalised hostname, while
// the page groups by the headscale-resolved DeviceName — both must find the rules,
// or the "show rules" button lands on an empty page).
package exit_rules

import (
	"testing"

	skygatedb "skygate/internal/db"
)

func TestDeviceRuleCounts_B348(t *testing.T) {
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b348.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	mustExec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	// device_rules.user_id FKs portal_users, so both owners come first.
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES
	          (1, 'skyadmin', 'x', 0), (6, 'michail', 'x', 0)`)
	// Two users, three devices — the live shape that exposed the bug: one device
	// with a large rule set (skyworker, 215 live) and one with a small one
	// (cyborg, 12 live) that the row window pushed off page 1.
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (1, 56, 'cyborg',    'skyadmin', '', 'subnet', '8.8.8.0/24',  'accept', 1),
	          (1, 56, 'cyborg',    'skyadmin', '', 'domain', 'youtube.com', 'accept', 1),
	          (1,  9, 'skyworker', 'skyadmin', '', 'subnet', '1.1.1.0/24',  'accept', 1),
	          (1,  9, 'skyworker', 'skyadmin', '', 'subnet', '1.0.0.0/24',  'accept', 1),
	          (1,  9, 'skyworker', 'skyadmin', '', 'subnet', '9.9.9.0/24',  'accept', 1),
	          (1,  8, 'tablet',    'skyadmin', '', 'subnet', '2.2.2.0/24',  'accept', 0),
	          (6, 30, 'basic',     'michail',  '', 'subnet', '3.3.3.0/24',  'accept', 1),
	          (1, 56, '',          'skyadmin', '', 'subnet', '4.4.4.0/24',  'accept', 1)`)
	// exit_servers is irrelevant here, but device_rules has no FK to it in SQLite.

	// Per-user inventory: EVERY device with enabled rules, and only those.
	got, err := DeviceRuleCountsForUser(d, 1)
	if err != nil {
		t.Fatalf("DeviceRuleCountsForUser: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("user 1: got %d rows (%+v), want exactly 2 (cyborg, skyworker; tablet's rule is disabled and the empty hostname is a pre-rename row)", len(got), got)
	}
	if got[0].Hostname != "cyborg" || got[0].Rules != 2 {
		t.Errorf("row 0 = %+v, want {cyborg 2}", got[0])
	}
	if got[1].Hostname != "skyworker" || got[1].Rules != 3 {
		t.Errorf("row 1 = %+v, want {skyworker 3}", got[1])
	}
	// The whole point: the small device is present even though the large one owns
	// far more rows. A row-windowed query would have returned only skyworker.
	if other, oerr := DeviceRuleCountsForUser(d, 6); oerr != nil || len(other) != 1 || other[0].Hostname != "basic" || other[0].UserName != "" {
		t.Errorf("user 6 inventory = %+v (err %v), want one entry {basic 1} with an empty user (the per-user query does not join)", other, oerr)
	}

	// Cross-user inventory: the same devices, attributed where the denormalised
	// user_name exists.
	all, err := DeviceRuleCountsForAdmin(d)
	if err != nil {
		t.Fatalf("DeviceRuleCountsForAdmin: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("admin inventory: got %d rows (%+v), want 3 (basic, cyborg, skyworker)", len(all), all)
	}
	// ORDER BY user_name, hostname → michail/basic, skyadmin/cyborg, skyadmin/skyworker.
	if all[0].UserName != "michail" || all[0].Hostname != "basic" {
		t.Errorf("row 0 = %+v, want {michail basic}", all[0])
	}
	if all[1].UserName != "skyadmin" || all[1].Hostname != "cyborg" || all[1].Rules != 2 {
		t.Errorf("row 1 = %+v, want {skyadmin cyborg 2}", all[1])
	}
}

func TestFilterRulesByDevice_B348(t *testing.T) {
	mkRule := func(id, devID int, name, value string) skygatedb.DeviceRule {
		return skygatedb.DeviceRule{ID: id, DeviceID: devID, DeviceName: name, TargetValue: value, Enabled: true}
	}
	rules := []skygatedb.DeviceRule{
		mkRule(1, 56, "cyborg", "youtube.com"),
		mkRule(2, 56, "cyborg", "8.8.8.0/24"),
		mkRule(3, 9, "skyworker", "1.1.1.0/24"),
	}

	// Case-insensitive on the DISPLAYED name (headscale lower-cases a node; the
	// rule row keeps whatever the form wrote).
	if got := filterRulesByDevice(rules, "CYBORG", nil); len(got) != 2 {
		t.Errorf("filter CYBORG by name: got %d rows, want 2", len(got))
	}
	// A node whose name could not be resolved is matched by the device id the
	// inventory resolved — the drill-down must not be empty for an offline device.
	unnamed := []skygatedb.DeviceRule{mkRule(4, 77, "", "2.2.2.0/24"), mkRule(5, 78, "", "3.3.3.0/24")}
	if got := filterRulesByDevice(unnamed, "offline-node", map[int]bool{77: true}); len(got) != 1 || got[0].ID != 4 {
		t.Errorf("filter by resolved device id: got %+v, want only rule 4", got)
	}
	// An empty filter is a no-op (the unfiltered list must be untouched).
	if got := filterRulesByDevice(rules, "", nil); len(got) != 3 {
		t.Errorf("empty filter changed the slice: got %d rows, want 3", len(got))
	}
	// A device with rules that is NOT the one asked for must never leak in.
	if got := filterRulesByDevice(rules, "skyworker", nil); len(got) != 1 || got[0].ID != 3 {
		t.Errorf("filter skyworker: got %+v, want only rule 3", got)
	}
}

func TestEqualFoldASCII_B348(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"cyborg", "cyborg", true},
		{"CYBORG", "cyborg", true},
		{"CyBoRg", "cYbOrG", true},
		{"cyborg", "cybor", false},
		{"", "", true},
		{"cyborg", "", false},
		{"cyborg-2", "cyborg-2", true},
		{"cyborg2", "cyborg3", false},
	} {
		if got := equalFoldASCII(tc.a, tc.b); got != tc.want {
			t.Errorf("equalFoldASCII(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
