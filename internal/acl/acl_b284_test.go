// internal/acl/acl_b284_test.go — B284 (2026-09-22).
//
// THE LIVE INCIDENT, second half. `deviceTagForRule` used to PREFER the
// denormalised `device_rules.user_name` and synthesise
// `tag:dev-<user>-<host>` from it. On the `aro` host that column held
// headscale's synthetic owner for tagged nodes, `tagged-devices`, so the
// generated policy referenced
//
//	tag:dev-tagged-devices-exit-node-vps
//	tag:dev-tagged-devices-workpc
//
// — tags that exist on no node and in no `tagOwners` block. headscale rejects a
// policy that references an undeclared tag AS A WHOLE ("tag not found"), and on
// a `policy.mode: file` host that means the daemon will not start on it:
// crash-loop counter 248, control plane down, every device gone from the portal.
//
// Proof from the live host (`python3` over the saved snapshots):
//
//	/tmp/policy.297.json undeclared: []                                  # working
//	/tmp/snap.304.json   undeclared: ['tag:dev-tagged-devices-exit-node-vps',
//	                                  'tag:dev-tagged-devices-workpc']   # refused
//
// The tag must come from the node: node_owner_map.tag is what headscale carries
// and what this generator declares, so a grant using it can never be undeclared.
package acl

import (
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// b284SeedDB mirrors the live rows: the device_rules denormalised owner is
// headscale's synthetic `tagged-devices`, while node_owner_map knows the real
// tag the node carries.
func b284SeedDB(t *testing.T) *sql.DB {
	t.Helper()
	t.Setenv("SKYGATE_BASE_DOMAIN", "ts.example.com")
	t.Setenv("SKYGATE_ADMIN_USER", "admin")
	t.Setenv("SKYGATE_ACL_VIA_ENABLED", "true")

	_, sqlDB, err := db.OpenWithDialect("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("OpenWithDialect: %v", err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.ApplyMigrations(sqlDB, db.DialectSQLite); err != nil {
		t.Fatalf("ApplyMigrations: %v", err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := sqlDB.Exec(q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO portal_users (username, password_hash, is_admin, headscale_user_id)
	          VALUES ('infra', 'x', 0, 3) ON CONFLICT(username) DO UPDATE SET headscale_user_id = 3`)
	mustExec(`INSERT INTO portal_users (username, password_hash, is_admin, headscale_user_id)
	          VALUES ('daniil', 'x', 1, 1) ON CONFLICT(username) DO UPDATE SET headscale_user_id = 1`)
	var daniilID int64
	if err := sqlDB.QueryRow(`SELECT id FROM portal_users WHERE username = 'daniil'`).Scan(&daniilID); err != nil {
		t.Fatalf("read daniil id: %v", err)
	}
	// node_owner_map: the real tags the nodes carry. The relay's row is owned by
	// the synthetic `tagged-devices`, exactly like the live host.
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname)
	          VALUES ('1', 0, 'tagged-devices', 'tag:dev-infra-exit-node-vps', 'exit-node-vps'),
	                 ('2', 1, 'daniil', 'tag:dev-daniil-workpc', 'workpc')`)
	// device_rules: the denormalised owner is the SYNTHETIC name — this is what
	// the pre-fix generator turned into `tag:dev-tagged-devices-workpc`.
	mustExec(`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value,
	                                   action, enabled, user_name, device_hostname)
	          VALUES ($1, 2, 'exit-node-vps', 'subnet', '104.16.0.0/12', 'accept', 1, 'tagged-devices', 'workpc')`, daniilID)
	mustExec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices)
	          VALUES ('104.16.0.0/12', 'exit-node-vps', 'explicit', 1, 1)`)
	return sqlDB
}

// TestDeviceTagComesFromTheNodeB284 is the regression for the refused snapshot:
// the grant's src must be the tag the node carries, never a tag invented from a
// synthetic headscale user name.
func TestDeviceTagComesFromTheNodeB284(t *testing.T) {
	sqlDB := b284SeedDB(t)
	gen, err := GenerateACLLiveFormat(sqlDB)
	if err != nil {
		t.Fatalf("GenerateACLLiveFormat: %v", err)
	}

	if strings.Contains(gen, "tag:dev-tagged-devices-") {
		t.Errorf("the policy references a tag invented from headscale's synthetic owner "+
			"(`tag:dev-tagged-devices-…`) — no node carries it and no tagOwners block declares it, "+
			"so headscale refuses the WHOLE policy and will not start on it:\n%s", gen)
	}
	if !strings.Contains(gen, `"src": ["tag:dev-daniil-workpc"]`) {
		t.Errorf("the subnet grant does not use the tag the node actually carries "+
			"(node_owner_map.tag = tag:dev-daniil-workpc):\n%s", gen)
	}
	undeclared, err := headscale.UndeclaredTags(gen)
	if err != nil {
		t.Fatalf("UndeclaredTags(gen): %v\n%s", err, gen)
	}
	if len(undeclared) > 0 {
		t.Errorf("generated policy references undeclared tag(s): %s\n%s",
			strings.Join(undeclared, ", "), gen)
	}
}

// TestDeviceTagForRuleRefusesToInventTagsB284 pins the pure helper: with no
// node_owner_map tag there is NO tag to return (the caller falls back to the
// device-IP selector), because an invented tag cannot be declared anywhere.
func TestDeviceTagForRuleRefusesToInventTagsB284(t *testing.T) {
	entry := db.ACLEntry{DeviceID: 7, UserName: "tagged-devices", DeviceHostname: "workpc", DeviceIP: "100.64.0.9"}

	if got := deviceTagForRule(entry, nil); got != "" {
		t.Errorf("deviceTagForRule without a node_owner_map row = %q, want \"\" "+
			"(an invented tag is undeclared by construction and kills the whole policy)", got)
	}
	if got := deviceTagForRule(entry, map[int]deviceOwner{7: {Username: "tagged-devices", Hostname: "workpc"}}); got != "" {
		t.Errorf("deviceTagForRule with an EMPTY node_owner_map tag = %q, want \"\"", got)
	}
	want := "tag:dev-daniil-workpc"
	if got := deviceTagForRule(entry, map[int]deviceOwner{7: {Username: "daniil", Hostname: "workpc", Tag: want}}); got != want {
		t.Errorf("deviceTagForRule = %q, want %q (the tag the node carries)", got, want)
	}
}
