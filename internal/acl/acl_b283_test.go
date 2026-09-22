// internal/acl/acl_b283_test.go — B283 (2026-09-22).
//
// THE LIVE INCIDENT. After the only relay was given its own per-node tag
// (`tag:dev-infra-exit-node-vps`, B275's assignment table then pinning the
// per-CIDR grants to it), the generated policy carried
// `via: ["tag:dev-infra-exit-node-vps"]` on 19 prefixes — while `tagOwners`
// listed only the per-user/per-device tags, because the B275 owner tag set
// (`prefixowner.TagByPrefix`, used by `ViaForPrefix` in the grants loop) was
// never merged into `distinctVias` that feeds the tagOwners block.
//
// headscale refuses such a document as a whole ("tag not found"), and on a
// `policy.mode: file` host that means the daemon will not START: valid JSON on
// disk (python's json.tool accepted it), crash-loop counter 248, control plane
// down, every device gone from the portal.
//
// These tests pin the invariant at the generator's own output: every tag the
// grants reference must be declared in the same document.
package acl

import (
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"
	"skygate/internal/headscale"
)

// b283SeedDB builds a SQLite database that produces exactly one per-CIDR grant
// pinned to the B275 assignment table's owner tag.
func b283SeedDB(t *testing.T) *sql.DB {
	t.Helper()
	t.Setenv("SKYGATE_BASE_DOMAIN", "ts.example.com")
	t.Setenv("SKYGATE_ADMIN_USER", "admin")
	// The grants[]+via format is what the live host runs (same env the pipeline
	// reads at call time); without it GenerateACLLiveFormat emits the legacy
	// acls[] document, which does not carry the B275 per-prefix pins at all.
	t.Setenv("SKYGATE_ACL_VIA_ENABLED", "true")

	// A bare :memory: DSN gives each POOL CONNECTION its own database, so use
	// the shared-cache form (already a supported DSN, see internal/db/dialect.go)
	// — otherwise a query can land on a connection that never saw the seeds and
	// the test would pass vacuously. Pinning the pool to ONE connection is not
	// an option: the generator holds a rows cursor while issuing its next query.
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
	// The migration chain seeds portal rows (including 'infra'/'daniil'), so
	// upsert instead of insert and then read daniil's id back for the FK on
	// device_rules.
	mustExec(`INSERT INTO portal_users (username, password_hash, is_admin, headscale_user_id)
	          VALUES ('infra', 'x', 0, 3)
	          ON CONFLICT(username) DO UPDATE SET headscale_user_id = 3, is_admin = 0`)
	mustExec(`INSERT INTO portal_users (username, password_hash, is_admin, headscale_user_id)
	          VALUES ('daniil', 'x', 1, 1)
	          ON CONFLICT(username) DO UPDATE SET headscale_user_id = 1, is_admin = 1`)
	var daniilID int64
	if err := sqlDB.QueryRow(`SELECT id FROM portal_users WHERE username = 'daniil'`).Scan(&daniilID); err != nil {
		t.Fatalf("read daniil id: %v", err)
	}
	// The relay's node_owner_map row carries the per-node tag but is owned by
	// headscale's synthetic `tagged-devices` user, which is NOT a portal
	// account — exactly the live shape (`/admin/devices` showed
	// `exit-node-vps tagged-devices`, and BackfillInfra skips such a row
	// because its UPDATE only matches rows whose username is a portal user).
	// That is what makes the tag invisible to every OTHER tagOwners path
	// (`GetPerUserDeviceTags` needs a portal user), so the B275 owner tag has
	// to be declared from the assignment table — the hole this test guards.
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname)
	          VALUES ('1', 0, 'tagged-devices', 'tag:dev-infra-exit-node-vps', 'exit-node-vps'),
	                 ('2', 1, 'daniil', 'tag:dev-daniil-workpc', 'workpc')`)
	mustExec(`INSERT INTO device_rules (user_id, device_id, exit_node_id, target_type, target_value, action, enabled)
	          VALUES ($1, 2, 'exit-node-vps', 'subnet', '104.16.0.0/12', 'accept', 1)`, daniilID)
	mustExec(`INSERT INTO prefix_owner (prefix, exit_node_id, source, claims, devices)
	          VALUES ('104.16.0.0/12', 'exit-node-vps', 'explicit', 1, 1)`)
	return sqlDB
}

// TestGenerateACLLiveFormatDeclaresEveryViaTagB283 is the regression for the
// live outage: a `via` pin taken from the assignment table must be declared in
// `tagOwners` of the SAME document.
func TestGenerateACLLiveFormatDeclaresEveryViaTagB283(t *testing.T) {
	sqlDB := b283SeedDB(t)
	gen, err := GenerateACLLiveFormat(sqlDB)
	if err != nil {
		t.Fatalf("GenerateACLLiveFormat: %v", err)
	}

	// The pin must be there — otherwise this test would pass vacuously and stop
	// guarding the incident.
	if !strings.Contains(gen, `"via": ["tag:dev-infra-exit-node-vps"]`) {
		t.Fatalf("the generated policy lost the B275 owner pin entirely, so this test would not catch the bug:\n%s", gen)
	}
	if !strings.Contains(gen, `"tag:dev-infra-exit-node-vps"`) {
		t.Fatal("the generated policy does not mention the owner tag at all")
	}

	undeclared, err := headscale.UndeclaredTags(gen)
	if err != nil {
		t.Fatalf("UndeclaredTags(gen): %v\ngenerated policy:\n%s", err, gen)
	}
	if len(undeclared) > 0 {
		t.Errorf("generated policy references %d tag(s) missing from tagOwners: %s\n"+
			"headscale rejects such a document as a whole and will not start on it (live: crash-loop, "+
			"control plane down, every device gone) — the B275 owner tags must be merged into the "+
			"tagOwners set:\n%s", len(undeclared), strings.Join(undeclared, ", "), gen)
	}
}
