// rule_limits_b328_test.go — B328: the rule caps must count what they say they count.
//
// The operator report this pins (measured on the live host 2026-09-25):
//
//	/my/exit-rules showed «cyborg (1/500)» in the device picker and refused the save
//	with «device limit exceeded: 500/500 user-facing rules on this device».
//
// Both numbers were real, and they came from different queries: the page used the
// correct per-(user,device) count (1), while the guard used
// countUserFacing(0, devID, false) — and the closure behind it had no "device without
// user" case, so it read the SYSTEM-WIDE count (500). The first test below builds
// exactly that shape and asserts the insert is ALLOWED; against the pre-B328 ladder it
// reproduces the operator's own message.
package exit_rules

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	skygatedb "skygate/internal/db"
)

// newB328DB returns a migrated SQLite database, skipping when the SQLite dialect is
// unavailable in this build (same guard as the B276.1 fixture).
func newB328DB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b328.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable in this build: %v", err)
	}
	if err := skygatedb.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func b328Exec(t *testing.T, d *sql.DB, q string, args ...interface{}) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("seed %q: %v", q, err)
	}
}

// seedB328LiveShape reproduces the operator's database shape: the target device holds
// ONE counted (user-facing) rule, inside a system that holds `systemWide` enabled rows
// — the other rows are derived subnet ranges, exactly like the live host where
// 263 + 236 + 1 = 500 rows sat across three devices and only 46 counted.
func seedB328LiveShape(t *testing.T, d *sql.DB, targetDevice, systemWide int) {
	t.Helper()
	b328Exec(t, d, `INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	                VALUES (1, 'skyadmin', 'x', 1, 1)`)
	b328Exec(t, d, `INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	                VALUES (2, 'other', 'x', 0, 2)`)
	// The one row that counts, on the target device.
	b328Exec(t, d, `INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, all_devices)
	                VALUES (1, ?, 'skyadmin', 'karolina', 'domain', 'youtube.com', 'accept', 1, 0)`, targetDevice)
	// The bulk: derived subnet ranges of another user's device — enabled, but not
	// user-facing. These are what made the system total reach the per-device cap.
	// The natural key (user, device, exit_node, type, value, parent_domain) is UNIQUE,
	// so the value must differ on every row; 198.18.x.y/24 is the RFC 2544 benchmark
	// range and does not repeat for i < 65536.
	for i := 1; i < systemWide; i++ {
		b328Exec(t, d, `INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, all_devices, parent_domain)
		                VALUES (2, 900, 'other', 'emilia', 'subnet', ?, 'accept', 1, 0, 'cdn:example.com')`,
			fmt.Sprintf("198.18.%d.%d/24", i/256, i%256))
	}
}

// TestB328_PerDeviceCapMustNotReadTheSystemTotal is THE regression: 500 enabled rows
// system-wide, 1 counted row on the device, per-device cap 500 → the insert must be
// allowed. Pre-B328 the ladder answered
// «device limit exceeded: 500/500 user-facing rules on this device».
func TestB328_PerDeviceCapMustNotReadTheSystemTotal(t *testing.T) {
	d := newB328DB(t)
	const targetDevice = 56
	seedB328LiveShape(t, d, targetDevice, 500)

	systemTotal := countAllEnabledRules(d)
	deviceFacing := countUserFacingForUserDevice(d, 1, targetDevice)
	if systemTotal != 500 {
		t.Fatalf("fixture system total = %d, want 500", systemTotal)
	}
	if deviceFacing != 1 {
		t.Fatalf("fixture device-facing count = %d, want 1 (the number the page shows)", deviceFacing)
	}

	limits := ruleLimits{MaxPerDevice: 500}
	counts := measureRuleLimits(d, limits, 1, targetDevice, "skyadmin")
	if counts.PerDevice != 1 {
		t.Errorf("measureRuleLimits.PerDevice = %d, want 1 — the per-device count read something else", counts.PerDevice)
	}
	if reason := limits.ExceedReason(counts); reason != "" {
		t.Fatalf("the insert was refused: %q\n  the per-device cap is 500 and this device has 1 user-facing rule (the system as a whole has %d) — this is exactly the operator report",
			reason, systemTotal)
	}

	// And document what the bug DID: feeding the system total in as the per-device
	// count produces the operator's message verbatim. If someone ever wires the system
	// count back into the device slot, this is the sentence the operator sees again.
	buggy := ruleLimitCounts{Username: "skyadmin", PerDevice: systemTotal}
	if reason := limits.ExceedReason(buggy); !strings.Contains(reason, "device limit exceeded: 500/500 user-facing rules on this device") {
		t.Errorf("the pre-B328 shape no longer reproduces the operator message: %q", reason)
	}
}

