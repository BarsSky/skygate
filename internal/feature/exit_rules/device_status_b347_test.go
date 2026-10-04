// device_status_b347_test.go — B347 (2026-10-04).
//
// The strip answers ONE question per device: "does it have access, and through
// which relay?" — and it must answer it for EVERY device with enabled rules,
// from the whole table, because the page that used to carry this information
// (the B343 badge) is inside a paginated group list. Measured live: page 1 of 5
// held only skyworker's rows, so cyborg had no marker at all.
//
// The assertions are therefore about COVERAGE and EXACTNESS:
//   - a pinned device reports its relay WITHOUT the `tag:` prefix (that is what
//     the operator types into `tailscale --exit-node=`);
//   - an unpinned device is present with an empty relay (the red marker);
//   - a disabled-only device and the pre-rename empty-hostname rows are absent;
//   - the count is the number of ENABLED rules for that device, not a page.
package exit_rules

import (
	"testing"

	skygatedb "skygate/internal/db"
)

func TestDeviceStatusRows_B347(t *testing.T) {
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b347.db")
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
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES (1, 'skyadmin', 'x', 0) ON CONFLICT (id) DO NOTHING`)
	// cyborg: 11 enabled rules, pinned to emilia (the live shape after the B345
	// repair) → must appear WITH the relay.
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (1, 56, 'cyborg', 'skyadmin', '', 'domain', 'youtube.com',   'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '142.250.0.0/15','accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '172.217.0.0/16','accept', 1)`)
	mustExec(`INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled) VALUES
	          (1, 'cyborg', 'tag:dev-infra-emilia', 0, 0, 1)`)
	// laptop: enabled rules, NO preference → must appear with an EMPTY relay (the
	// state that used to be invisible).
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (1, 9, 'laptop', 'skyadmin', '', 'subnet', '9.9.9.0/24', 'accept', 1)`)
	// tablet: only a DISABLED rule → must not appear at all.
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (1, 8, 'tablet', 'skyadmin', '', 'subnet', '8.8.8.0/24', 'accept', 0)`)
	// pre-rename leftover: no hostname → must not appear (the reconciler finds it
	// through node_owner_map; naming it would send the operator hunting).
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (1, 56, '', 'skyadmin', '', 'subnet', '74.125.0.0/16', 'accept', 1)`)

	got, err := DeviceStatusRows(d, 1)
	if err != nil {
		t.Fatalf("DeviceStatusRows: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d rows (%+v), want exactly 2 (cyborg and laptop; the disabled-only tablet and the empty hostname are excluded)", len(got), got)
	}
	// ORDER BY device_hostname → cyborg, laptop.
	if got[0].Hostname != "cyborg" || got[0].Rules != 3 || got[0].Relay != "dev-infra-emilia" || !got[0].Pinned {
		t.Errorf("row 0 = %+v, want {cyborg 3 dev-infra-emilia pinned} — the relay is shown without the tag: prefix because that is what --exit-node= takes", got[0])
	}
	if got[1].Hostname != "laptop" || got[1].Rules != 1 || got[1].Relay != "" || got[1].Pinned {
		t.Errorf("row 1 = %+v, want {laptop 1 \"\" unpinned} — an unpinned device must still be LISTED, that is the red marker", got[1])
	}

	// A user with no rules gets an empty slice (not nil-with-error): the template
	// then renders no strip at all.
	none, err := DeviceStatusRows(d, 999)
	if err != nil {
		t.Fatalf("user without rules: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("user without rules returned %+v, want empty", none)
	}
}

// TestAssignedRelayFor_B347 pins the post-save flash's source: the relay of the
// device that was just saved, "" when there is none (the flash then stays
// silent and the strip's red marker carries the message), and "" for an empty
// hostname rather than a query that could match a pre-rename row.
func TestAssignedRelayFor_B347(t *testing.T) {
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b347b.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := skygatedb.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	// device_exit_node_prefs.user_id FKs portal_users, so the owner row comes
	// first (the B343 fixture does the same).
	if _, err := d.Exec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES (1, 'skyadmin', 'x', 0) ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed portal user: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled) VALUES
	                     (1, 'cyborg', 'tag:dev-infra-emilia', 0, 0, 1),
	                     (1, 'skyworker', 'tag:dev-infra-karolina', 0, 0, 1)`); err != nil {
		t.Fatalf("seed prefs: %v", err)
	}
	for _, tc := range []struct{ host, want string }{
		{"cyborg", "dev-infra-emilia"},
		{"skyworker", "dev-infra-karolina"},
		{"laptop", ""},
		{"", ""},
		{"   ", ""},
	} {
		if got := AssignedRelayFor(d, 1, tc.host); got != tc.want {
			t.Errorf("AssignedRelayFor(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
	// Another user's preference must not leak into the flash.
	if got := AssignedRelayFor(d, 2, "cyborg"); got != "" {
		t.Errorf("AssignedRelayFor(user 2) = %q, want \"\" (the preference is per-user)", got)
	}
}
