package acl

// acl_b398_test.go — B398 (2026-10-18): a DERIVED exit-node preference must not become
// a permission FILTER.
//
// Live evidence (the operator's installation): `device_exit_node_prefs` held
// `cyborg → tag:dev-infra-karolina` with `set_by_user_id=0` — written by the
// auto-reconciler, not by a human — and the generator emitted
//
//	{ "src": ["tag:dev-skyadmin-cyborg"], "dst": ["autogroup:internet"],
//	  "ip": ["*"], "via": ["tag:dev-infra-karolina"] }
//
// `via` on an `autogroup:internet` grant is a selector, not a preference: choosing
// emilia in the Tailscale client made the grant unmatched and headscale dropped ALL of
// that device's exit-node traffic — «если выставить emilia, то нет доступа ни к чему;
// karolina — всё грузится». Clearing the row from the panel restored access.
//
// B398 therefore pins only an EXPLICIT operator choice (`set_by_user_id > 0`); a derived
// row still feeds the prefix assignment (prefixowner.LoadClaims) but never narrows what
// the client may use.

import (
	"database/sql"
	"strings"
	"testing"

	"skygate/internal/db"
)

// b398ACLDB is a migrated SQLite database (the native install's backend) — this test
// must run in the ordinary gate, not only in CI's PostgreSQL job.
func b398ACLDB(t *testing.T) *sql.DB {
	t.Helper()
	t.Setenv("SKYGATE_BASE_DOMAIN", "ts.example.com")
	t.Setenv("SKYGATE_ADMIN_USER", "admin")
	sqlDB, err := db.OpenFastSQLiteForTest(t.TempDir() + "/b398acl.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.MigrateSQLite(sqlDB); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	return sqlDB
}

// seedB398 writes the live shape: one portal user, the device tag, an enabled subnet
// rule with NO exit node (the "engine decides" case) and the preference row.
func seedB398(t *testing.T, d *sql.DB, setBy int64) {
	t.Helper()
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	          VALUES (1, 'skyadmin', 'x', 1, 1)`)
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname)
	          VALUES ('101', 1, 'skyadmin', 'tag:dev-skyadmin-cyborg', 1, 0, 'cyborg')`)
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname)
	          VALUES ('102', 1, 'infra', 'tag:dev-infra-karolina', 1, 0, 'karolina')`)
	mustExec(`INSERT INTO device_rules (user_id, device_id, device_hostname, user_name, exit_node_id, target_type, target_value, action, enabled)
	          VALUES (1, 101, 'cyborg', 'skyadmin', '', 'subnet', '142.250.0.0/15', 'accept', 1)`)
	mustExec(`INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled)
	          VALUES (1, 'cyborg', 'tag:dev-infra-karolina', ?, 0, 1)`, setBy)
}

func b398InternetGrants(t *testing.T, policy, src string) (pinned, loose int) {
	t.Helper()
	for _, g := range parseGrantsForTest(t, policy) {
		if !strings.Contains(g, `"src": ["`+src+`"]`) || !strings.Contains(g, `"autogroup:internet"`) {
			continue
		}
		if strings.Contains(g, `"via":`) {
			pinned++
		} else {
			loose++
		}
	}
	return pinned, loose
}

// TestB398_DerivedPreferenceIsNotAFilter is the regression for the live blackhole: an
// auto-derived preference must leave the device with an UNPINNED internet grant, so any
// exit node chosen in the client works.
func TestB398_DerivedPreferenceIsNotAFilter(t *testing.T) {
	d := b398ACLDB(t)
	seedB398(t, d, 0) // derived: no human ever chose this
	policy, err := GenerateACLWithViaForPlane(d, "")
	if err != nil {
		t.Fatalf("GenerateACLWithViaForPlane: %v", err)
	}
	pinned, loose := b398InternetGrants(t, policy, "tag:dev-skyadmin-cyborg")
	if pinned != 0 {
		t.Errorf("a DERIVED preference produced %d pinned autogroup:internet grant(s): choosing another exit node in the client would block ALL traffic\n%s", pinned, policy)
	}
	if loose != 1 {
		t.Errorf("loose (unpinned) autogroup:internet grants = %d, want 1 — the device must be free to use any exit node", loose)
	}
}

// TestB398_ExplicitPinStillPins is the other half: an operator's explicit choice keeps
// its `via`, because that is a decision, not a guess.
func TestB398_ExplicitPinStillPins(t *testing.T) {
	d := b398ACLDB(t)
	seedB398(t, d, 1) // a human chose karolina explicitly
	policy, err := GenerateACLWithViaForPlane(d, "")
	if err != nil {
		t.Fatalf("GenerateACLWithViaForPlane: %v", err)
	}
	pinned, loose := b398InternetGrants(t, policy, "tag:dev-skyadmin-cyborg")
	if pinned != 1 {
		t.Errorf("an EXPLICIT pin produced %d pinned autogroup:internet grant(s), want 1\n%s", pinned, policy)
	}
	if loose != 0 {
		t.Errorf("loose grants = %d, want 0 — an additive unpinned grant would defeat the pin", loose)
	}
	if !strings.Contains(policy, `"via": ["tag:dev-infra-karolina"]`) {
		t.Error("the explicit pin does not name karolina")
	}
}