// TestB328_LevelsStillFireAtTheirOwnBoundary: tightening the fix must not disable the
// caps. Each level fails on its own count and names itself.
func TestB328_LevelsStillFireAtTheirOwnBoundary(t *testing.T) {
	cases := []struct {
		name   string
		limits ruleLimits
		counts ruleLimitCounts
		want   string // substring of the reason; "" = allowed
	}{
		{"device at cap", ruleLimits{MaxPerDevice: 500}, ruleLimitCounts{PerDevice: 500}, "device limit exceeded: 500/500"},
		{"device one below cap", ruleLimits{MaxPerDevice: 500}, ruleLimitCounts{PerDevice: 499}, ""},
		{"user at cap", ruleLimits{MaxPerUser: 2000, MaxPerDevice: 500}, ruleLimitCounts{Username: "skyadmin", PerUser: 2000, PerDevice: 1}, "user limit exceeded: 2000/2000 rules for user skyadmin"},
		{"total at cap", ruleLimits{MaxTotal: 10000}, ruleLimitCounts{Total: 10000}, "system limit exceeded: 10000/10000"},
		{"no caps configured", ruleLimits{}, ruleLimitCounts{PerDevice: 9999, PerUser: 9999, Total: 9999}, ""},
		{"device level reports before the total", ruleLimits{MaxPerDevice: 10, MaxTotal: 10}, ruleLimitCounts{PerDevice: 10, Total: 10}, "device limit exceeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.limits.ExceedReason(tc.counts)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("ExceedReason = %q, want allowed", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("ExceedReason = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// TestB328_MeasureOnlyQueriesTheEnabledLevels: a level that is switched off must not
// cost a query, and a level that is on must be measured from its OWN identity — never
// from a neighbouring one.
func TestB328_MeasureOnlyQueriesTheEnabledLevels(t *testing.T) {
	d := newB328DB(t)
	seedB328LiveShape(t, d, 56, 4)

	none := measureRuleLimits(d, ruleLimits{}, 1, 56, "skyadmin")
	if none != (ruleLimitCounts{Username: "skyadmin"}) {
		t.Errorf("with no caps the counts must all be zero, got %+v", none)
	}

	onlyDevice := measureRuleLimits(d, ruleLimits{MaxPerDevice: 500}, 1, 56, "skyadmin")
	if onlyDevice.PerDevice != 1 || onlyDevice.PerUser != 0 || onlyDevice.Total != 0 {
		t.Errorf("the per-device level filled the wrong fields: %+v (want PerDevice=1, PerUser=0, Total=0)", onlyDevice)
	}

	onlyUser := measureRuleLimits(d, ruleLimits{MaxPerUser: 2000}, 1, 56, "skyadmin")
	if onlyUser.PerUser != 1 || onlyUser.PerDevice != 0 || onlyUser.Total != 0 {
		t.Errorf("the per-user level filled the wrong fields: %+v (want PerUser=1, PerDevice=0, Total=0)", onlyUser)
	}

	onlyTotal := measureRuleLimits(d, ruleLimits{MaxTotal: 10000}, 1, 56, "skyadmin")
	if onlyTotal.Total != 4 || onlyTotal.PerUser != 0 || onlyTotal.PerDevice != 0 {
		t.Errorf("the total level filled the wrong fields: %+v (want Total=4, PerUser=0, PerDevice=0)", onlyTotal)
	}
}

// TestB328_QuotaUnitMatchesTheLiveComposition pins the UNIT of measure against the
// composition measured on the operator's host, so a future "tidy-up" of the SQL cannot
// silently start charging the operator for the range bulk skygate creates on their
// behalf:
//
//	subnet + parent_domain set  → excluded (the expanded CIDR ranges)
//	subnet, no parent_domain    → counts   (hand-entered)
//	domain + parent_domain set  → counts   (the kept per-domain row, B298)
//	domain, no parent_domain    → counts   (hand-entered)
func TestB328_QuotaUnitMatchesTheLiveComposition(t *testing.T) {
	d := newB328DB(t)
	seedB328LiveShape(t, d, 56, 1) // one counted row on device 56
	ins := func(targetType, parentDomain string) {
		b328Exec(t, d, `INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, all_devices, parent_domain)
		                VALUES (1, 56, 'skyadmin', 'karolina', ?, '203.0.113.99', 'accept', 1, 0, ?)`,
			targetType, parentDomain)
	}
	ins("subnet", "discord.com")     // excluded
	ins("subnet", "cdn:ghcr.io")     // excluded
	ins("subnet", "")                // counts (hand-entered)
	ins("domain", "cdn:discord.com") // counts (the kept per-domain row)

	if got := countUserFacingForUserDevice(d, 1, 56); got != 3 {
		t.Errorf("counted rows on device 56 = %d, want 3 (1 seeded + the hand-entered subnet + the kept per-domain row; the 2 expanded subnet ranges must not count)", got)
	}
	if got := countAllEnabledRules(d); got != 5 {
		t.Errorf("system-wide enabled count = %d, want 5 — this is the number the pre-B328 guard misused as a per-device count", got)
	}
}
