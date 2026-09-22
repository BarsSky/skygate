// reconciler_rename_rules_b277_5_test.go — v1.5.45 —
// regression guard for the device_rules rename propagation.
//
// The live bug: an operator renamed headscale node "node" →
// "exit-node-vps". B231 (the pref-migrator) ran and migrated
// device_exit_node_prefs.device_hostname + exit_node_tag to the
// new name. But device_rules.exit_node_id and
// device_rules.device_hostname kept their OLD values — the ACL
// builder continued to emit `via: tag:dev-infra-node`, which
// didn't match the new node's tag. Tailscale silently ignored
// the via= pin and traffic went through DERP / default.
//
// Fix: applyRenameMigration now also UPDATE-s every
// device_rules row for the user where exit_node_id OR
// device_hostname = oldHost. The test below exercises the
// SQL contract end-to-end against a live sqlite (MigrateSQLite).
package exit_rules

import (
	"context"
	"database/sql"
	"testing"

	skygatedb "skygate/internal/db"
)

// newRenameRulesDB opens an in-memory sqlite, runs the v0.75
// migration set, and returns the handle. Mirrors the pattern
// from all_devices_b276_1_test.go.
func newRenameRulesDB(t *testing.T) *sql.DB {
	t.Helper()
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b277_5.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable in this build: %v", err)
	}
	if err := skygatedb.MigrateSQLite(d); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// seedRenameRules wires one user + two exit-node rows in
// node_owner_map (mirrors a real rename) + four rules with
// exit_node_id="node" (the old host).
func seedRenameRules(t *testing.T, d *sql.DB) {
	t.Helper()
	exec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	exec(`INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	      VALUES (1, 'skyadmin', 'x', 1, 1)`)
	// Old host "node" still in node_owner_map with the OLD tag
	// (B111-era); migration runs because (user, "node") is NOT
	// in node_owner_map after the rename (only "exit-node-vps"
	// is). The classier then looks up rows with tag =
	// exit_node_tag and finds "exit-node-vps" — that's the
	// rename candidate.
	exec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname)
	      VALUES ('5', 1, 'skyadmin', 'tag:dev-infra-exit-node-vps', 1, 0, 'exit-node-vps')`)
	exec(`INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, device_hostname, device_ip, all_devices)
	      VALUES (1, 1, 'skyadmin', 'node', 'ip', '104.16.0.0/12', 'accept', 1, 'node', '100.64.0.1', 0)`)
	exec(`INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, device_hostname, device_ip, all_devices)
	      VALUES (1, 1, 'skyadmin', 'node', 'domain', 'example.com', 'accept', 1, 'node', '100.64.0.1', 0)`)
	exec(`INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, device_hostname, device_ip, all_devices)
	      VALUES (1, 2, 'skyadmin', 'node', 'subnet', '8.8.4.0/24', 'accept', 1, 'node', '100.64.0.2', 0)`)
	// One DISABLED rule — must NOT be touched (the rename is
	// about live rules; disabled rules aren't part of the ACL).
	exec(`INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, device_hostname, device_ip, all_devices)
	      VALUES (1, 1, 'skyadmin', 'node', 'ip', '9.9.9.0/24', 'accept', 0, 'node', '100.64.0.1', 0)`)
	// One rule for ANOTHER user — must NOT be touched.
	exec(`INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	      VALUES (2, 'michail', 'x', 0, 2)`)
	exec(`INSERT INTO device_rules (user_id, device_id, user_name, exit_node_id, target_type, target_value, action, enabled, device_hostname, device_ip, all_devices)
	      VALUES (2, 9, 'michail', 'node', 'ip', '1.1.1.0/24', 'accept', 1, 'node', '100.64.0.9', 0)`)
	// The pref row B231 will migrate (sets up the test).
	exec(`INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled)
	      VALUES (1, 'node', 'tag:exit-node', 0, 0, 1)`)
}

// TestApplyRenameMigration_PropagatesToDeviceRules pins the
// v1.5.45 fix: when B231 migrates a pref from "node" →
// "exit-node-vps", every rule for that user with
// exit_node_id="node" or device_hostname="node" gets the new
// name. Disabled rules + other users' rules are untouched.
func TestApplyRenameMigration_PropagatesToDeviceRules(t *testing.T) {
	d := newRenameRulesDB(t)
	seedRenameRules(t, d)

	s := &Service{DB: skygatedb.FixedDBSource{DB: d}}
	rulesAffected, err := s.applyRenameMigration(context.Background(),
		1, "node", "exit-node-vps", "tag:dev-infra-exit-node-vps")
	if err != nil {
		t.Fatalf("applyRenameMigration: %v", err)
	}
	if rulesAffected != 3 {
		t.Errorf("rulesAffected = %d, want 3 (the 3 live rules for user 1; disabled rule + user 2's rule must NOT be touched)", rulesAffected)
	}

	// Live rules for user 1 with exit_node_id="node" → flipped
	// to "exit-node-vps". device_hostname follows.
	rows, err := d.Query(`
		SELECT target_value, exit_node_id, device_hostname, enabled
		  FROM device_rules
		 WHERE user_id = 1 AND target_value IN ('104.16.0.0/12','example.com','8.8.4.0/24')
		 ORDER BY target_value`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	want := map[string]struct {
		ExitNode     string
		DeviceHost   string
		Enabled      int
	}{
		"104.16.0.0/12": {"exit-node-vps", "exit-node-vps", 1},
		"example.com":  {"exit-node-vps", "exit-node-vps", 1},
		"8.8.4.0/24":    {"exit-node-vps", "exit-node-vps", 1},
	}
	for rows.Next() {
		var tv, en, dh string
		var enabled int
		if err := rows.Scan(&tv, &en, &dh, &enabled); err != nil {
			t.Fatalf("scan: %v", err)
		}
		w := want[tv]
		if en != w.ExitNode {
			t.Errorf("%s: exit_node_id = %q, want %q", tv, en, w.ExitNode)
		}
		if dh != w.DeviceHost {
			t.Errorf("%s: device_hostname = %q, want %q", tv, dh, w.DeviceHost)
		}
	}

	// Disabled rule for user 1 — must NOT be touched
	var disExitNode string
	if err := d.QueryRow(`SELECT exit_node_id FROM device_rules
		WHERE user_id = 1 AND target_value = '9.9.9.0/24'`).Scan(&disExitNode); err != nil {
		t.Fatalf("disabled scan: %v", err)
	}
	if disExitNode != "node" {
		t.Errorf("disabled rule should remain stale, got exit_node_id=%q", disExitNode)
	}

	// Rule for user 2 (michail) — must NOT be touched.
	var otherExitNode string
	if err := d.QueryRow(`SELECT exit_node_id FROM device_rules
		WHERE user_id = 2 AND target_value = '1.1.1.0/24'`).Scan(&otherExitNode); err != nil {
		t.Fatalf("other scan: %v", err)
	}
	if otherExitNode != "node" {
		t.Errorf("other-user rule should remain stale, got exit_node_id=%q", otherExitNode)
	}

	// Pref row is migrated too (existing B231 contract).
	var prefNewTag string
	if err := d.QueryRow(`SELECT exit_node_tag FROM device_exit_node_prefs
		WHERE user_id = 1 AND device_hostname = 'exit-node-vps'`).Scan(&prefNewTag); err != nil {
		t.Fatalf("pref scan: %v", err)
	}
	if prefNewTag != "tag:dev-infra-exit-node-vps" {
		t.Errorf("pref migrated to %q, want tag:dev-infra-exit-node-vps", prefNewTag)
	}
}
