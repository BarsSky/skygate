// rule_counts_b351_test.go — B351 (2026-10-05).
//
// The two halves of the operator's report, pinned:
//
//  1. the admin page's counters must come from the DATABASE, not from the 50-row
//     page slice (measured live: the same user read 19/500 on page 6 and 49/500 on
//     page 7);
//  2. the denormalised `user_name` must be repairable, because the auto-updater's
//     raw INSERTs never wrote it (measured live: 220 of 349 rows empty, ALL 68 of
//     `basic`'s).
package db

import (
	"testing"
)

// b351Exec fails the test on a seed error so a broken fixture cannot read as a
// product regression.
func TestAdminRuleCounters_B351(t *testing.T) {
	_, d, err := OpenWithDialect("sqlite:" + t.TempDir() + "/b351.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	mustExec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES
	          (1, 'skyadmin', 'x', 1), (6, 'michail', 'x', 0)`)
	// michail/device 29 (`basic`) — the live shape: three CDN-derived subnets (which
	// the quota does NOT count), one hand-entered subnet (which it does), one
	// domain rule, and one DISABLED row that no counter may see.
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, parent_domain, action, enabled) VALUES
	          (6, 29, 'basic', '', 'karolina', 'subnet', '104.16.0.0/12',   'cdn:cloudflare:discord.com', 'accept', 1),
	          (6, 29, 'basic', '', 'karolina', 'subnet', '172.64.0.0/13',   'cdn:cloudflare:discord.gg',  'accept', 1),
	          (6, 29, 'basic', '', 'karolina', 'subnet', '103.21.244.0/22', 'cdn:cloudflare:discord.media', 'accept', 1),
	          (6, 29, 'basic', '', 'karolina', 'subnet', '10.0.0.0/24',     '',                           'accept', 1),
	          (6, 29, 'basic', '', 'karolina', 'domain', 'discord.com',     '',                           'accept', 1),
	          (6, 29, 'basic', '', 'karolina', 'subnet', '1.1.1.0/24',      '',                           'accept', 0)`)
	// skyadmin/skyworker — two enabled rules on a second device of another user.
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, parent_domain, action, enabled) VALUES
	          (1, 9, 'skyworker', 'skyadmin', 'emilia', 'subnet', '8.8.8.0/24', '', 'accept', 1),
	          (1, 9, 'skyworker', 'skyadmin', 'emilia', 'domain', 'youtube.com', '', 'accept', 1)`)

	c, err := AdminRuleCounters(d)
	if err != nil {
		t.Fatalf("AdminRuleCounters: %v", err)
	}

	if got := c.EnabledByUser[6]; got != 5 {
		t.Errorf("EnabledByUser[michail] = %d, want 5 (the disabled row must not count)", got)
	}
	if got := c.EnabledByUser[1]; got != 2 {
		t.Errorf("EnabledByUser[skyadmin] = %d, want 2", got)
	}
	// The quota unit (B328): enabled AND (target_type != 'subnet' OR parent_domain = '').
	// Of michail's five: three CDN subnet rows are excluded, the hand-entered subnet
	// and the domain rule count → 2. If this ever equals EnabledByUser, the badge is
	// quoting a limit that is not the one the insert guard enforces.
	if got := c.UserFacingByUser[6]; got != 2 {
		t.Errorf("UserFacingByUser[michail] = %d, want 2 (3 CDN subnet rows are excluded)", got)
	}
	if got := c.EnabledByUserDevice[UserDeviceKey(6, 29)]; got != 5 {
		t.Errorf("EnabledByUserDevice[michail:29] = %d, want 5", got)
	}
	if got := c.EnabledByUserDeviceExit[UserDeviceExitKey(6, 29, "karolina")]; got != 5 {
		t.Errorf("EnabledByUserDeviceExit[michail:29:karolina] = %d, want 5", got)
	}
	if got := c.EnabledByUserDeviceExit[UserDeviceExitKey(1, 9, "emilia")]; got != 2 {
		t.Errorf("EnabledByUserDeviceExit[skyadmin:9:emilia] = %d, want 2", got)
	}
	if got := c.EnabledByUserDevice[UserDeviceKey(6, 999)]; got != 0 {
		t.Errorf("a device with no rules must be absent (0), got %d", got)
	}
}

// TestBackfillDeviceRuleUserNames_B351 pins the healer: it fills what portal_users
// can answer, leaves a row whose owner is gone alone, and is idempotent (the second
// run must touch nothing — it runs on every five-minute tick).
func TestBackfillDeviceRuleUserNames_B351(t *testing.T) {
	_, d, err := OpenWithDialect("sqlite:" + t.TempDir() + "/b351bf.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	mustExec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES (6, 'michail', 'x', 0)`)
	// Two rows the healer can fix (an empty owner on two devices) and one that is
	// already correct. The EXISTS guard is what keeps a row whose portal_users entry
	// is gone untouched; device_rules.user_id carries an FK here, so that case is
	// exercised by the statement itself rather than by a row this fixture could
	// legally insert.
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (6, 29, 'basic', '', 'karolina', 'subnet', '1.0.0.0/24', 'accept', 1),
	          (6, 30, 'laptop', '', 'karolina', 'subnet', '2.0.0.0/24', 'accept', 1),
	          (6, 31, 'phone', 'michail', 'karolina', 'subnet', '3.0.0.0/24', 'accept', 1)`)

	n, err := BackfillDeviceRuleUserNames(d)
	if err != nil {
		t.Fatalf("BackfillDeviceRuleUserNames: %v", err)
	}
	if n != 2 {
		t.Errorf("first run fixed %d row(s), want 2", n)
	}
	var empty int
	if err := d.QueryRow(`SELECT COUNT(*) FROM device_rules WHERE COALESCE(user_name,'') = ''`).Scan(&empty); err != nil {
		t.Fatalf("count empty: %v", err)
	}
	if empty != 0 {
		t.Errorf("%d row(s) still have an empty user_name, want 0", empty)
	}
	n2, err := BackfillDeviceRuleUserNames(d)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if n2 != 0 {
		t.Errorf("second run fixed %d row(s), want 0 (idempotent — it runs every tick)", n2)
	}
}

// TestBackfillDeviceRuleUserNames_B351_PG runs the same UPDATE on real PostgreSQL:
// the statement uses a correlated subquery plus an EXISTS guard and a
// `COALESCE(user_name,”)` predicate, all of which are worth proving on the
// dialect the live deployment actually runs (CI sets SKYGATE_TEST_PG_DSN).
func TestBackfillDeviceRuleUserNames_B351_PG(t *testing.T) {
	d := OpenTestPG(t)
	if _, err := d.Exec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES (6, 'michail', 'x', 0) ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed portal_users: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled)
	                     VALUES (6, 29, 'basic', '', 'karolina', 'subnet', '1.0.0.0/24', 'accept', 1)`); err != nil {
		t.Fatalf("seed device_rules: %v", err)
	}
	if _, err := BackfillDeviceRuleUserNames(d); err != nil {
		t.Fatalf("BackfillDeviceRuleUserNames on PG: %v", err)
	}
	var name string
	if err := d.QueryRow(`SELECT user_name FROM device_rules WHERE device_id = 29 AND target_value = '1.0.0.0/24'`).Scan(&name); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if name != "michail" {
		t.Errorf("user_name = %q, want michail", name)
	}
}
