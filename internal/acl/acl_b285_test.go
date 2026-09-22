// internal/acl/acl_b285_test.go — B285 (2026-09-22).
//
// THE THIRD LAYER OF THE SAME INCIDENT. headscale rejects a policy that
// references a tag its tagOwners does not declare — as a whole, and on a
// `policy.mode: file` host that means the daemon will not START. v1.5.48 made
// skygate refuse such a document instead of writing it (B283), v1.5.49 made the
// device tag come from the node (B284) — and that exposed this gap:
//
//	acl-drift: re-apply FAILED (refusing to set a policy that references 1
//	tag(s) missing from tagOwners: tag:dev-daniil-workpc …) — the live policy
//	stays stale; the next pass retries
//
// The grants take their tag from node_owner_map (B284), while the tagOwners
// block derives its entries from the per-user/per-device sources — and the two
// use DIFFERENT columns. Live on `aro` both node_owner_map rows were owned by
// headscale's synthetic `tagged-devices` (not a portal account), so the
// per-user block emitted nothing while the grants named
// `tag:dev-daniil-workpc` and `tag:dev-infra-exit-node-vps`. The refusal was
// correct (headscale would have rejected the document anyway), but the ACL
// could never apply — so the generator now sweeps every tag its grants name
// into tagOwners.
package acl

import (
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// b285SeedDB mirrors the live rows: both nodes carry a real tag in
// node_owner_map, and BOTH rows are owned by headscale's synthetic
// `tagged-devices` — which is not a portal user, so `GetPerUserDeviceTags`
// (the per-user tagOwners source) cannot see either tag.
func b285SeedDB(t *testing.T) *sql.DB {
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
	          VALUES ('daniil', 'x', 1, 1) ON CONFLICT(username) DO UPDATE SET headscale_user_id = 1`)
	var daniilID int64
	if err := sqlDB.QueryRow(`SELECT id FROM portal_users WHERE username = 'daniil'`).Scan(&daniilID); err != nil {
		t.Fatalf("read daniil id: %v", err)
	}
	// Both rows owned by the synthetic user, both carrying the real tag.
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname)
	          VALUES ('1', 0, 'tagged-devices', 'tag:dev-infra-exit-node-vps', 'exit-node-vps'),
	                 ('2', 0, 'tagged-devices', 'tag:dev-daniil-workpc', 'workpc')`)
	// A per-CIDR rule for the CLIENT device (no prefix ownership of its own, so
	// the B283 owner-tag path cannot declare its tag either).
	mustExec(`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value,
	                                   action, enabled, user_name, device_hostname)
	          VALUES ($1, 2, 'exit-node-vps', 'subnet', '1.2.3.0/24', 'accept', 1, 'tagged-devices', 'workpc')`, daniilID)
	return sqlDB
}

// TestGeneratorDeclaresEveryGrantTagB285 is the regression: every tag a grant
// names must be declared in the same document.
func TestGeneratorDeclaresEveryGrantTagB285(t *testing.T) {
	sqlDB := b285SeedDB(t)
	gen, err := GenerateACLLiveFormat(sqlDB)
	if err != nil {
		t.Fatalf("GenerateACLLiveFormat: %v", err)
	}

	// The grant must really use the node's tag, otherwise this test would pass
	// vacuously.
	if !strings.Contains(gen, `"src": ["tag:dev-daniil-workpc"]`) {
		t.Fatalf("the client rule did not resolve to the node's tag — the test would not guard the gap:\n%s", gen)
	}
	undeclared, err := headscale.UndeclaredTags(gen)
	if err != nil {
		t.Fatalf("UndeclaredTags(gen): %v\n%s", err, gen)
	}
	if len(undeclared) > 0 {
		t.Errorf("the generated policy references %d tag(s) it never declares: %s\n"+
			"headscale refuses such a document as a whole, so the ACL can never apply "+
			"(and in file mode the daemon will not start on it):\n%s",
			len(undeclared), strings.Join(undeclared, ", "), gen)
	}
}
