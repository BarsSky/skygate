// inert_rules_b343_test.go — B343 (2026-10-02).
//
// The marker must be exact in BOTH directions: a device with enabled rules and
// no preference is named (that is the "правило есть, а доступа нет" state), and
// nothing else is — a device WITH a preference, a device whose only rules are
// disabled, and the pre-rename rows with an empty device_hostname must never
// appear, or the banner becomes noise the operator learns to ignore.
package exit_rules

import (
	"testing"

	skygatedb "skygate/internal/db"
)

func TestDevicesWithoutExitNodePref_B343(t *testing.T) {
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b343.db")
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
	// cyborg: 3 enabled rules, NO preference → the marker.
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '8.8.8.0/24',  'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'subnet', '8.8.4.0/24',  'accept', 1),
	          (1, 56, 'cyborg', 'skyadmin', '', 'domain', 'youtube.com', 'accept', 1)`)
	// basic: has a preference → must NOT be reported.
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (1, 7, 'basic', 'skyadmin', '', 'subnet', '1.1.1.0/24', 'accept', 1)`)
	mustExec(`INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled) VALUES
	          (1, 'basic', 'tag:dev-infra-emilia', 0, 0, 1)`)
	// tablet: its only rule is DISABLED → must NOT be reported.
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (1, 8, 'tablet', 'skyadmin', '', 'subnet', '9.9.9.0/24', 'accept', 0)`)
	// pre-rename leftover: no hostname → must NOT be reported (the reconciler finds
	// it through node_owner_map, and naming it would send the operator hunting).
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (1, 56, '', 'skyadmin', '', 'subnet', '142.250.0.0/15', 'accept', 1)`)

	got, err := DevicesWithoutExitNodePref(d, 1)
	if err != nil {
		t.Fatalf("DevicesWithoutExitNodePref: %v", err)
	}
	if len(got) != 1 || got["cyborg"] != 3 {
		t.Fatalf("got %v, want exactly {cyborg: 3} (basic has a preference, tablet's rule is disabled, the empty hostname is a pre-rename row)", got)
	}
	labels := inertDeviceLabels(got)
	if len(labels) != 1 || labels[0] != "cyborg (3)" {
		t.Fatalf("labels = %v, want [cyborg (3)] — the banner renders these verbatim", labels)
	}

	// A user with nothing to report must yield an EMPTY map, not nil-with-error:
	// the page then renders no banner at all.
	none, err := DevicesWithoutExitNodePref(d, 999)
	if err != nil {
		t.Fatalf("user without rules: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("user without rules returned %v, want an empty map", none)
	}
	if labels := inertDeviceLabels(none); len(labels) != 0 {
		t.Errorf("empty map produced labels %v", labels)
	}
}
