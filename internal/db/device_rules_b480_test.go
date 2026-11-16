// device_rules_b480_test.go — B480 (2026-11-16).
//
// ONE RULE PER TARGET: the dedup lookup used before an insert must find the existing
// rule REGARDLESS of the relay it names, and report that relay. That is what lets the
// writer answer «already covered by karolina» instead of inserting a second row for
// emilia — the live defect that put two relays' rules on the same Discord /32s.
package db

import (
	"errors"
	"testing"
)

func TestB480_FindDeviceRuleIDAnyRelay(t *testing.T) {
	sqlDB, err := OpenFastSQLiteForTest(t.TempDir() + "/b480.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := MigrateSQLite(sqlDB); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	// The fixture seeds a rule without the whole user/device graph; the lookup under
	// test is a WHERE clause, not an integrity check.
	if _, err := sqlDB.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("disable fk enforcement: %v", err)
	}
	seed := func(relay string) {
		t.Helper()
		if _, err := sqlDB.Exec(`INSERT INTO device_rules
			(user_id, device_id, exit_node_id, target_type, target_value, enabled, created_at, action, device_ip, parent_domain, user_name, device_hostname)
			VALUES (6, 29, $1, 'subnet', '162.159.128.233/32', 1, 0, 'accept', '100.64.0.7', 'discord.com', 'michail', 'basic')`, relay); err != nil {
			t.Fatalf("seed %s: %v", relay, err)
		}
	}
	seed("karolina")

	// The existing rule is found even though the caller asks about another relay: that
	// is the whole point — the target is already covered and its relay is returned.
	id, relay, err := FindDeviceRuleIDAnyRelay(sqlDB, 6, 29, "subnet", "162.159.128.233/32")
	if err != nil {
		t.Fatalf("FindDeviceRuleIDAnyRelay: %v", err)
	}
	if id <= 0 || relay != "karolina" {
		t.Fatalf("got id=%d relay=%q, want the existing karolina row", id, relay)
	}

	// Adding the same target for another relay must NOT change which row is found.
	seed("emilia")
	id2, relay2, err := FindDeviceRuleIDAnyRelay(sqlDB, 6, 29, "subnet", "162.159.128.233/32")
	if err != nil {
		t.Fatalf("FindDeviceRuleIDAnyRelay (after duplicate): %v", err)
	}
	if relay2 != "karolina" {
		t.Fatalf("relay = %q, want the first (oldest, enabled) row to win the lookup", relay2)
	}
	if id2 != id {
		t.Fatalf("id = %d, want the first row %d (a lookup must be stable)", id2, id)
	}

	// An unknown target reports ErrNotFound, so the caller knows it may insert.
	if _, _, err := FindDeviceRuleIDAnyRelay(sqlDB, 6, 29, "subnet", "198.51.100.0/24"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound for an uncovered target", err)
	}
}
