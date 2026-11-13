// acl_b470_test.go — B470 (2026-11-14).
//
// THE LEGACY (`acls`) POLICY MUST CARRY PORTS IN EVERY dst ENTRY.
//
// Measured while moving the Cloudflare ranges to karolina with
// SKYGATE_ACL_VIA_ENABLED unset (the default): the generated policy was refused by
// headscale 0.29 with
//
//	setting policy: parsing policy: json: cannot unmarshal JSON string into Go v2.AliasWithPorts
//	within "/acls/5/dst/0": dst="tag:dev-skyadmin-cyborg":
//	port range "dev-skyadmin-cyborg": invalid port range format
//
// because the per-DEVICE mesh grants wrote a BARE tag in `dst` and relied on
// `"ip": ["*"]` for the ports. That `ip` field is a `grants`-schema idea: in the legacy
// schema headscale parses EVERY dst entry as host:ports, so a bare tag cannot be rescued
// and the whole document is rejected — on every install that runs with via disabled,
// which is the default. The same file's other legacy rules already wrote
// `tag:public:*`; only this one block was wrong.
//
// The test pins the shape at the GENERATOR (where the mistake was), not at the API: an
// install that cannot apply a policy at all has no working control plane.
package acl

import (
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"skygate/internal/db"
)

// b470DB is a migrated SQLite database with the minimum the per-DEVICE block needs:
// one portal user with THREE tagged devices (so more than one dst tag and more than one
// grant) and one with a single device (no inter-device traffic → no grant).
func b470DB(t *testing.T) *sql.DB {
	t.Helper()
	t.Setenv("SKYGATE_BASE_DOMAIN", "ts.example.com")
	t.Setenv("SKYGATE_ADMIN_USER", "skyadmin")
	sqlDB, err := db.OpenFastSQLiteForTest(t.TempDir() + "/b470acl.db")
	if err != nil {
		t.Skipf("sqlite dialect unavailable: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.MigrateSQLite(sqlDB); err != nil {
		t.Fatalf("migrate sqlite: %v", err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := sqlDB.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO portal_users (id, username, password_hash, is_admin, headscale_user_id)
	          VALUES (1, 'skyadmin', 'x', 1, 1), (2, 'michail', 'x', 0, 2)`)
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, tagged_by_user_id, tagged_at, hostname) VALUES
	          ('101', 2, 'michail',  'tag:dev-michail-basic',   2, 0, 'basic'),
	          ('102', 2, 'michail',  'tag:dev-michail-phone',   2, 0, 'phone'),
	          ('103', 2, 'michail',  'tag:dev-michail-tablet',  2, 0, 'tablet'),
	          ('201', 1, 'skyadmin', 'tag:dev-skyadmin-cyborg', 1, 0, 'cyborg')`)
	return sqlDB
}

// TestB470_LegacyPerDeviceGrantsCarryPorts is the regression: no bare tag may appear in
// `dst`, and the mesh grants themselves must still be emitted.
func TestB470_LegacyPerDeviceGrantsCarryPorts(t *testing.T) {
	d := b470DB(t)
	policy, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	var body struct {
		ACLs []struct {
			Src []string `json:"src"`
			Dst []string `json:"dst"`
			IP  []string `json:"ip"`
		} `json:"acls"`
	}
	if err := json.Unmarshal([]byte(policy), &body); err != nil {
		t.Fatalf("generated policy is not valid JSON: %v", err)
	}
	withPorts := regexp.MustCompile(`:\*$`)
	meshGrants := 0
	for _, a := range body.ACLs {
		for _, dst := range a.Dst {
			if !strings.HasPrefix(dst, "tag:dev-") {
				continue
			}
			if !withPorts.MatchString(dst) {
				t.Fatalf("dst entry %q carries no port suffix — headscale 0.29 rejects the WHOLE "+
					"policy with \"invalid port range format\" and an install with via disabled can "+
					"never apply a policy (B470)\npolicy:\n%s", dst, policy)
			}
		}
		if len(a.Src) == 1 && strings.HasPrefix(a.Src[0], "tag:dev-michail-") {
			meshGrants++
			if len(a.Dst) != 2 {
				t.Errorf("src %s must get the user's OTHER two devices as dst, got %v",
					a.Src[0], a.Dst)
			}
		}
	}
	if meshGrants != 3 {
		t.Fatalf("expected 3 per-device mesh grants for michail (one per device), got %d\npolicy:\n%s",
			meshGrants, policy)
	}
}

// TestB470_LegacyEveryDstEntryIsHostPorts — the narrower fix is not enough if any OTHER rule in the
// legacy document still writes a bare alias, because headscale refuses the document as a whole. This
// sweeps every dst entry of every rule (mesh, per-user, infra catch-all, ssh, subnet) through the
// grammar headscale applies, `host:ports`.
func TestB470_LegacyEveryDstEntryIsHostPorts(t *testing.T) {
	d := b470DB(t)
	policy, err := GenerateACL(d)
	if err != nil {
		t.Fatalf("GenerateACL: %v", err)
	}
	var body struct {
		ACLs []struct {
			Dst []string `json:"dst"`
		} `json:"acls"`
	}
	if err := json.Unmarshal([]byte(policy), &body); err != nil {
		t.Fatalf("generated policy is not valid JSON: %v", err)
	}
	// headscale splits a dst entry at its LAST colon into host:ports, and the ports half must be
	// `*`, a number or a port list. That is why a bare `tag:dev-alice-laptop` fails ("port range
	// \"alice-laptop\"") while `tag:dev-alice-laptop:*`, `user@example.com:*`, `10.0.1.0/24:22` and
	// `::/0:*` all pass — the check below reproduces that split instead of enumerating host shapes.
	hostPorts := regexp.MustCompile(`^(.+):(\*|[0-9]+(--?[0-9]+)?(,[0-9]+)*)$`)
	n := 0
	for i, a := range body.ACLs {
		for _, dst := range a.Dst {
			n++
			if !hostPorts.MatchString(dst) {
				t.Errorf("rule %d: dst entry %q is not host:ports — headscale 0.29 refuses the WHOLE "+
					"policy on it (B470)\npolicy:\n%s", i, dst, policy)
			}
		}
	}
	if n == 0 {
		t.Fatalf("no dst entries to check — the sweep verified nothing")
	}
}

// TestB470_ViaSchemaIsUntouched — the `grants` schema (SKYGATE_ACL_VIA_ENABLED=true) uses
// `ip`, not port suffixes, and must keep working exactly as before. The B470 fix is
// deliberately scoped to the legacy generator: adding `:*` in the grants schema would be
// a second, unreviewed format change.
func TestB470_ViaSchemaIsUntouched(t *testing.T) {
	d := b470DB(t)
	policy, err := GenerateACLWithVia(d)
	if err != nil {
		t.Fatalf("GenerateACLWithVia: %v", err)
	}
	var body struct {
		Grants []struct {
			Src []string `json:"src"`
			Dst []string `json:"dst"`
			IP  []string `json:"ip"`
		} `json:"grants"`
	}
	if err := json.Unmarshal([]byte(policy), &body); err != nil {
		t.Fatalf("generated grants policy is not valid JSON: %v", err)
	}
	if len(body.Grants) == 0 {
		t.Fatalf("no grants emitted:\n%s", policy)
	}
	for _, g := range body.Grants {
		for _, dst := range g.Dst {
			if strings.HasPrefix(dst, "tag:dev-") && strings.HasSuffix(dst, ":*") {
				t.Errorf("grants schema dst %q carries a port suffix; that schema uses `ip` "+
					"(B470 fix must not change it)", dst)
			}
		}
	}
}
