// acl_b356_test.go — B356 (2026-10-07).
//
// THE SAFETY NET IN THE ACL. The preference reconciler (internal/feature/exit_rules)
// eventually re-points a stale row, but the ACL is generated before it converges — and
// it may legitimately never converge (the pin is the operator's own, or no single owner
// can be derived). Meanwhile the B265 per-device grant carries `via=[<preferred tag>]`,
// which headscale applies as a permission FILTER, and that pinned grant REPLACES the
// loose one (grants are additive: emitting both would leave the pin defeated). So a
// preference naming a relay the monitor calls unusable is a full egress outage for that
// device.
//
// The live measurement (2026-10-07): `karolina` and `sharlotta` offline, every one of
// the 139 `prefix_owner` rows on `emilia`, and two devices still emitting
// `via: ["tag:dev-infra-karolina"]`.
//
// What these tests pin:
//
//	(1) an unusable preferred relay REMOVES the via pin (the device keeps egress);
//	(2) a USABLE preferred relay keeps it — including the `untagged` state, which B273
//	    defines as usable (a literal `state == "online"` test would break this);
//	(3) a relay we have NEVER measured keeps it — unknown is not broken;
//	(4) the fallback is what makes the device work: the loose unpinned grant appears.
package acl

import (
	"database/sql"
	"testing"

	"skygate/internal/db"
)

// TestUnusablePreferredRelay_Pure pins the decision itself, with no database — this is
// the half that runs in every environment.
func TestUnusablePreferredRelay_Pure(t *testing.T) {
	verdicts := map[string]relayVerdict{
		"karolina":       {Usable: false, State: "offline"},
		"sharlotta":      {Usable: false, State: "degraded"},
		"emilia":         {Usable: true, State: "online"},
		"untagged-relay": {Usable: true, State: "untagged"},
	}
	cases := []struct {
		name    string
		tag     string
		verdict map[string]relayVerdict
		wantOK  bool
		want    string
	}{
		{"offline relay loses the pin", "tag:dev-infra-karolina", verdicts, true, "offline"},
		{"degraded relay loses the pin", "tag:dev-infra-sharlotta", verdicts, true, "degraded"},
		{"online relay keeps the pin", "tag:dev-infra-emilia", verdicts, false, ""},
		{"untagged-but-working relay keeps the pin (B273: usable)", "tag:dev-infra-untagged-relay", verdicts, false, ""},
		{"legacy tag form of an offline relay loses the pin", "tag:exit-karolina", verdicts, true, "offline"},
		{"a relay with no health row keeps the pin (never measured)", "tag:dev-infra-unknown-host", verdicts, false, ""},
		{"a class tag names no relay, so nothing is decided", "tag:exit-node", verdicts, false, ""},
		{"an empty preference is not a pin at all", "", verdicts, false, ""},
		{"no health snapshot at all keeps every pin", "tag:dev-infra-karolina", nil, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state, ok := unusablePreferredRelay(c.tag, c.verdict)
			if ok != c.wantOK || state != c.want {
				t.Errorf("unusablePreferredRelay(%q) = (%q, %v), want (%q, %v)", c.tag, state, ok, c.want, c.wantOK)
			}
		})
	}
}

