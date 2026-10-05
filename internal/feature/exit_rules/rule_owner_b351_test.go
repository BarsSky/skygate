// rule_owner_b351_test.go — B351 (2026-10-05).
//
// The operator's suspicion was «нет ли обновлятора сервиса что переписывает в
// неправильном ключе» — and there was: the domain auto-updater's two raw INSERTs
// never wrote `user_name`, and `DeviceRuleCountsForAdmin` grouped by that column,
// so the /admin/exit-rules device index listed ONE device TWICE (once under
// `michail`, once under ""). These tests pin both halves: the lookup the INSERTs
// now use, and the inventory that no longer depends on the denormalised column.
package exit_rules

import (
	"testing"

	skygatedb "skygate/internal/db"
)

func TestDeviceRuleCountsForAdmin_MergesBlankOwner_B351(t *testing.T) {
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b351idx.db")
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
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES
	          (1, 'skyadmin', 'x', 1), (6, 'michail', 'x', 0)`)
	// THE LIVE SHAPE: one device (`basic`), three enabled rules, and the owner column
	// empty on two of them (what the auto-updater produced before B351). The index
	// must report ONE line with the total, not two lines with a split count.
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (6, 29, 'basic', '',         'karolina', 'subnet', '104.16.0.0/12', 'accept', 1),
	          (6, 29, 'basic', '',         'karolina', 'subnet', '172.64.0.0/13', 'accept', 1),
	          (6, 29, 'basic', 'michail',  'karolina', 'domain', 'discord.com',   'accept', 1),
	          (6, 30, 'phone', '',         'karolina', 'domain', 'youtube.com',   'accept', 0)`)
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled) VALUES
	          (1, 9, 'skyworker', '', 'emilia', 'subnet', '8.8.8.0/24', 'accept', 1)`)

	rows, err := DeviceRuleCountsForAdmin(d)
	if err != nil {
		t.Fatalf("DeviceRuleCountsForAdmin: %v", err)
	}
	// Two devices have enabled rules (basic, skyworker); the disabled `phone` row is
	// not a device with rules.
	if len(rows) != 2 {
		t.Fatalf("inventory has %d line(s), want 2 — one per device:\n%+v", len(rows), rows)
	}
	byHost := map[string]DeviceRuleCount{}
	for _, r := range rows {
		byHost[r.Hostname] = r
	}
	if got := byHost["basic"]; got.Rules != 3 || got.UserName != "michail" {
		t.Errorf("basic = %+v, want {UserName:michail Rules:3} — the blank owner must not split the device", got)
	}
	if got := byHost["skyworker"]; got.Rules != 1 || got.UserName != "skyadmin" {
		t.Errorf("skyworker = %+v, want {UserName:skyadmin Rules:1}", got)
	}
}

func TestRuleOwnerLookup_B351(t *testing.T) {
	_, d, err := skygatedb.OpenWithDialect("sqlite:" + t.TempDir() + "/b351owner.db")
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
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin) VALUES (6, 'michail', 'x', 0)`)
	// node_owner_map.node_id is TEXT while device_rules.device_id is an INTEGER —
	// the CAST in the query is what makes the join work on both dialects.
	mustExec(`INSERT INTO node_owner_map (node_id, hostname, username, tag) VALUES ('29', 'basic', 'michail', 'tag:dev-michail-basic')`)

	l := newRuleOwnerLookup(d)
	name, host := l.get(6, 29)
	if name != "michail" || host != "basic" {
		t.Fatalf("get(6,29) = (%q,%q), want (michail,basic)", name, host)
	}
	// The two halves resolve INDEPENDENTLY: the username comes from portal_users by
	// user_id (so an unknown device still gets it), while the hostname needs
	// node_owner_map. An unknown pair must not fail the tick — the ACL falls back to
	// node_owner_map by device_id (B265) and the tick's backfill repairs the row on
	// the next pass.
	if name, host := l.get(6, 999); name != "michail" || host != "" {
		t.Errorf("get(6,999) = (%q,%q), want (michail,empty) — the username does not depend on the device", name, host)
	}
	// Cached: the second call must not need the table again (one query per unique
	// pair per pass — the whole reason the lookup exists).
	mustExec(`DELETE FROM node_owner_map`)
	if name, host := l.get(6, 29); name != "michail" || host != "basic" {
		t.Errorf("get(6,29) after the row was deleted = (%q,%q) — the answer is not cached", name, host)
	}
}
