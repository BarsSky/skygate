// internal/db/device_rule_owner_b341_test.go — B341 (2026-10-01).
//
// OwnerTagCountsForDeviceRules answers the question the preferred-exit reconciler
// needs when a device's rules name NO relay: "which relay would the DATA plane use
// for the prefixes this device's rules cover?" It must answer it from the same
// table the ACL pin comes from (prefix_owner), on BOTH backends, and it must match
// the device's rules the same way collectDevicePrefState does (denormalised
// hostname OR the current hostname via node_owner_map) — otherwise a renamed
// device would silently look like a device with no rules.
package db

import (
	"database/sql"
	"strings"
	"testing"
)

// b341SQLiteDB opens a migrated, PRIVATE in-memory SQLite database (private per
// test because file::memory:?cache=shared is process-wide).
func b341SQLiteDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := "file:b341_" + strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + "?mode=memory&cache=shared"
	_, d, err := OpenWithDialect(dsn)
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := ApplyMigrations(d, DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations(sqlite): %v", err)
	}
	return d
}

// seedB341 writes the live shape of the `cyborg` case: an owner in prefix_owner
// for the prefixes the rules cover, and rules that carry NO relay.
func seedB341(t *testing.T, d *sql.DB) {
	t.Helper()
	mustExec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	// device_rules.user_id has a real FK to portal_users (ON DELETE CASCADE), so
	// the owner must exist before any rule can point at it.
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES (1, 'skyadmin', 'x', 0) ON CONFLICT (id) DO NOTHING`)
	// cyborg is node 56 under headscale's synthetic owner; the relay is emilia.
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname) VALUES
	          ('56', 0, 'tagged-devices', 'tag:dev-skyadmin-cyborg', 'cyborg'),
	          ('3',  0, 'tagged-devices', 'tag:dev-infra-emilia',    'emilia'),
	          ('9',  0, 'tagged-devices', 'tag:dev-skyadmin-skyworker', 'skyworker')`)
	// The assignment table: emilia owns the youtube prefixes, karolina owns one more.
	mustExec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices, updated_at) VALUES
	          ('8.8.8.0/24',   'emilia',   'explicit', 3, 3, 0),
	          ('74.125.0.0/16','emilia',   'explicit', 3, 3, 0),
	          ('1.1.1.0/24',   'karolina', 'auto',     1, 1, 0)`)
	// cyborg's rules: 11 rows, every one with an EMPTY relay (the live defect), one
	// of them carrying a stale hostname so only the node_owner_map branch matches.
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '8.8.8.0/24',    'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '74.125.0.0/16', 'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '9.9.9.0/24',    'accept', 1),
	          (1, 56, '',       'skyadmin', '', 'subnet', '1.1.1.0/24',    'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'domain', 'youtube.com',   'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '8.8.4.0/24',    'accept', 0)`)
}

func b341AssertCounts(t *testing.T, d *sql.DB, label string) {
	t.Helper()
	got, err := OwnerTagCountsForDeviceRules(d, 1, "cyborg")
	if err != nil {
		t.Fatalf("%s: OwnerTagCountsForDeviceRules: %v", label, err)
	}
	if len(got) != 2 {
		t.Fatalf("%s: owners = %v, want exactly emilia and karolina (the disabled rule and the unowned prefix must not count)", label, got)
	}
	if got["emilia"] != 2 {
		t.Errorf("%s: emilia count = %d, want 2 (8.8.8.0/24 + 74.125.0.0/16)", label, got["emilia"])
	}
	if got["karolina"] != 1 {
		t.Errorf("%s: karolina count = %d, want 1 (1.1.1.0/24, matched through node_owner_map because the rule keeps a stale hostname)", label, got["karolina"])
	}
	// The domain row ("youtube.com") is not in prefix_owner and must not appear.
	if _, bad := got["youtube.com"]; bad {
		t.Errorf("%s: the domain row leaked into the owner counts — the ACL pins exist per resolved CIDR, not per domain", label)
	}
}

// TestOwnerTagCountsForDeviceRules_SQLite_B341 always runs.
func TestOwnerTagCountsForDeviceRules_SQLite_B341(t *testing.T) {
	d := b341SQLiteDB(t)
	seedB341(t, d)
	b341AssertCounts(t, d, "sqlite")

	// A device with no rules at all is an empty map, not an error: the caller
	// must be able to tell "no rules" from "query failed".
	none, err := OwnerTagCountsForDeviceRules(d, 1, "nosuchdevice")
	if err != nil {
		t.Fatalf("sqlite: device without rules: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("sqlite: device without rules returned %v, want an empty map", none)
	}
}

// TestOwnerTagCountsForDeviceRules_Postgres_B341 runs whenever a test DSN is
// configured (SKIPs otherwise — never FAILs, AGENTS rule 1). The query uses `$N`
// placeholders and CAST(device_id AS TEXT) precisely so both backends answer the
// same way; the SQLite half above cannot prove the PostgreSQL half (B331/L-45).
func TestOwnerTagCountsForDeviceRules_Postgres_B341(t *testing.T) {
	d := openTestDB(t) // OpenTestPG — SKIPs without SKYGATE_TEST_PG_DSN
	// The schema is shared per test database, so clean the three tables this test
	// writes before seeding (a sibling test may have left rows behind).
	for _, q := range []string{
		`DELETE FROM device_rules WHERE user_id = 1`,
		`DELETE FROM prefix_owner WHERE prefix IN ('8.8.8.0/24','74.125.0.0/16','1.1.1.0/24')`,
		`DELETE FROM node_owner_map WHERE hostname IN ('cyborg','emilia','skyworker')`,
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("cleanup %q: %v", q, err)
		}
	}
	seedB341(t, d)
	b341AssertCounts(t, d, "postgres")
}