// b356ACLDB builds a SQLite database that produces one per-device
// `autogroup:internet` grant for `tester/workstation`, whose preference names
// `relay-1` (the relay under test).
func b356ACLDB(t *testing.T) *sql.DB {
	t.Helper()
	t.Setenv("SKYGATE_BASE_DOMAIN", "ts.example.com")
	t.Setenv("SKYGATE_ADMIN_USER", "admin")
	_, sqlDB, err := db.OpenWithDialect("sqlite:" + t.TempDir() + "/b356acl.db")
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
	          VALUES (1, 'tester', 'x', 1, 1) ON CONFLICT (id) DO NOTHING`)
	// The device tag is the one headscale carries AND the one the synthesised
	// `tag:dev-<user>-<host>` form equals, so the per-device grant loop sees it.
	mustExec(`INSERT INTO node_owner_map (node_id, headscale_user_id, username, tag, hostname) VALUES
	          ('101', 1, 'tester', 'tag:dev-tester-workstation', 'workstation'),
	          ('102', 1, 'infra',  'tag:dev-infra-relay-1',       'relay-1')`)
	mustExec(`INSERT INTO device_exit_node_prefs (user_id, device_hostname, exit_node_tag, set_by_user_id, updated_at, via_enabled)
	          VALUES (1, 'workstation', 'tag:dev-infra-relay-1', 1, 0, 1)`)
	return sqlDB
}

// policyGrants generates the live grants[] document and reports (pinned, loose) for
// the device's autogroup:internet grants.
func policyGrants(t *testing.T, d *sql.DB) (int, int) {
	t.Helper()
	policy, err := GenerateACLWithViaForPlane(d, "")
	if err != nil {
		t.Fatalf("GenerateACLWithViaForPlane: %v", err)
	}
	return countGrantsText(t, policy, "tag:dev-tester-workstation", `"autogroup:internet"`, true),
		countGrantsText(t, policy, "tag:dev-tester-workstation", `"autogroup:internet"`, false)
}

// TestB356_UnusablePreferredRelayRemovesThePin is property (1) and (4): the pinned
// grant disappears and the LOOSE one appears, because a device with no grant at all
// would have no internet either.
func TestB356_UnusablePreferredRelayRemovesThePin(t *testing.T) {
	d := b356ACLDB(t)
	if err := db.UpsertExitNodeHealth(d, db.ExitNodeHealth{NodeID: "102", Hostname: "relay-1", State: "offline", Healthy: false}); err != nil {
		t.Fatalf("seed health: %v", err)
	}
	pinned, loose := policyGrants(t, d)
	if pinned != 0 {
		t.Errorf("pinned autogroup:internet grants = %d, want 0 — a `via` naming an unusable relay is a permission filter, not a preference", pinned)
	}
	if loose != 1 {
		t.Errorf("loose (unpinned) autogroup:internet grants = %d, want 1 — the device must fall back, not be filtered into a black hole", loose)
	}
}

// TestB356_UsablePreferredRelayKeepsThePin is property (2): nothing changes for the
// devices that work. `untagged` is checked explicitly because B273 defines it as
// USABLE (the relay routes, only its bookkeeping tag is missing) and a literal
// `state == "online"` comparison in the ACL would silently unpin those devices.
func TestB356_UsablePreferredRelayKeepsThePin(t *testing.T) {
	for _, tc := range []struct {
		state   string
		healthy bool
	}{
		{"online", true},
		{"untagged", true},
	} {
		t.Run(tc.state, func(t *testing.T) {
			d := b356ACLDB(t)
			if err := db.UpsertExitNodeHealth(d, db.ExitNodeHealth{NodeID: "102", Hostname: "relay-1", State: tc.state, Healthy: tc.healthy}); err != nil {
				t.Fatalf("seed health: %v", err)
			}
			pinned, loose := policyGrants(t, d)
			if pinned != 1 {
				t.Errorf("pinned autogroup:internet grants = %d, want 1 (B265 behaviour is unchanged for a usable relay)", pinned)
			}
			if loose != 0 {
				t.Errorf("loose autogroup:internet grants = %d, want 0 (grants are additive: a loose grant would defeat the pin)", loose)
			}
		})
	}
}

// TestB356_NeverMeasuredRelayKeepsThePin is property (3): with no exit_node_health row
// the ACL cannot answer the question, and "I cannot answer" must not unpin the tailnet
// — a transient DB hiccup or a relay the monitor has not reached yet would otherwise
// silently rewrite every device's egress.
func TestB356_NeverMeasuredRelayKeepsThePin(t *testing.T) {
	d := b356ACLDB(t)
	pinned, loose := policyGrants(t, d)
	if pinned != 1 || loose != 0 {
		t.Errorf("with no health snapshot: pinned=%d loose=%d, want pinned=1 loose=0 (the pre-B356 document)", pinned, loose)
	}
}
